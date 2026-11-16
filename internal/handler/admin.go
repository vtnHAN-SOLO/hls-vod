package handler

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// Admin is the service behind /api/v1/admin. *service.Admin satisfies it.
// Each method checks the caller's admin role in the database itself.
type Admin interface {
	Users(ctx context.Context, p service.Principal, query string, page service.Page) (service.UserPage, error)
	SetDisabled(ctx context.Context, p service.Principal, userID string, disabled bool) (model.UserSummary, error)
	SetRole(ctx context.Context, p service.Principal, userID, role string) (model.UserSummary, error)
	Jobs(ctx context.Context, p service.Principal, status string, page service.Page) (service.JobPage, error)
	Movies(ctx context.Context, p service.Principal, status, query string, page service.Page) (service.AdminMoviePage, error)
	RetryMovie(ctx context.Context, p service.Principal, movieID string) error
	RetranscodeMovie(ctx context.Context, p service.Principal, movieID string) error
	DeleteMovie(ctx context.Context, p service.Principal, movieID string) error
	SetFeatured(ctx context.Context, p service.Principal, movieID string, featured bool) error
	EditMovie(ctx context.Context, p service.Principal, movieID string, e service.MovieEdit) (model.AdminMovie, error)
	PublishMovie(ctx context.Context, p service.Principal, movieID string) error
	UnpublishMovie(ctx context.Context, p service.Principal, movieID string) error
	UploadPoster(ctx context.Context, p service.Principal, movieID string, data []byte) (model.AdminMovie, error)
	DeletePoster(ctx context.Context, p service.Principal, movieID string) error
	RequireAdmin(ctx context.Context, p service.Principal) error
}

// adminAction is an admin action on one movie.
type adminAction func(ctx context.Context, p service.Principal, id string) error

// Admin error codes.
const (
	codeSelfChange = "self_change"
	codeLastAdmin  = "last_admin"
	codeNotFailed  = "not_failed"
	codeJobActive  = "job_active"
)

// adminUserJSON is a user as the admin area shows it. Never a hash or token.
type adminUserJSON struct {
	ID              string     `json:"id"`
	Username        string     `json:"username"`
	Email           *string    `json:"email"`
	EmailVerifiedAt *time.Time `json:"email_verified_at"`
	Role            string     `json:"role"`
	GoogleLinked    bool       `json:"google_linked"`
	HasPassword     bool       `json:"has_password"`
	DisabledAt      *time.Time `json:"disabled_at"`
	CreatedAt       time.Time  `json:"created_at"`
}

type adminUserListJSON struct {
	Items  []adminUserJSON `json:"items"`
	Total  int64           `json:"total"`
	Limit  int64           `json:"limit"`
	Offset int64           `json:"offset"`
}

type roleRequest struct {
	Role string `json:"role"`
}

// jobJSON is one transcode job. The backend detail is k8s_job_name
// (kubernetes) or local_pid (local dispatcher); error_msg is as stored.
type jobJSON struct {
	ID         string     `json:"id"`
	MovieID    string     `json:"movie_id"`
	MovieTitle string     `json:"movie_title"`
	Status     string     `json:"status"`
	Purpose    string     `json:"purpose"`
	StartedAt  *time.Time `json:"started_at"`
	FinishedAt *time.Time `json:"finished_at"`
	ErrorMsg   *string    `json:"error_msg"`
	K8sJobName *string    `json:"k8s_job_name"`
	LocalPID   *int       `json:"local_pid"`
	CreatedAt  time.Time  `json:"created_at"`
}

type jobListJSON struct {
	Items  []jobJSON `json:"items"`
	Total  int64     `json:"total"`
	Limit  int64     `json:"limit"`
	Offset int64     `json:"offset"`
}

// adminMovieJSON is a movie as the admin area lists it: any status, the
// status and purpose of its active job (null when none is queued or
// running), and when its old versions are removed (null when none wait).
type adminMovieJSON struct {
	ID                  string      `json:"id"`
	Title               string      `json:"title"`
	Year                *int        `json:"year"`
	Overview            *string     `json:"overview"`
	Status              string      `json:"status"`
	FeaturedAt          *time.Time  `json:"featured_at"`
	PublishedAt         *time.Time  `json:"published_at"`
	Poster              *posterJSON `json:"poster"`
	BackdropURL         *string     `json:"backdrop_url"`
	ActiveJob           *string     `json:"active_job"`
	ActiveJobPurpose    *string     `json:"active_job_purpose"`
	LibraryCleanupAfter *time.Time  `json:"library_cleanup_after"`
	CreatedAt           time.Time   `json:"created_at"`
	UpdatedAt           time.Time   `json:"updated_at"`
}

type adminMovieListJSON struct {
	Items  []adminMovieJSON `json:"items"`
	Total  int64            `json:"total"`
	Limit  int64            `json:"limit"`
	Offset int64            `json:"offset"`
}

func registerAdmin(mux *http.ServeMux, logger *slog.Logger, protect func(http.Handler) http.Handler, a Admin, c Catalog) {
	mux.Handle("GET /api/v1/admin/users", protect(adminUsers(logger, a)))
	mux.Handle("POST /api/v1/admin/users/{id}/disable", protect(adminSetDisabled(logger, a, true)))
	mux.Handle("POST /api/v1/admin/users/{id}/enable", protect(adminSetDisabled(logger, a, false)))
	mux.Handle("PUT /api/v1/admin/users/{id}/role", protect(adminSetRole(logger, a)))
	mux.Handle("GET /api/v1/admin/jobs", protect(adminJobs(logger, a)))
	mux.Handle("GET /api/v1/admin/movies", protect(adminMovies(logger, a, c)))
	mux.Handle("POST /api/v1/admin/movies/{id}/retry", protect(adminMovieAction(logger, a.RetryMovie)))
	mux.Handle("POST /api/v1/admin/movies/{id}/retranscode", protect(adminMovieAction(logger, a.RetranscodeMovie)))
	mux.Handle("DELETE /api/v1/admin/movies/{id}", protect(adminMovieAction(logger, a.DeleteMovie)))
	mux.Handle("PATCH /api/v1/admin/movies/{id}", protect(adminEditMovie(logger, a, c)))
	mux.Handle("POST /api/v1/admin/movies/{id}/feature", protect(adminFeature(logger, a, true)))
	mux.Handle("DELETE /api/v1/admin/movies/{id}/feature", protect(adminFeature(logger, a, false)))
	mux.Handle("POST /api/v1/admin/movies/{id}/publish", protect(adminNoContent(logger, a.PublishMovie)))
	mux.Handle("DELETE /api/v1/admin/movies/{id}/publish", protect(adminNoContent(logger, a.UnpublishMovie)))
	mux.Handle("PUT /api/v1/admin/movies/{id}/poster", protect(adminUploadPoster(logger, a, c)))
	mux.Handle("DELETE /api/v1/admin/movies/{id}/poster", protect(adminNoContent(logger, a.DeletePoster)))
}

// publishRefusals are the messages of a refused publish, by reason code.
var publishRefusals = map[string]string{
	service.ReasonNotReady:        "only a ready movie can be published",
	service.ReasonMissingPoster:   "upload a poster before publishing",
	service.ReasonMissingOverview: "write a description before publishing",
}

func writeAdminError(w http.ResponseWriter, r *http.Request, logger *slog.Logger, err error) {
	var refused *service.PublishRefusedError
	switch {
	case errors.Is(err, service.ErrUserNotFound):
		writeError(w, http.StatusNotFound, codeNotFound, "user not found")
	case errors.Is(err, service.ErrSelfChange):
		writeError(w, http.StatusConflict, codeSelfChange, "you cannot disable your own account or change your own role")
	case errors.Is(err, service.ErrLastAdmin):
		writeError(w, http.StatusConflict, codeLastAdmin, "this is the last active admin: make someone else admin first")
	case errors.Is(err, service.ErrNotFailed):
		writeError(w, http.StatusConflict, codeNotFailed, "only a failed movie can be retried")
	case errors.Is(err, service.ErrJobActive):
		writeError(w, http.StatusConflict, codeJobActive, "the movie has a queued or running transcode job")
	case errors.Is(err, service.ErrNotReadyForRetranscode):
		writeError(w, http.StatusConflict, codeNotReady, "only a ready movie can be retranscoded")
	case errors.Is(err, service.ErrNotReadyForFeature):
		writeError(w, http.StatusConflict, codeNotReady, "only a ready movie can be featured")
	case errors.Is(err, service.ErrNotPublishedForFeature):
		writeError(w, http.StatusConflict, codeNotPublished, "only a published movie can be featured: publish it first")
	case errors.Is(err, service.ErrMoviePublished):
		writeError(w, http.StatusConflict, codeMoviePublished, "the movie is published: unpublish it first")
	case errors.As(err, &refused):
		writeError(w, http.StatusConflict, refused.Reason, publishRefusals[refused.Reason])
	default:
		writeServiceError(w, r, logger, err)
	}
}

// pageParams reads ?limit= and ?offset=.
func pageParams(r *http.Request) (service.Page, error) {
	q := r.URL.Query()
	limit, err := intParam(q, "limit", service.DefaultLimit)
	if err != nil {
		return service.Page{}, err
	}
	offset, err := intParam(q, "offset", 0)
	if err != nil {
		return service.Page{}, err
	}
	return service.Page{Limit: limit, Offset: offset}, nil
}

// adminParams reads the page and the named single-valued parameters.
func adminParams(r *http.Request, names ...string) (service.Page, []string, error) {
	page, err := pageParams(r)
	if err != nil {
		return service.Page{}, nil, err
	}
	vals := make([]string, len(names))
	for i, n := range names {
		if vals[i], err = stringParam(r.URL.Query(), n); err != nil {
			return service.Page{}, nil, err
		}
	}
	return page, vals, nil
}

func writeAdminJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, v)
}

func toAdminUserJSON(u model.UserSummary) adminUserJSON {
	return adminUserJSON{ID: u.ID, Username: u.Username, Email: u.Email, EmailVerifiedAt: u.EmailVerifiedAt,
		Role: string(u.Role), GoogleLinked: u.GoogleLinked, HasPassword: u.HasPassword(),
		DisabledAt: u.DisabledAt, CreatedAt: u.CreatedAt.UTC()}
}

func adminUsers(logger *slog.Logger, a Admin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, vals, err := adminParams(r, "q")
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		res, err := a.Users(r.Context(), principal(r), vals[0], page)
		if err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		out := adminUserListJSON{Items: make([]adminUserJSON, 0, len(res.Users)), Total: res.Total, Limit: res.Limit, Offset: res.Offset}
		for _, u := range res.Users {
			out.Items = append(out.Items, toAdminUserJSON(u))
		}
		writeAdminJSON(w, out)
	}
}

func adminSetDisabled(logger *slog.Logger, a Admin, disabled bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, err := a.SetDisabled(r.Context(), principal(r), r.PathValue("id"), disabled)
		if err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		writeAdminJSON(w, toAdminUserJSON(u))
	}
}

func adminSetRole(logger *slog.Logger, a Admin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req roleRequest
		if !decodeJSON(w, r, &req, `body must be {"role": "user" | "admin"}`) {
			return
		}
		u, err := a.SetRole(r.Context(), principal(r), r.PathValue("id"), req.Role)
		if err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		writeAdminJSON(w, toAdminUserJSON(u))
	}
}

func adminJobs(logger *slog.Logger, a Admin) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, vals, err := adminParams(r, "status")
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		res, err := a.Jobs(r.Context(), principal(r), vals[0], page)
		if err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		out := jobListJSON{Items: make([]jobJSON, 0, len(res.Jobs)), Total: res.Total, Limit: res.Limit, Offset: res.Offset}
		for _, j := range res.Jobs {
			out.Items = append(out.Items, jobJSON{ID: j.ID, MovieID: j.MovieID, MovieTitle: j.MovieTitle,
				Status: string(j.Status), Purpose: string(j.Purpose), StartedAt: utc(j.StartedAt), FinishedAt: utc(j.FinishedAt), ErrorMsg: j.ErrorMsg,
				K8sJobName: j.K8sJobName, LocalPID: j.LocalPID, CreatedAt: j.CreatedAt.UTC()})
		}
		writeAdminJSON(w, out)
	}
}

func adminMovies(logger *slog.Logger, a Admin, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		page, vals, err := adminParams(r, "status", "q")
		if err != nil {
			writeServiceError(w, r, logger, err)
			return
		}
		res, err := a.Movies(r.Context(), principal(r), vals[0], vals[1], page)
		if err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		out := adminMovieListJSON{Items: make([]adminMovieJSON, 0, len(res.Movies)), Total: res.Total, Limit: res.Limit, Offset: res.Offset}
		for _, m := range res.Movies {
			out.Items = append(out.Items, toAdminMovieJSON(c, m))
		}
		writeAdminJSON(w, out)
	}
}

// toAdminMovieJSON signs the poster and backdrop URLs: an admin may see
// every movie.
func toAdminMovieJSON(c Catalog, m model.AdminMovie) adminMovieJSON {
	var job, purpose *string
	if m.ActiveJob != nil && m.ActiveJobPurpose != nil {
		s, p := string(*m.ActiveJob), string(*m.ActiveJobPurpose)
		job, purpose = &s, &p
	}
	return adminMovieJSON{ID: m.ID, Title: m.Title, Year: m.Year, Overview: m.Overview, Status: string(m.Status),
		FeaturedAt: utc(m.FeaturedAt), PublishedAt: utc(m.PublishedAt),
		Poster: toPosterJSON(c.Poster(m.Movie)), BackdropURL: c.BackdropURL(m.Movie), ActiveJob: job, ActiveJobPurpose: purpose,
		LibraryCleanupAfter: utc(m.LibraryCleanupAfter), CreatedAt: m.CreatedAt.UTC(), UpdatedAt: m.UpdatedAt.UTC()}
}

// optional is a JSON field of a partial update. UnmarshalJSON runs only for
// a key that is present, so Set tells "absent" from null.
type optional[T any] struct {
	Set   bool
	Value *T
}

func (o *optional[T]) UnmarshalJSON(b []byte) error {
	o.Set = true
	return json.Unmarshal(b, &o.Value)
}

func (o optional[T]) service() service.Optional[T] {
	return service.Optional[T]{Set: o.Set, Value: o.Value}
}

type movieEditRequest struct {
	Title    optional[string] `json:"title"`
	Year     optional[int64]  `json:"year"`
	Overview optional[string] `json:"overview"`
}

// maxMovieEditBody fits a 2000-character overview of 4-byte characters,
// escaped.
const maxMovieEditBody = 64 << 10

func adminEditMovie(logger *slog.Logger, a Admin, c Catalog) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req movieEditRequest
		if !decodeJSONMax(w, r, &req, `body must be {"title"?: string, "year"?: integer | null, "overview"?: string | null}`, maxMovieEditBody) {
			return
		}
		m, err := a.EditMovie(r.Context(), principal(r), r.PathValue("id"), service.MovieEdit{
			Title: req.Title.service(), Year: req.Year.service(), Overview: req.Overview.service(),
		})
		if err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		writeAdminJSON(w, toAdminMovieJSON(c, m))
	}
}

func adminFeature(logger *slog.Logger, a Admin, featured bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := a.SetFeatured(r.Context(), principal(r), r.PathValue("id"), featured); err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusNoContent)
	}
}

// adminMovieAction answers 202: a retry or retranscode is queued, a delete
// is finished by the watcher.
func adminMovieAction(logger *slog.Logger, act adminAction) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if err := act(r.Context(), principal(r), r.PathValue("id")); err != nil {
			writeAdminError(w, r, logger, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusAccepted)
	}
}

func utc(t *time.Time) *time.Time {
	if t == nil {
		return nil
	}
	u := t.UTC()
	return &u
}
