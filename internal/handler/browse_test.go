package handler

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
)

func TestPopularMovies(t *testing.T) {
	st := catalogStore()
	st.popular = []model.Popular{{Movie: st.movies[0], Viewers: 3}}
	srv := newServerWith(t, &fakePinger{}, st)
	for _, tt := range []struct {
		name, query string
		status      int
		window      string
		back        time.Duration // 0: ever
		limit       int
	}{
		{"default: a week, 10", "", 200, "week", 7 * 24 * time.Hour, 10},
		{"month", "?window=month&limit=5", 200, "month", 30 * 24 * time.Hour, 5},
		{"all", "?window=all&limit=20", 200, "all", 0, 20},
		{"unknown window", "?window=year", 400, "", 0, 0},
		{"limit above 20", "?limit=21", 400, "", 0, 0},
		{"limit zero", "?limit=0", 400, "", 0, 0},
		{"repeated window", "?window=week&window=all", 400, "", 0, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st.gotSince = nil
			r := doFull(t, srv, http.MethodGet, "/api/v1/movies/popular"+tt.query)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.status != 200 {
				assertErrorCode(t, r.body, codeInvalidRequest)
				if st.gotSince != nil {
					t.Fatal("store asked for an invalid request")
				}
				return
			}
			since := *st.gotSince
			switch {
			case tt.back == 0 && since != nil:
				t.Fatalf("since = %v, want ever (nil)", since)
			case tt.back != 0 && (since == nil || time.Since(*since)-tt.back > time.Minute || time.Since(*since) < tt.back):
				t.Fatalf("since = %v, want about now - %v", since, tt.back)
			}
			if st.gotLimit != tt.limit {
				t.Fatalf("limit = %d, want %d", st.gotLimit, tt.limit)
			}
			var out popularListJSON
			decode(t, r.body, &out)
			if out.Window != tt.window || len(out.Items) != 1 || out.Items[0].Viewers != 3 || out.Items[0].BackdropURL == nil {
				t.Fatalf("body = %s", r.body)
			}
		})
	}
	if r := doAs(t, srv, http.MethodGet, "/api/v1/movies/popular", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("no token = %d", r.status)
	}
	st.err = errors.New("db down")
	if r := doFull(t, srv, http.MethodGet, "/api/v1/movies/popular"); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
}

// The spotlight: featured movies when there are any, else the newest ready
// movies (and featured: false).
func TestFeaturedMovies(t *testing.T) {
	st := catalogStore()
	srv := newServerWith(t, &fakePinger{}, st)
	var out featuredListJSON

	r := doAs(t, srv, http.MethodGet, "/api/v1/movies/featured", fixtures(t).viewerToken)
	if r.status != http.StatusOK {
		t.Fatalf("status = %d: %s", r.status, r.body)
	}
	decode(t, r.body, &out)
	if out.Featured || st.gotFilter == nil || st.gotFilter.Status == nil || *st.gotFilter.Status != model.StatusReady ||
		st.gotFilter.Limit != 5 || st.gotFilter.Sort != "added_desc" {
		t.Fatalf("fallback: featured %v, filter %+v", out.Featured, st.gotFilter)
	}

	st.gotFilter = nil
	st.featured = []model.Movie{st.movies[0]}
	r = doAs(t, srv, http.MethodGet, "/api/v1/movies/featured", fixtures(t).viewerToken)
	decode(t, r.body, &out)
	if !out.Featured || len(out.Items) != 1 || out.Items[0].ID != readyID || st.gotFilter != nil {
		t.Fatalf("featured: %s (list asked: %v)", r.body, st.gotFilter != nil)
	}
	if out.Items[0].TopHeight != nil {
		t.Fatalf("top_height = %d, want null (the fake has none)", *out.Items[0].TopHeight)
	}

	if r := doAs(t, srv, http.MethodGet, "/api/v1/movies/featured", ""); r.status != http.StatusUnauthorized {
		t.Fatalf("no token = %d", r.status)
	}
	st.err = errors.New("db down")
	if r := doFull(t, srv, http.MethodGet, "/api/v1/movies/featured"); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
}

func TestAdminEditMovie(t *testing.T) {
	f := fixtures(t)
	st := newFakeAdminStore(t)
	srv := newAdminServer(t, st)
	edit := func(id, body string) response {
		t.Helper()
		return call(t, srv, http.MethodPatch, "/api/v1/admin/movies/"+id, f.adminToken, body)
	}

	r := edit(readyID, `{"title":"  The Long Night  ","year":2004,"overview":"Two lines.\nOf text."}`)
	if r.status != http.StatusOK {
		t.Fatalf("edit = %d %s", r.status, r.body)
	}
	var m adminMovieJSON
	decode(t, r.body, &m)
	if m.Title != "The Long Night" || m.Year == nil || *m.Year != 2004 || m.Overview == nil || *m.Overview != "Two lines.\nOf text." {
		t.Fatalf("after edit: %+v", m)
	}
	// A partial edit keeps the rest; null and "" clear.
	decode(t, edit(readyID, `{"year":null}`).body, &m)
	if m.Year != nil || m.Title != "The Long Night" || m.Overview == nil {
		t.Fatalf("after clearing the year: %+v", m)
	}
	// A published movie keeps its overview: unpublish it first.
	if r := edit(readyID, `{"overview":"   "}`); r.status != http.StatusConflict || errorCode(t, r) != codeMoviePublished {
		t.Fatalf("clearing a published movie's overview = %d %s", r.status, r.body)
	}
	decode(t, edit(unpublishedID, `{"overview":"Draft text"}`).body, &m)
	decode(t, edit(unpublishedID, `{"overview":"   "}`).body, &m)
	if m.Overview != nil {
		t.Fatalf("after a blank overview: %+v", m)
	}

	long := strings.Repeat("é", 201)
	for _, tt := range []struct{ name, id, body string }{
		{"empty body object", readyID, `{}`},
		{"null title", readyID, `{"title":null}`},
		{"blank title", readyID, `{"title":"   "}`},
		{"title too long", readyID, `{"title":"` + long + `"}`},
		{"control character in title", readyID, `{"title":"a\u0007b"}`},
		{"NUL in overview", readyID, `{"overview":"a\u0000b"}`},
		{"year too early", readyID, `{"year":1879}`},
		{"year too late", readyID, `{"year":2101}`},
		{"year not an integer", readyID, `{"year":"2004"}`},
		{"overview too long", readyID, `{"overview":"` + strings.Repeat("x", 2001) + `"}`},
		{"unknown field", readyID, `{"title":"x","status":"ready"}`},
		{"not a uuid", "..%2Fx", `{"title":"x"}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if r := edit(tt.id, tt.body); r.status != http.StatusBadRequest || errorCode(t, r) != codeInvalidRequest {
				t.Fatalf("= %d %s, want 400 invalid_request", r.status, r.body)
			}
		})
	}
	// Exactly the limits pass.
	if r := edit(readyID, `{"title":"`+strings.Repeat("é", 200)+`","overview":"`+strings.Repeat("x", 2000)+`","year":1880}`); r.status != http.StatusOK {
		t.Fatalf("at the limits = %d %s", r.status, r.body)
	}
	if r := edit(model.NewID(), `{"title":"x"}`); r.status != http.StatusNotFound || errorCode(t, r) != codeNotFound {
		t.Fatalf("unknown movie = %d %s", r.status, r.body)
	}
	// A user is refused before validation can tell them anything.
	if r := call(t, srv, http.MethodPatch, "/api/v1/admin/movies/"+readyID, f.viewerToken, `{"title":""}`); r.status != http.StatusForbidden {
		t.Fatalf("user, invalid body = %d", r.status)
	}
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPatch, srv.URL+"/api/v1/admin/movies/"+readyID, strings.NewReader(`{"title":"x"}`))
	req.Header.Set("Authorization", "Bearer "+f.adminToken)
	req.Header.Set("Content-Type", "text/plain")
	if r := send(t, srv, req); r.status != http.StatusUnsupportedMediaType {
		t.Fatalf("text/plain = %d", r.status)
	}
}

func TestAdminFeature(t *testing.T) {
	f := fixtures(t)
	st := newFakeAdminStore(t)
	srv := newAdminServer(t, st)
	as := func(method, id string) response {
		t.Helper()
		return call(t, srv, method, "/api/v1/admin/movies/"+id+"/feature", f.adminToken, "")
	}
	if r := as(http.MethodPost, readyID); r.status != http.StatusNoContent || len(r.body) != 0 {
		t.Fatalf("feature = %d %s", r.status, r.body)
	}
	var movies adminMovieListJSON
	decode(t, call(t, srv, http.MethodGet, "/api/v1/admin/movies", f.adminToken, "").body, &movies)
	for _, m := range movies.Items {
		if (m.ID == readyID) != (m.FeaturedAt != nil) {
			t.Fatalf("%s featured_at = %v", m.Title, m.FeaturedAt)
		}
	}
	if r := as(http.MethodDelete, readyID); r.status != http.StatusNoContent {
		t.Fatalf("unfeature = %d %s", r.status, r.body)
	}
	if st.movies[readyID].FeaturedAt != nil {
		t.Fatal("still featured")
	}
	// Unfeaturing works in any status.
	if r := as(http.MethodDelete, failedID); r.status != http.StatusNoContent {
		t.Fatalf("unfeature failed movie = %d", r.status)
	}
	for _, tt := range []struct {
		name, method, id string
		status           int
		code             string
	}{
		{"feature a failed movie", http.MethodPost, failedID, http.StatusConflict, codeNotReady},
		{"feature a processing movie", http.MethodPost, busyID, http.StatusConflict, codeNotReady},
		{"feature unknown", http.MethodPost, model.NewID(), http.StatusNotFound, codeNotFound},
		{"unfeature unknown", http.MethodDelete, model.NewID(), http.StatusNotFound, codeNotFound},
		{"feature not a uuid", http.MethodPost, "..%2Fx", http.StatusBadRequest, codeInvalidRequest},
		{"unfeature not a uuid", http.MethodDelete, "..%2Fx", http.StatusBadRequest, codeInvalidRequest},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if r := as(tt.method, tt.id); r.status != tt.status || errorCode(t, r) != tt.code {
				t.Fatalf("= %d %s, want %d %s", r.status, r.body, tt.status, tt.code)
			}
		})
	}
}
