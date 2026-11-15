package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// The admin area: users, transcode jobs, retry and delete of movies.
//
// Every method first checks, in the database, that the caller is an admin
// who is not disabled and that their session is still live (requireAdmin).
// The role in the access token is not enough: a demoted admin keeps that
// claim until the token expires, up to 15 min. These endpoints are rare, so
// one query each is fine.
//
// The api never touches files: deleting a movie marks it deleting, and the
// watcher removes its files and then the row. Every action logs one INFO
// line: who, what, on which id.

var (
	// ErrUserNotFound means no user has that id.
	ErrUserNotFound = errors.New("user not found")
	// ErrSelfChange means an admin tried to disable or change the role of
	// their own account.
	ErrSelfChange = errors.New("you cannot disable your own account or change your own role")
	// ErrLastAdmin means the change would leave no active admin.
	ErrLastAdmin = errors.New("the last active admin cannot be disabled or demoted")
	// ErrNotFailed means a retry of a movie that is not failed.
	ErrNotFailed = errors.New("only a failed movie can be retried")
	// ErrJobActive means the movie has a queued or running transcode job.
	ErrJobActive = errors.New("the movie has a queued or running transcode job")
	// ErrNotReadyForRetranscode means a retranscode of a movie that is not
	// ready.
	ErrNotReadyForRetranscode = errors.New("only a ready movie can be retranscoded")
)

// AdminStore is the slice of the store the admin area needs.
type AdminStore interface {
	AdminCheck(ctx context.Context, userID, familyID string) (admin, live bool, err error)
	ListUserSummaries(ctx context.Context, f store.UserFilter) ([]model.UserSummary, int64, error)
	SetUserDisabled(ctx context.Context, actorID, targetID string, disabled bool) (model.UserSummary, error)
	SetUserRole(ctx context.Context, actorID, targetID string, role model.Role) (model.UserSummary, error)
	ListJobs(ctx context.Context, f store.JobFilter) ([]model.Job, int64, error)
	AdminMovies(ctx context.Context, f store.MovieFilter) ([]model.AdminMovie, int64, error)
	RetryMovie(ctx context.Context, id string) error
	RetranscodeMovie(ctx context.Context, id string) error
	DeleteMovie(ctx context.Context, id string) error
}

// Admin serves the /api/v1/admin endpoints.
type Admin struct {
	store AdminStore
	log   *slog.Logger
}

// NewAdmin returns the admin service over st.
func NewAdmin(st AdminStore, log *slog.Logger) *Admin {
	return &Admin{store: st, log: log}
}

// Page is a page request: Limit 1..MaxLimit, Offset >= 0.
type Page struct {
	Limit  int64
	Offset int64
}

func (p Page) check() error {
	if p.Limit < 1 || p.Limit > MaxLimit {
		return &ValidationError{"limit", fmt.Sprintf("must be between 1 and %d", MaxLimit)}
	}
	if p.Offset < 0 {
		return &ValidationError{"offset", "must not be negative"}
	}
	return nil
}

// UserPage is one page of users.
type UserPage struct {
	Users []model.UserSummary
	Total int64
	Page
}

// JobPage is one page of transcode jobs.
type JobPage struct {
	Jobs  []model.Job
	Total int64
	Page
}

// AdminMoviePage is one page of the admin movie list.
type AdminMoviePage struct {
	Movies []model.AdminMovie
	Total  int64
	Page
}

// requireAdmin: an admin who is not disabled (403 otherwise, whatever the
// token says), with a live session (401 otherwise, as /me/*).
func (a *Admin) requireAdmin(ctx context.Context, p Principal) error {
	if !model.ValidID(p.SessionID) {
		return ErrUnauthenticated
	}
	admin, live, err := a.store.AdminCheck(ctx, p.UserID, p.SessionID)
	switch {
	case err != nil:
		return err
	case !admin:
		return fmt.Errorf("%w: not an active admin", ErrForbidden)
	case !live:
		return ErrUnauthenticated
	}
	return nil
}

// search checks a search text: "" is none, else at most MaxQueryLength
// characters of valid UTF-8.
func search(field, q string) (*string, error) {
	q = strings.TrimSpace(q)
	if q == "" {
		return nil, nil
	}
	if !utf8.ValidString(q) || strings.ContainsRune(q, 0) {
		return nil, &ValidationError{field, "must be valid UTF-8 text"}
	}
	if utf8.RuneCountInString(q) > MaxQueryLength {
		return nil, &ValidationError{field, fmt.Sprintf("must be at most %d characters", MaxQueryLength)}
	}
	return &q, nil
}

// Users lists users by username; query is a case-insensitive substring of
// the username or the email.
func (a *Admin) Users(ctx context.Context, p Principal, query string, page Page) (UserPage, error) {
	if err := a.requireAdmin(ctx, p); err != nil {
		return UserPage{}, err
	}
	if err := page.check(); err != nil {
		return UserPage{}, err
	}
	q, err := search("q", query)
	if err != nil {
		return UserPage{}, err
	}
	users, total, err := a.store.ListUserSummaries(ctx, store.UserFilter{Query: q, Limit: page.Limit, Offset: page.Offset})
	if err != nil {
		return UserPage{}, err
	}
	return UserPage{Users: users, Total: total, Page: page}, nil
}

// SetDisabled disables (every session revoked at once) or enables a user.
// An admin cannot disable themselves (ErrSelfChange), and the last active
// admin cannot be disabled (ErrLastAdmin). Access tokens the user already
// holds keep working until they expire, at most 15 min.
func (a *Admin) SetDisabled(ctx context.Context, p Principal, userID string, disabled bool) (model.UserSummary, error) {
	action := "enable_user"
	if disabled {
		action = "disable_user"
	}
	return a.changeUser(ctx, p, userID, action, func(id string) (model.UserSummary, error) {
		if disabled && id == p.UserID {
			return model.UserSummary{}, ErrSelfChange
		}
		return a.store.SetUserDisabled(ctx, p.UserID, id, disabled)
	})
}

// SetRole changes a user's role. Not one's own (ErrSelfChange), and never
// leaving no active admin (ErrLastAdmin). The user's access tokens keep the
// old role until they expire; the admin endpoints check the database, so
// they refuse a demoted admin at once.
func (a *Admin) SetRole(ctx context.Context, p Principal, userID, role string) (model.UserSummary, error) {
	r := model.Role(role)
	return a.changeUser(ctx, p, userID, "set_role_"+role, func(id string) (model.UserSummary, error) {
		if !r.Valid() {
			return model.UserSummary{}, &ValidationError{"role", "must be user or admin"}
		}
		if id == p.UserID {
			return model.UserSummary{}, ErrSelfChange
		}
		return a.store.SetUserRole(ctx, p.UserID, id, r)
	})
}

// changeUser is the shared part of SetDisabled and SetRole: the admin
// check, the id, the store's errors and the log line.
func (a *Admin) changeUser(ctx context.Context, p Principal, userID, action string,
	change func(id string) (model.UserSummary, error),
) (model.UserSummary, error) {
	if err := a.requireAdmin(ctx, p); err != nil {
		return model.UserSummary{}, err
	}
	id := strings.ToLower(userID)
	if !model.ValidID(id) {
		return model.UserSummary{}, &ValidationError{"id", "must be a UUID"}
	}
	u, err := change(id)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return model.UserSummary{}, ErrUserNotFound
	case errors.Is(err, store.ErrLastAdmin):
		return model.UserSummary{}, ErrLastAdmin
	case errors.Is(err, store.ErrNotAdmin):
		return model.UserSummary{}, fmt.Errorf("%w: %w", ErrForbidden, err)
	case err != nil:
		return model.UserSummary{}, err
	}
	a.log.InfoContext(ctx, "admin action", "admin_id", p.UserID, "action", action, "target_id", id)
	return u, nil
}

// Jobs lists transcode jobs, newest first; status "" is every status.
func (a *Admin) Jobs(ctx context.Context, p Principal, status string, page Page) (JobPage, error) {
	if err := a.requireAdmin(ctx, p); err != nil {
		return JobPage{}, err
	}
	if err := page.check(); err != nil {
		return JobPage{}, err
	}
	f := store.JobFilter{Limit: page.Limit, Offset: page.Offset}
	if status != "" {
		s := model.JobStatus(status)
		if !s.Valid() {
			return JobPage{}, &ValidationError{"status", "must be one of queued, running, succeeded, failed"}
		}
		f.Status = &s
	}
	jobs, total, err := a.store.ListJobs(ctx, f)
	if err != nil {
		return JobPage{}, err
	}
	return JobPage{Jobs: jobs, Total: total, Page: page}, nil
}

// Movies lists movies of every status, deleting included, newest first,
// each with its active job; status "" is every status, query a title
// substring.
func (a *Admin) Movies(ctx context.Context, p Principal, status, query string, page Page) (AdminMoviePage, error) {
	if err := a.requireAdmin(ctx, p); err != nil {
		return AdminMoviePage{}, err
	}
	if err := page.check(); err != nil {
		return AdminMoviePage{}, err
	}
	f := store.MovieFilter{Limit: page.Limit, Offset: page.Offset}
	if status != "" {
		s := model.MovieStatus(status)
		if !s.Valid() {
			return AdminMoviePage{}, &ValidationError{"status", "must be one of pending, processing, ready, failed, deleting"}
		}
		f.Status = &s
	}
	q, err := search("q", query)
	if err != nil {
		return AdminMoviePage{}, err
	}
	f.Title = q
	movies, total, err := a.store.AdminMovies(ctx, f)
	if err != nil {
		return AdminMoviePage{}, err
	}
	return AdminMoviePage{Movies: movies, Total: total, Page: page}, nil
}

// RetryMovie queues a new transcode of a failed movie (ErrNotFailed
// otherwise; a second click finds it pending, or the one-active-job index
// refuses: ErrJobActive).
func (a *Admin) RetryMovie(ctx context.Context, p Principal, movieID string) error {
	return a.changeMovie(ctx, p, movieID, "retry_movie", a.store.RetryMovie)
}

// RetranscodeMovie queues a new transcode of a ready movie into a new
// version folder. The movie stays ready and plays its current version until
// the new one is published; the old version is removed by the watcher once
// every URL signed for it has expired. ErrNotReadyForRetranscode for any
// other status, ErrJobActive while a job is queued or running.
func (a *Admin) RetranscodeMovie(ctx context.Context, p Principal, movieID string) error {
	return a.changeMovie(ctx, p, movieID, "retranscode_movie", a.store.RetranscodeMovie)
}

// DeleteMovie marks a movie deleting: hidden from users and refused for
// playback at once, its files and row removed by the watcher's next run.
// ErrJobActive while a job is queued or running.
func (a *Admin) DeleteMovie(ctx context.Context, p Principal, movieID string) error {
	return a.changeMovie(ctx, p, movieID, "delete_movie", a.store.DeleteMovie)
}

func (a *Admin) changeMovie(ctx context.Context, p Principal, movieID, action string,
	change func(ctx context.Context, id string) error,
) error {
	if err := a.requireAdmin(ctx, p); err != nil {
		return err
	}
	id := strings.ToLower(movieID)
	if !model.ValidID(id) {
		return &ValidationError{"id", "must be a UUID"}
	}
	switch err := change(ctx, id); {
	case errors.Is(err, store.ErrNotFound):
		return ErrNotFound
	case errors.Is(err, store.ErrNotFailed):
		return ErrNotFailed
	case errors.Is(err, store.ErrJobActive):
		return ErrJobActive
	case errors.Is(err, store.ErrMovieNotReady):
		return ErrNotReadyForRetranscode
	case err != nil:
		return err
	}
	a.log.InfoContext(ctx, "admin action", "admin_id", p.UserID, "action", action, "target_id", id)
	return nil
}
