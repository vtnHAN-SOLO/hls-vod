package handler

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// fakeAdminStore is an in-memory service.AdminStore. The locking and the
// last-admin rule under concurrency are tested against Postgres (store);
// here: the HTTP side and the service's rules.
type fakeAdminStore struct {
	mu     sync.Mutex
	users  map[string]*model.UserSummary // by id
	dead   map[string]bool               // users whose session is revoked
	jobs   []model.Job
	movies map[string]*model.AdminMovie
	err    error // returned by every call when set
	// changeErr is returned by the user changes, as Postgres would in a
	// race (ErrLastAdmin).
	changeErr error
}

func newFakeAdminStore(t *testing.T) *fakeAdminStore {
	t.Helper()
	created := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	email := "viewer@example.com"
	s := &fakeAdminStore{
		users: map[string]*model.UserSummary{
			adminID: {User: model.User{ID: adminID, Username: "admin", PasswordHash: fixtures(t).adminHash, Role: model.RoleAdmin, CreatedAt: created}},
			viewerID: {User: model.User{ID: viewerID, Username: "viewer", Role: model.RoleUser, Email: &email,
				EmailVerifiedAt: &created, CreatedAt: created}, GoogleLinked: true},
		},
		dead:   map[string]bool{},
		movies: map[string]*model.AdminMovie{},
	}
	running, ingestPurpose := model.JobRunning, model.PurposeIngest
	overview := "A movie viewers can see."
	for _, m := range []model.AdminMovie{
		{Movie: model.Movie{ID: readyID, Title: "Ready", Status: model.StatusReady, Overview: &overview, PublishedAt: &created,
			PosterHash: readyPosterHash[:], PosterWidths: []int{300, 600, 1000}, CreatedAt: created, UpdatedAt: created}},
		// Ready, not published, no poster, no overview.
		{Movie: model.Movie{ID: unpublishedID, Title: "Draft", Status: model.StatusReady, CreatedAt: created, UpdatedAt: created}},
		{Movie: model.Movie{ID: failedID, Title: "Broken", Status: model.StatusFailed, CreatedAt: created, UpdatedAt: created}},
		{Movie: model.Movie{ID: busyID, Title: "Busy", Status: model.StatusProcessing, CreatedAt: created, UpdatedAt: created}, ActiveJob: &running, ActiveJobPurpose: &ingestPurpose},
	} {
		s.movies[m.ID] = &m
	}
	msg, pid := "ffprobe: invalid data", 4242
	s.jobs = []model.Job{
		{ID: model.NewID(), MovieID: busyID, MovieTitle: "Busy", Status: model.JobRunning, Purpose: model.PurposeIngest, StartedAt: &created, LocalPID: &pid, CreatedAt: created},
		{ID: model.NewID(), MovieID: failedID, MovieTitle: "Broken", Status: model.JobFailed, Purpose: model.PurposeRetranscode, StartedAt: &created,
			FinishedAt: &created, ErrorMsg: &msg, CreatedAt: created},
	}
	return s
}

const busyID = processingID

func (s *fakeAdminStore) AdminCheck(_ context.Context, userID, _ string) (bool, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return false, false, s.err
	}
	u, ok := s.users[userID]
	return ok && u.Role == model.RoleAdmin && !u.Disabled(), !s.dead[userID], nil
}

func (s *fakeAdminStore) ListUserSummaries(_ context.Context, f store.UserFilter) ([]model.UserSummary, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, 0, s.err
	}
	var out []model.UserSummary
	for _, id := range []string{adminID, viewerID} { // by username
		u := s.users[id]
		if f.Query == nil || strings.Contains(u.Username, *f.Query) {
			out = append(out, *u)
		}
	}
	total := int64(len(out))
	if f.Offset >= total {
		return nil, total, nil
	}
	return out[f.Offset:min(total, f.Offset+f.Limit)], total, nil
}

func (s *fakeAdminStore) change(actor, target string, apply func(u *model.UserSummary)) (model.UserSummary, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.UserSummary{}, s.err
	}
	if s.changeErr != nil {
		return model.UserSummary{}, s.changeErr
	}
	u, ok := s.users[target]
	if !ok {
		return model.UserSummary{}, store.ErrNotFound
	}
	if a := s.users[actor]; a == nil || a.Role != model.RoleAdmin || a.Disabled() {
		return model.UserSummary{}, store.ErrNotAdmin
	}
	apply(u)
	return *u, nil
}

func (s *fakeAdminStore) SetUserDisabled(_ context.Context, actor, target string, disabled bool) (model.UserSummary, error) {
	return s.change(actor, target, func(u *model.UserSummary) {
		if !disabled {
			u.DisabledAt = nil
		} else if u.DisabledAt == nil {
			now := time.Now()
			u.DisabledAt = &now
		}
	})
}

func (s *fakeAdminStore) SetUserRole(_ context.Context, actor, target string, role model.Role) (model.UserSummary, error) {
	return s.change(actor, target, func(u *model.UserSummary) { u.Role = role })
}

func (s *fakeAdminStore) ListJobs(_ context.Context, f store.JobFilter) ([]model.Job, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, 0, s.err
	}
	var out []model.Job
	for _, j := range s.jobs {
		if f.Status == nil || j.Status == *f.Status {
			out = append(out, j)
		}
	}
	return out, int64(len(out)), nil
}

func (s *fakeAdminStore) AdminMovies(_ context.Context, f store.MovieFilter) ([]model.AdminMovie, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, 0, s.err
	}
	var out []model.AdminMovie
	for _, id := range []string{busyID, failedID, readyID, unpublishedID} {
		m, ok := s.movies[id]
		if ok && (f.Status == nil || m.Status == *f.Status) && (f.Title == nil || strings.Contains(m.Title, *f.Title)) {
			out = append(out, *m)
		}
	}
	return out, int64(len(out)), nil
}

func (s *fakeAdminStore) RetryMovie(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	m, ok := s.movies[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case m.Status != model.StatusFailed:
		return store.ErrNotFailed
	case m.ActiveJob != nil:
		return store.ErrJobActive
	}
	queued := model.JobQueued
	m.Status, m.ActiveJob = model.StatusPending, &queued
	return nil
}

func (s *fakeAdminStore) RetranscodeMovie(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	m, ok := s.movies[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case m.Status != model.StatusReady:
		return store.ErrMovieNotReady
	case m.ActiveJob != nil:
		return store.ErrJobActive
	}
	queued, purpose := model.JobQueued, model.PurposeRetranscode
	m.ActiveJob, m.ActiveJobPurpose = &queued, &purpose
	return nil
}

func (s *fakeAdminStore) DeleteMovie(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	m, ok := s.movies[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case m.ActiveJob != nil:
		return store.ErrJobActive
	}
	m.Status = model.StatusDeleting
	return nil
}

func (s *fakeAdminStore) SetFeatured(_ context.Context, id string, featured bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	m, ok := s.movies[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case featured && m.Status != model.StatusReady:
		return store.ErrMovieNotReady
	case featured && m.PublishedAt == nil:
		return store.ErrMovieNotPublished
	}
	m.FeaturedAt = nil
	if featured {
		now := time.Now()
		m.FeaturedAt = &now
	}
	return nil
}

func (s *fakeAdminStore) UpdateMovieMetadata(_ context.Context, id string, md model.MovieMetadata) (model.AdminMovie, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.AdminMovie{}, s.err
	}
	m, ok := s.movies[id]
	if !ok {
		return model.AdminMovie{}, store.ErrNotFound
	}
	if md.Title != nil {
		m.Title = *md.Title
	}
	if md.ClearYear {
		m.Year = nil
	} else if md.Year != nil {
		m.Year = md.Year
	}
	if md.ClearOverview && m.PublishedAt != nil {
		return model.AdminMovie{}, store.ErrMoviePublished
	}
	if md.ClearOverview {
		m.Overview = nil
	} else if md.Overview != nil {
		m.Overview = md.Overview
	}
	return *m, nil
}

func (s *fakeAdminStore) GetAdminMovie(_ context.Context, id string) (model.AdminMovie, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return model.AdminMovie{}, s.err
	}
	m, ok := s.movies[id]
	if !ok {
		return model.AdminMovie{}, store.ErrNotFound
	}
	return *m, nil
}

// PublishMovie checks the rule the movies_published_check constraint
// enforces in Postgres, in the store's order.
func (s *fakeAdminStore) PublishMovie(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	m, ok := s.movies[id]
	switch {
	case !ok:
		return store.ErrNotFound
	case m.Status != model.StatusReady:
		return &store.PublishRefusedError{Reason: store.RefusedNotReady}
	case m.PosterHash == nil:
		return &store.PublishRefusedError{Reason: store.RefusedMissingPoster}
	case m.Overview == nil || strings.TrimSpace(*m.Overview) == "":
		return &store.PublishRefusedError{Reason: store.RefusedMissingOverview}
	}
	if m.PublishedAt == nil {
		now := time.Now()
		m.PublishedAt = &now
	}
	return nil
}

func (s *fakeAdminStore) UnpublishMovie(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	m, ok := s.movies[id]
	if !ok {
		return store.ErrNotFound
	}
	m.PublishedAt, m.FeaturedAt = nil, nil
	return nil
}

// fakeAdminStore is the poster.Store too, so an upload shows in the movie.
func (s *fakeAdminStore) Put(_ context.Context, id string, ps poster.Poster) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.movies[id]
	if !ok {
		return poster.ErrNotFound
	}
	m.PosterHash = ps.Hash[:]
	m.PosterWidths = nil
	for _, v := range ps.Variants {
		m.PosterWidths = append(m.PosterWidths, v.Width)
	}
	return nil
}

func (s *fakeAdminStore) Get(context.Context, string, int, []byte) ([]byte, error) {
	return nil, poster.ErrNotFound
}

func (s *fakeAdminStore) Delete(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.movies[id]
	switch {
	case !ok:
		return poster.ErrNotFound
	case m.PublishedAt != nil:
		return poster.ErrPublished
	}
	m.PosterHash, m.PosterWidths = nil, nil
	return nil
}

func newAdminServer(t *testing.T, st *fakeAdminStore) *httptest.Server {
	t.Helper()
	f := fixtures(t)
	logger := slog.New(slog.DiscardHandler)
	as := newFakeAuthStore(t)
	a := service.NewAuth(as, f.hasher, f.signer, auth.NewThrottle(), auth.NewThrottle(), logger)
	signup := service.NewRegistration(newFakeRegStore(as), f.hasher, &fakeCaptcha{}, &fakeMailer{}, a,
		service.RegistrationConfig{Open: true, BaseURL: testBaseURL, TurnstileSiteKey: testSiteKey}, logger)
	srv := httptest.NewServer(NewRouter(logger, prometheus.NewRegistry(), &fakePinger{}, service.NewCatalog(&fakeStore{}, st, f.media),
		a, signup, Options{SecureCookies: true, Admin: service.NewAdmin(st, st, logger)}))
	t.Cleanup(srv.Close)
	return srv
}

// adminEndpoints is every /api/v1/admin endpoint, with a valid request.
var adminEndpoints = []struct{ method, path, body string }{
	{http.MethodGet, "/api/v1/admin/users", ""},
	{http.MethodPost, "/api/v1/admin/users/" + viewerID + "/disable", ""},
	{http.MethodPost, "/api/v1/admin/users/" + viewerID + "/enable", ""},
	{http.MethodPut, "/api/v1/admin/users/" + viewerID + "/role", `{"role":"admin"}`},
	{http.MethodGet, "/api/v1/admin/jobs", ""},
	{http.MethodGet, "/api/v1/admin/movies", ""},
	{http.MethodPost, "/api/v1/admin/movies/" + failedID + "/retry", ""},
	{http.MethodPost, "/api/v1/admin/movies/" + readyID + "/retranscode", ""},
	{http.MethodDelete, "/api/v1/admin/movies/" + readyID, ""},
	{http.MethodPatch, "/api/v1/admin/movies/" + readyID, `{"title":"Renamed"}`},
	{http.MethodPost, "/api/v1/admin/movies/" + readyID + "/feature", ""},
	{http.MethodDelete, "/api/v1/admin/movies/" + readyID + "/feature", ""},
}

// Every admin endpoint asks the database, not the token: a user, a demoted
// admin whose token still says admin, and a disabled admin all get 403; no
// token or a revoked session gets 401. Nothing is changed.
func TestAdminEndpointsRefuseNonAdmins(t *testing.T) {
	f := fixtures(t)
	for _, tt := range []struct {
		name   string
		token  string
		setup  func(s *fakeAdminStore)
		status int
	}{
		{"user", f.viewerToken, nil, http.StatusForbidden},
		{"demoted admin, token still says admin", f.adminToken,
			func(s *fakeAdminStore) { s.users[adminID].Role = model.RoleUser }, http.StatusForbidden},
		{"disabled admin", f.adminToken, func(s *fakeAdminStore) {
			now := time.Now()
			s.users[adminID].DisabledAt = &now
			s.dead[adminID] = true // disabling revoked their sessions
		}, http.StatusForbidden},
		{"admin whose session was revoked", f.adminToken, func(s *fakeAdminStore) { s.dead[adminID] = true }, http.StatusUnauthorized},
		{"no token", "", nil, http.StatusUnauthorized},
	} {
		for _, ep := range adminEndpoints {
			t.Run(tt.name+" "+ep.method+" "+ep.path, func(t *testing.T) {
				st := newFakeAdminStore(t)
				if tt.setup != nil {
					tt.setup(st)
				}
				r := call(t, newAdminServer(t, st), ep.method, ep.path, tt.token, ep.body)
				if r.status != tt.status {
					t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
				}
				if st.users[viewerID].DisabledAt != nil || st.users[viewerID].Role != model.RoleUser ||
					st.movies[failedID].Status != model.StatusFailed || st.movies[readyID].Status != model.StatusReady ||
					st.movies[readyID].ActiveJob != nil || st.movies[readyID].Title != "Ready" ||
					st.movies[readyID].FeaturedAt != nil {
					t.Fatal("a refused request changed something")
				}
			})
		}
	}
}

func TestAdminUsers(t *testing.T) {
	f := fixtures(t)
	st := newFakeAdminStore(t)
	srv := newAdminServer(t, st)
	as := func(method, path, body string) response {
		t.Helper()
		return call(t, srv, method, path, f.adminToken, body)
	}

	var list adminUserListJSON
	r := as(http.MethodGet, "/api/v1/admin/users", "")
	if r.status != http.StatusOK || r.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("list = %d %s", r.status, r.body)
	}
	decode(t, r.body, &list)
	if list.Total != 2 || len(list.Items) != 2 || list.Items[1].Username != "viewer" || !list.Items[1].GoogleLinked ||
		list.Items[1].HasPassword || !list.Items[0].HasPassword {
		t.Fatalf("list = %+v", list)
	}
	for _, bad := range []string{"bcrypt", "argon2", "hash", "token"} {
		if strings.Contains(string(r.body), bad) {
			t.Fatalf("the user list mentions %q: %s", bad, r.body)
		}
	}
	decode(t, as(http.MethodGet, "/api/v1/admin/users?q=view&limit=1", "").body, &list)
	if list.Total != 1 || list.Limit != 1 || list.Items[0].ID != viewerID {
		t.Fatalf("search = %+v", list)
	}
	for _, q := range []string{"?limit=0", "?limit=101", "?offset=-1", "?q=a&q=b", "?q=" + strings.Repeat("x", 101)} {
		if r := as(http.MethodGet, "/api/v1/admin/users"+q, ""); r.status != http.StatusBadRequest {
			t.Errorf("%s = %d", q, r.status)
		}
	}

	var u adminUserJSON
	r = as(http.MethodPost, "/api/v1/admin/users/"+viewerID+"/disable", "")
	decode(t, r.body, &u)
	if r.status != http.StatusOK || u.DisabledAt == nil {
		t.Fatalf("disable = %d %s", r.status, r.body)
	}
	r = as(http.MethodPost, "/api/v1/admin/users/"+strings.ToUpper(viewerID)+"/enable", "")
	decode(t, r.body, &u)
	if r.status != http.StatusOK || u.DisabledAt != nil {
		t.Fatalf("enable = %d %s", r.status, r.body)
	}
	r = as(http.MethodPut, "/api/v1/admin/users/"+viewerID+"/role", `{"role":"admin"}`)
	decode(t, r.body, &u)
	if r.status != http.StatusOK || u.Role != "admin" {
		t.Fatalf("role = %d %s", r.status, r.body)
	}
	if r := as(http.MethodPut, "/api/v1/admin/users/"+viewerID+"/role", `{"role":"user"}`); r.status != http.StatusOK {
		t.Fatalf("role back = %d %s", r.status, r.body)
	}

	for _, tt := range []struct {
		name, method, path, body string
		status                   int
		code                     string
	}{
		{"disable yourself", http.MethodPost, "/api/v1/admin/users/" + adminID + "/disable", "", http.StatusConflict, codeSelfChange},
		{"demote yourself", http.MethodPut, "/api/v1/admin/users/" + adminID + "/role", `{"role":"user"}`, http.StatusConflict, codeSelfChange},
		{"unknown user", http.MethodPost, "/api/v1/admin/users/" + model.NewID() + "/disable", "", http.StatusNotFound, codeNotFound},
		{"unknown user, enable", http.MethodPost, "/api/v1/admin/users/" + model.NewID() + "/enable", "", http.StatusNotFound, codeNotFound},
		{"unknown user, role", http.MethodPut, "/api/v1/admin/users/" + model.NewID() + "/role", `{"role":"user"}`, http.StatusNotFound, codeNotFound},
		{"not a uuid", http.MethodPost, "/api/v1/admin/users/..%2Fetc/disable", "", http.StatusBadRequest, codeInvalidRequest},
		{"not a uuid, enable", http.MethodPost, "/api/v1/admin/users/x/enable", "", http.StatusBadRequest, codeInvalidRequest},
		{"no such role", http.MethodPut, "/api/v1/admin/users/" + viewerID + "/role", `{"role":"root"}`, http.StatusBadRequest, codeInvalidRequest},
		{"unknown field", http.MethodPut, "/api/v1/admin/users/" + viewerID + "/role", `{"role":"user","x":1}`, http.StatusBadRequest, codeInvalidRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := as(tt.method, tt.path, tt.body)
			if r.status != tt.status || errorCode(t, r) != tt.code {
				t.Fatalf("= %d %s, want %d %s", r.status, r.body, tt.status, tt.code)
			}
		})
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/api/v1/admin/users/"+viewerID+"/role", strings.NewReader(`{"role":"user"}`))
	req.Header.Set("Authorization", "Bearer "+f.adminToken)
	req.Header.Set("Content-Type", "text/plain")
	if r := send(t, srv, req); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", r.status)
	}

	// The last admin, as Postgres answers when two admins demote or
	// disable each other at once.
	st.mu.Lock()
	st.changeErr = store.ErrLastAdmin
	st.mu.Unlock()
	for _, ep := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/admin/users/" + viewerID + "/disable", ""},
		{http.MethodPut, "/api/v1/admin/users/" + viewerID + "/role", `{"role":"user"}`},
	} {
		if r := as(ep.method, ep.path, ep.body); r.status != http.StatusConflict || errorCode(t, r) != codeLastAdmin {
			t.Fatalf("%s %s = %d %s", ep.method, ep.path, r.status, r.body)
		}
	}
	st.mu.Lock()
	st.changeErr, st.err = nil, errors.New("db down")
	st.mu.Unlock()
	for _, ep := range adminEndpoints {
		if r := as(ep.method, ep.path, ep.body); r.status != http.StatusInternalServerError {
			t.Errorf("%s %s, store down = %d", ep.method, ep.path, r.status)
		}
	}
	// The admin check itself down: 500 too, never a pass.
	if r := as(http.MethodGet, "/api/v1/admin/users", ""); r.status != http.StatusInternalServerError {
		t.Fatalf("check down = %d", r.status)
	}
}

func TestAdminJobsAndMovies(t *testing.T) {
	f := fixtures(t)
	st := newFakeAdminStore(t)
	srv := newAdminServer(t, st)
	as := func(method, path string) response {
		t.Helper()
		return call(t, srv, method, path, f.adminToken, "")
	}

	var jobs jobListJSON
	r := as(http.MethodGet, "/api/v1/admin/jobs")
	decode(t, r.body, &jobs)
	if r.status != http.StatusOK || jobs.Total != 2 || jobs.Items[0].LocalPID == nil || jobs.Items[1].ErrorMsg == nil {
		t.Fatalf("jobs = %d %s", r.status, r.body)
	}
	decode(t, as(http.MethodGet, "/api/v1/admin/jobs?status=failed").body, &jobs)
	if jobs.Total != 1 || jobs.Items[0].MovieTitle != "Broken" {
		t.Fatalf("failed jobs = %+v", jobs)
	}
	for _, q := range []string{"?status=paused", "?limit=0", "?status=a&status=b"} {
		if r := as(http.MethodGet, "/api/v1/admin/jobs"+q); r.status != http.StatusBadRequest {
			t.Errorf("jobs%s = %d", q, r.status)
		}
	}

	var movies adminMovieListJSON
	decode(t, as(http.MethodGet, "/api/v1/admin/movies").body, &movies)
	if movies.Total != 4 || movies.Items[0].ActiveJob == nil || *movies.Items[0].ActiveJob != "running" || movies.Items[1].ActiveJob != nil {
		t.Fatalf("movies = %+v", movies)
	}
	for _, q := range []string{"?status=gone", "?offset=-1", "?q=a&q=b"} {
		if r := as(http.MethodGet, "/api/v1/admin/movies"+q); r.status != http.StatusBadRequest {
			t.Errorf("movies%s = %d", q, r.status)
		}
	}

	// Retry: only failed, once.
	if r := as(http.MethodPost, "/api/v1/admin/movies/"+failedID+"/retry"); r.status != http.StatusAccepted || len(r.body) != 0 {
		t.Fatalf("retry = %d %s", r.status, r.body)
	}
	if r := as(http.MethodPost, "/api/v1/admin/movies/"+failedID+"/retry"); r.status != http.StatusConflict || errorCode(t, r) != codeNotFailed {
		t.Fatalf("second retry = %d %s", r.status, r.body)
	}
	if r := as(http.MethodPost, "/api/v1/admin/movies/"+readyID+"/retry"); r.status != http.StatusConflict || errorCode(t, r) != codeNotFailed {
		t.Fatalf("retry ready = %d %s", r.status, r.body)
	}
	// Delete: refused while a job is active, then deleting.
	for _, id := range []string{busyID, failedID} {
		if r := as(http.MethodDelete, "/api/v1/admin/movies/"+id); r.status != http.StatusConflict || errorCode(t, r) != codeJobActive {
			t.Fatalf("delete %s with an active job = %d %s", id, r.status, r.body)
		}
	}
	st.mu.Lock()
	st.movies[failedID].Status, st.movies[failedID].ActiveJob = model.StatusFailed, nil
	queued := model.JobQueued
	st.movies[readyID].Status, st.movies[readyID].ActiveJob = model.StatusFailed, &queued // a retry that raced the first
	st.mu.Unlock()
	if r := as(http.MethodPost, "/api/v1/admin/movies/"+readyID+"/retry"); r.status != http.StatusConflict || errorCode(t, r) != codeJobActive {
		t.Fatalf("retry with an active job = %d %s", r.status, r.body)
	}
	if r := as(http.MethodDelete, "/api/v1/admin/movies/"+failedID); r.status != http.StatusAccepted {
		t.Fatalf("delete = %d %s", r.status, r.body)
	}
	decode(t, as(http.MethodGet, "/api/v1/admin/movies?status=deleting").body, &movies)
	if movies.Total != 1 || movies.Items[0].ID != failedID {
		t.Fatalf("deleting = %+v", movies)
	}
	for _, tt := range []struct {
		method, path string
		status       int
	}{
		{http.MethodDelete, "/api/v1/admin/movies/" + model.NewID(), http.StatusNotFound},
		{http.MethodPost, "/api/v1/admin/movies/" + model.NewID() + "/retry", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/admin/movies/..%2F..%2Fetc", http.StatusBadRequest},
		{http.MethodPost, "/api/v1/admin/movies/x/retry", http.StatusBadRequest},
	} {
		if r := as(tt.method, tt.path); r.status != tt.status {
			t.Errorf("%s %s = %d, want %d", tt.method, tt.path, r.status, tt.status)
		}
	}
}

// Retranscode: only a ready movie, never beside an active job; the movie
// stays ready.
func TestAdminRetranscode(t *testing.T) {
	f := fixtures(t)
	st := newFakeAdminStore(t)
	srv := newAdminServer(t, st)
	as := func(id string) response {
		t.Helper()
		return call(t, srv, http.MethodPost, "/api/v1/admin/movies/"+id+"/retranscode", f.adminToken, "")
	}
	if r := as(readyID); r.status != http.StatusAccepted || len(r.body) != 0 {
		t.Fatalf("retranscode = %d %s", r.status, r.body)
	}
	var movies adminMovieListJSON
	decode(t, call(t, srv, http.MethodGet, "/api/v1/admin/movies", f.adminToken, "").body, &movies)
	for _, m := range movies.Items {
		if m.ID == readyID && (m.Status != "ready" || m.ActiveJob == nil || *m.ActiveJobPurpose != "retranscode") {
			t.Fatalf("after the request: %+v; want ready with a queued retranscode", m)
		}
	}
	for _, tt := range []struct {
		name, id string
		status   int
		code     string
	}{
		{"a job already queued", readyID, http.StatusConflict, codeJobActive},
		{"not ready", failedID, http.StatusConflict, codeNotReady},
		{"processing", busyID, http.StatusConflict, codeNotReady},
		{"unknown", model.NewID(), http.StatusNotFound, codeNotFound},
		{"not a uuid", "..%2Fx", http.StatusBadRequest, codeInvalidRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if r := as(tt.id); r.status != tt.status || errorCode(t, r) != tt.code {
				t.Fatalf("= %d %s, want %d %s", r.status, r.body, tt.status, tt.code)
			}
		})
	}
}

// A disabled user: login refused after the right password (403), refresh
// refused, verify-email refused (the token kept), and forgot-password sends
// nothing. (Google sign-in: the store refuses; tested in cmd/api.)
func TestDisabledUser(t *testing.T) {
	e := newRegEnv(t, regSetup{})
	password := randomString(18)
	laptop := e.verified(t, "dana@example.com", "dana", password)
	if r := e.register(t, "una@example.com", "una", randomString(18), "good"); r.status != http.StatusAccepted {
		t.Fatal(r.status)
	}
	unaToken := tokenFrom(t, e.mailer.take())

	disable := func(name string, on bool) {
		e.as.mu.Lock()
		defer e.as.mu.Unlock()
		u := e.as.users[name]
		u.DisabledAt = nil
		if on {
			now := time.Now()
			u.DisabledAt = &now
		}
		e.as.users[name] = u
	}
	disable("dana", true)
	disable("una", true)

	if r := e.login(t, "dana", "wrong-"+password, ""); r.status != http.StatusUnauthorized || errorCode(t, r) != codeInvalidCredentials {
		t.Fatalf("wrong password = %d %s; a disabled account must not show before the password checks out", r.status, r.body)
	}
	if r := e.login(t, "dana", password, ""); r.status != http.StatusForbidden || errorCode(t, r) != codeAccountDisabled {
		t.Fatalf("login = %d %s", r.status, r.body)
	}
	if laptop.refreshes(t, e.srv) {
		t.Fatal("a disabled user refreshed")
	}
	if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": unaToken}); r.status != http.StatusForbidden ||
		errorCode(t, r) != codeAccountDisabled {
		t.Fatalf("verify = %d %s", r.status, r.body)
	}
	if r := e.forgot(t, "dana@example.com", "good"); r.status != http.StatusAccepted {
		t.Fatalf("forgot = %d", r.status)
	}
	if r := e.post(t, "/api/v1/auth/resend-verification", map[string]string{"email": "una@example.com", "turnstile_token": "good"}); r.status != http.StatusAccepted {
		t.Fatalf("resend = %d", r.status)
	}
	if m := e.mailer.take(); len(m) != 0 {
		t.Fatalf("mailed a disabled account: %+v", m)
	}

	disable("dana", false)
	disable("una", false)
	if r := e.login(t, "dana", password, ""); r.status != http.StatusOK {
		t.Fatalf("login after enable = %d %s", r.status, r.body)
	}
	if r := e.post(t, "/api/v1/auth/verify-email", map[string]string{"token": unaToken}); r.status != http.StatusOK {
		t.Fatalf("verify after enable = %d %s", r.status, r.body)
	}
}
