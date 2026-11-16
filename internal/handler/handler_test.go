package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// fakePinger returns err and records whether the context had a deadline.
type fakePinger struct {
	err         error
	gotDeadline time.Duration
}

func (f *fakePinger) Ping(ctx context.Context) error {
	if d, ok := ctx.Deadline(); ok {
		f.gotDeadline = time.Until(d)
	}
	return f.err
}

func newServer(t *testing.T, db Pinger) *httptest.Server {
	t.Helper()
	return newServerWith(t, db, &fakeStore{})
}

// newServerWith wires the real service.Catalog and service.Auth over
// in-memory stores, so handler tests exercise the services' rules too,
// without Postgres.
func newServerWith(t *testing.T, db Pinger, st *fakeStore) *httptest.Server {
	t.Helper()
	srv, _ := newServerFull(t, db, st, newFakeAuthStore(t), Options{SecureCookies: true})
	return srv
}

func newServerFull(t *testing.T, db Pinger, st *fakeStore, as *fakeAuthStore, opts Options) (*httptest.Server, *fakeAuthStore) {
	t.Helper()
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	a := service.NewAuth(as, fixtures(t).hasher, fixtures(t).signer, auth.NewThrottle(), auth.NewThrottle(), slog.New(slog.DiscardHandler))
	signup := service.NewRegistration(newFakeRegStore(as), fixtures(t).hasher, &fakeCaptcha{}, &fakeMailer{}, a,
		service.RegistrationConfig{Open: true, BaseURL: testBaseURL, TurnstileSiteKey: testSiteKey}, slog.New(slog.DiscardHandler))
	srv := httptest.NewServer(NewRouter(slog.New(slog.DiscardHandler), reg, db, service.NewCatalog(st, st, fixtures(t).media), a, signup, opts))
	t.Cleanup(srv.Close)
	return srv, as
}

// mediaFile checks that u is a media URL signed with the test key and still
// valid, and returns the part the token protects plus the file:
// "{movie_id}/{path}".
func mediaFile(t *testing.T, u string) string {
	t.Helper()
	parts := strings.SplitN(strings.TrimPrefix(u, service.MediaPrefix), "/", 4)
	if !strings.HasPrefix(u, service.MediaPrefix) || len(parts) != 4 {
		t.Fatalf("%q is not /media/{token}/{expires}/{id}/{file}", u)
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || exp <= time.Now().Unix() {
		t.Fatalf("%q: expires %q is not in the future", u, parts[1])
	}
	if want := fixtures(t).media.Token(parts[2], exp); parts[0] != want {
		t.Fatalf("%q: token %q, want %q", u, parts[0], want)
	}
	return parts[2] + "/" + parts[3]
}

func do(t *testing.T, srv *httptest.Server, method, path string) (int, string) {
	t.Helper()
	r := doFull(t, srv, method, path)
	return r.status, string(r.body)
}

type response struct {
	status int
	header http.Header
	body   []byte
}

// doFull sends a request as the admin; every /api/ response is checked
// against the spec.
func doFull(t *testing.T, srv *httptest.Server, method, path string) response {
	t.Helper()
	return doAs(t, srv, method, path, fixtures(t).adminToken)
}

// doAs sends a request with token as its bearer token ("" for none).
func doAs(t *testing.T, srv *httptest.Server, method, path, token string) response {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, srv.URL+path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return send(t, srv, req)
}

// send sends req as it is; the response is checked against the spec.
func send(t *testing.T, srv *httptest.Server, req *http.Request) response {
	t.Helper()
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	checkSpec(t, req, resp, body)
	return response{status: resp.StatusCode, header: resp.Header, body: body}
}

func TestRouter(t *testing.T) {
	tests := []struct {
		name         string
		pingErr      error
		method       string
		path         string
		wantStatus   int
		wantContains string
	}{
		{"healthz ok", nil, http.MethodGet, "/healthz", http.StatusOK, "ok"},
		{"healthz ignores db failure", errors.New("down"), http.MethodGet, "/healthz", http.StatusOK, "ok"},
		{"readyz ok", nil, http.MethodGet, "/readyz", http.StatusOK, `"status":"ready"`},
		{"readyz db down", errors.New("down"), http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"postgres":"unreachable"`},
		{"metrics exposes go runtime", nil, http.MethodGet, "/metrics", http.StatusOK, "go_goroutines"},
		{"healthz rejects POST", nil, http.MethodPost, "/healthz", http.StatusMethodNotAllowed, ""},
		{"unknown path 404", nil, http.MethodGet, "/nope", http.StatusNotFound, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := newServer(t, &fakePinger{err: tt.pingErr})
			status, body := do(t, srv, tt.method, tt.path)
			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if tt.wantContains != "" && !strings.Contains(body, tt.wantContains) {
				t.Errorf("body %q does not contain %q", body, tt.wantContains)
			}
		})
	}
}

func TestReadyzBody(t *testing.T) {
	tests := []struct {
		name       string
		pingErr    error
		wantStatus string
	}{
		{"ready", nil, "ready"},
		{"unavailable hides error detail", errors.New("dial tcp 10.0.0.9:5432: secret detail"), "unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := &fakePinger{err: tt.pingErr}
			_, body := do(t, newServer(t, p), http.MethodGet, "/readyz")

			var got readyResponse
			if err := json.Unmarshal([]byte(body), &got); err != nil {
				t.Fatalf("body is not JSON: %v (%q)", err, body)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("status = %q, want %q", got.Status, tt.wantStatus)
			}
			if strings.Contains(body, "secret detail") {
				t.Error("readyz leaked the underlying error to the client")
			}
			if p.gotDeadline <= 0 || p.gotDeadline > ReadyTimeout {
				t.Errorf("ping deadline = %v, want within (0, %v]", p.gotDeadline, ReadyTimeout)
			}
		})
	}
}

// The dispatcher serves only the probes, with the api's exact semantics.
func TestProbeRouter(t *testing.T) {
	for _, tt := range []struct {
		name    string
		pingErr error
		path    string
		want    int
	}{
		{"healthz", nil, "/healthz", http.StatusOK},
		{"healthz ignores the database", errors.New("down"), "/healthz", http.StatusOK},
		{"readyz", nil, "/readyz", http.StatusOK},
		{"readyz with the database down", errors.New("down"), "/readyz", http.StatusServiceUnavailable},
		{"metrics", nil, "/metrics", http.StatusOK},
		{"nothing else", nil, "/movies", http.StatusNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(NewProbeRouter(slog.New(slog.DiscardHandler), prometheus.NewRegistry(), &fakePinger{err: tt.pingErr}))
			t.Cleanup(srv.Close)
			if code, body := do(t, srv, http.MethodGet, tt.path); code != tt.want {
				t.Fatalf("GET %s = %d, want %d: %s", tt.path, code, tt.want, body)
			}
		})
	}
}
