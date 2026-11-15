package handler

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// postProgress sends body to POST /api/v1/progress as token.
func postProgress(t *testing.T, srv *httptest.Server, token, contentType, body string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, srv.URL+"/api/v1/progress", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return send(t, srv, req)
}

func TestSaveProgress(t *testing.T) {
	f := fixtures(t)
	const ok = "application/json"
	body := func(id, pos string) string { return `{"movie_id":"` + id + `","position_sec":` + pos + `}` }
	tests := []struct {
		name        string
		token       string
		contentType string
		body        string
		status      int
		code        string
	}{
		{"ready movie", f.viewerToken, ok, body(readyID, "30"), 204, ""},
		{"charset is fine", f.viewerToken, "application/json; charset=utf-8", body(readyID, "31"), 204, ""},
		{"uppercase id", f.viewerToken, ok, body(strings.ToUpper(readyID), "32"), 204, ""},
		{"start of the movie", f.viewerToken, ok, body(readyID, "0"), 204, ""},
		{"end of the movie", f.viewerToken, ok, body(readyID, "8160"), 204, ""},
		{"past the runtime", f.viewerToken, ok, body(readyID, "8161"), 400, "invalid_request"},
		{"negative", f.viewerToken, ok, body(readyID, "-1"), 400, "invalid_request"},
		{"fractional seconds", f.viewerToken, ok, body(readyID, "30.5"), 400, "invalid_request"},
		{"position as a string", f.viewerToken, ok, body(readyID, `"30"`), 400, "invalid_request"},
		{"no position", f.viewerToken, ok, `{"movie_id":"` + readyID + `"}`, 400, "invalid_request"},
		{"not a uuid", f.viewerToken, ok, body("../etc", "1"), 400, "invalid_request"},
		{"not JSON", f.viewerToken, ok, `movie_id=x`, 400, "invalid_request"},
		// The user is the token's subject: a user field in the body is refused, not used.
		{"user in the body", f.viewerToken, ok, `{"user_id":"` + adminID + `","movie_id":"` + readyID + `","position_sec":1}`, 400, "invalid_request"},
		{"body too large", f.viewerToken, ok, `{"movie_id":"` + strings.Repeat(" ", 2<<10) + `"}`, 400, "invalid_request"},
		{"no content type", f.viewerToken, "", body(readyID, "1"), 415, "unsupported_media_type"},
		{"form encoded", f.viewerToken, "application/x-www-form-urlencoded", body(readyID, "1"), 415, "unsupported_media_type"},
		{"text/plain (a cross-site form)", f.viewerToken, "text/plain", body(readyID, "1"), 415, "unsupported_media_type"},
		{"pending, as a user", f.viewerToken, ok, body(pendingID, "1"), 404, "not_found"},
		{"pending, as an admin", f.adminToken, ok, body(pendingID, "1"), 409, "not_ready"},
		{"unknown movie", f.viewerToken, ok, body(unknownID, "1"), 404, "not_found"},
		{"no token", "", ok, body(readyID, "1"), 401, "unauthorized"},
		{"bad token", "x.y.z", ok, body(readyID, "1"), 401, "unauthorized"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServerWith(t, &fakePinger{}, catalogStore())
			r := postProgress(t, srv, tt.token, tt.contentType, tt.body)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.code != "" {
				assertErrorCode(t, r.body, tt.code)
			}
		})
	}
}

// Progress belongs to the token's user: saved by one, never visible to or
// changed by another, and it feeds playback's resume_sec.
func TestProgressIsPerUser(t *testing.T) {
	f := fixtures(t)
	st := catalogStore()
	srv := newServerWith(t, &fakePinger{}, st)

	cw := func(token string) continueWatchingJSON {
		t.Helper()
		r := doAs(t, srv, http.MethodGet, "/api/v1/continue-watching", token)
		if r.status != http.StatusOK {
			t.Fatalf("continue-watching = %d: %s", r.status, r.body)
		}
		if r.header.Get("Cache-Control") != "no-store" {
			t.Errorf("Cache-Control = %q", r.header.Get("Cache-Control"))
		}
		var out continueWatchingJSON
		decode(t, r.body, &out)
		return out
	}
	resume := func(token string) *int {
		t.Helper()
		r := doAs(t, srv, http.MethodGet, "/api/v1/movies/"+readyID+"/playback", token)
		if r.status != http.StatusOK {
			t.Fatalf("playback = %d: %s", r.status, r.body)
		}
		var p playbackJSON
		decode(t, r.body, &p)
		return p.ResumeSec
	}

	if got := cw(f.viewerToken); len(got.Items) != 0 {
		t.Fatalf("empty continue-watching = %+v", got)
	}
	if got := resume(f.viewerToken); got != nil {
		t.Fatalf("resume with no progress = %d", *got)
	}
	if r := postProgress(t, srv, f.viewerToken, "application/json", `{"movie_id":"`+readyID+`","position_sec":30}`); r.status != 204 {
		t.Fatalf("save = %d: %s", r.status, r.body)
	}

	got := cw(f.viewerToken)
	if len(got.Items) != 1 || got.Items[0].Movie.ID != readyID || got.Items[0].PositionSec != 30 ||
		got.Items[0].RuntimeSec == nil || *got.Items[0].RuntimeSec != 8160 {
		t.Fatalf("continue-watching = %+v", got)
	}
	if p := resume(f.viewerToken); p == nil || *p != 30 {
		t.Fatalf("resume = %v, want 30", p)
	}
	// The admin is another user: none of the viewer's progress shows up.
	if other := cw(f.adminToken); len(other.Items) != 0 {
		t.Fatalf("another user's continue-watching = %+v", other)
	}
	if p := resume(f.adminToken); p != nil {
		t.Fatalf("another user's resume = %d", *p)
	}
	// The admin saving the same movie does not move the viewer's position.
	if r := postProgress(t, srv, f.adminToken, "application/json", `{"movie_id":"`+readyID+`","position_sec":900}`); r.status != 204 {
		t.Fatalf("admin save = %d", r.status)
	}
	if p := resume(f.viewerToken); p == nil || *p != 30 {
		t.Fatalf("viewer's resume after the admin's save = %v", p)
	}

	// Watched to 95%: finished, out of the row, and playback starts over.
	if r := postProgress(t, srv, f.viewerToken, "application/json", `{"movie_id":"`+readyID+`","position_sec":7752}`); r.status != 204 {
		t.Fatalf("save = %d", r.status)
	}
	if got := cw(f.viewerToken); len(got.Items) != 0 {
		t.Fatalf("a finished movie stays in continue-watching: %+v", got)
	}
	if p := resume(f.viewerToken); p != nil {
		t.Fatalf("a finished movie resumes at %d", *p)
	}
}

func TestProgressErrors(t *testing.T) {
	f := fixtures(t)
	broken := catalogStore()
	broken.err = errors.New("connection refused")
	for name, tt := range map[string]struct {
		st     *fakeStore
		method string
		path   string
		token  string
		status int
		code   string
	}{
		"continue-watching without a token": {catalogStore(), http.MethodGet, "/api/v1/continue-watching", "", 401, "unauthorized"},
		"continue-watching store failure":   {broken, http.MethodGet, "/api/v1/continue-watching", f.viewerToken, 500, "internal"},
		"POST continue-watching":            {catalogStore(), http.MethodPost, "/api/v1/continue-watching", f.viewerToken, 405, "method_not_allowed"},
		"GET progress":                      {catalogStore(), http.MethodGet, "/api/v1/progress", f.viewerToken, 405, "method_not_allowed"},
		"playback resume store failure":     {broken, http.MethodGet, "/api/v1/movies/" + readyID + "/playback", f.viewerToken, 500, "internal"},
	} {
		t.Run(name, func(t *testing.T) {
			r := doAs(t, newServerWith(t, &fakePinger{}, tt.st), tt.method, tt.path, tt.token)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			assertErrorCode(t, r.body, tt.code)
		})
	}
	t.Run("save store failure", func(t *testing.T) {
		// GetMovie fails first; the detail stays in the log.
		r := postProgress(t, newServerWith(t, &fakePinger{}, broken), f.viewerToken, "application/json",
			`{"movie_id":"`+readyID+`","position_sec":1}`)
		if r.status != 500 || strings.Contains(string(r.body), "refused") {
			t.Fatalf("save = %d: %s", r.status, r.body)
		}
	})
}
