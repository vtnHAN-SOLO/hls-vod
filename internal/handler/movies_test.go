package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/poster"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// fakeStore is an in-memory service.CatalogStore.
type fakeStore struct {
	movies     []model.Movie // in list order
	renditions map[string][]model.Rendition
	total      int64
	err        error

	gotFilter *store.MovieFilter

	subtitles map[string][]model.SubtitleTrack
	popular   []model.Popular
	featured  []model.Movie
	gotSince  **time.Time // set by PopularMovies: nil inside means "ever"
	gotLimit  int

	// posters are the poster.Store side: JPEG bytes by "{id}/{width}/{hex hash}".
	posters map[string][]byte

	// progress is keyed by user id, then movie id; seq orders saves.
	mu       sync.Mutex
	progress map[string]map[string]fakeProgress
	seq      int
}

type fakeProgress struct {
	pos, seq int
}

func (f *fakeStore) Put(context.Context, string, poster.Poster) error { return nil }
func (f *fakeStore) Delete(context.Context, string) error             { return nil }

func (f *fakeStore) Get(_ context.Context, id string, width int, hash []byte) ([]byte, error) {
	if f.err != nil {
		return nil, f.err
	}
	b, ok := f.posters[fmt.Sprintf("%s/%d/%x", id, width, hash)]
	if !ok {
		return nil, poster.ErrNotFound
	}
	return b, nil
}

func (f *fakeStore) ListMovies(_ context.Context, flt store.MovieFilter) ([]model.Movie, int64, error) {
	f.gotFilter = &flt
	return f.movies, f.total, f.err
}

func (f *fakeStore) GetMovie(_ context.Context, id string) (model.Movie, error) {
	if f.err != nil {
		return model.Movie{}, f.err
	}
	for _, m := range f.movies {
		if m.ID == id {
			return m, nil
		}
	}
	return model.Movie{}, store.ErrNotFound
}

func (f *fakeStore) ListRenditions(_ context.Context, id string) ([]model.Rendition, error) {
	return f.renditions[id], f.err
}

// SaveProgress keeps the real store's guard: a ready movie, within its runtime.
func (f *fakeStore) SaveProgress(ctx context.Context, userID, movieID string, pos int) error {
	m, err := f.GetMovie(ctx, movieID)
	if errors.Is(err, store.ErrNotFound) || (err == nil && (m.Status != model.StatusReady || (m.RuntimeSec != nil && pos > *m.RuntimeSec))) {
		return store.ErrProgressRefused
	}
	if err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.progress == nil {
		f.progress = map[string]map[string]fakeProgress{}
	}
	if f.progress[userID] == nil {
		f.progress[userID] = map[string]fakeProgress{}
	}
	f.seq++
	f.progress[userID][movieID] = fakeProgress{pos, f.seq}
	return nil
}

func (f *fakeStore) GetProgress(_ context.Context, userID, movieID string) (int, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.progress[userID][movieID]
	return p.pos, ok, nil
}

func (f *fakeStore) ContinueWatching(_ context.Context, userID string, finishedPercent, limit int) ([]model.Progress, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []model.Progress
	for _, m := range f.movies {
		p, ok := f.progress[userID][m.ID]
		if !ok || m.Status != model.StatusReady ||
			(m.RuntimeSec != nil && p.pos*100 >= *m.RuntimeSec*finishedPercent) {
			continue
		}
		out = append(out, model.Progress{Movie: m, PositionSec: p.pos, UpdatedAt: m.UpdatedAt.Add(time.Duration(p.seq) * time.Second)})
	}
	slices.SortFunc(out, func(a, b model.Progress) int { return b.UpdatedAt.Compare(a.UpdatedAt) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (f *fakeStore) ListSubtitles(_ context.Context, id string) ([]model.SubtitleTrack, error) {
	return f.subtitles[id], f.err
}

func (f *fakeStore) PopularMovies(_ context.Context, since *time.Time, limit int) ([]model.Popular, error) {
	f.gotSince, f.gotLimit = &since, limit
	return f.popular, f.err
}

func (f *fakeStore) FeaturedMovies(_ context.Context, limit int) ([]model.Movie, error) {
	if f.err != nil {
		return nil, f.err
	}
	if len(f.featured) > limit {
		return f.featured[:limit], nil
	}
	return f.featured, nil
}

const (
	readyID      = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"
	pendingID    = "1c2d3e4f-5a6b-4c7d-8e9f-0a1b2c3d4e5f"
	processingID = "2d3e4f5a-6b7c-4d8e-9f0a-1b2c3d4e5f6a"
	failedID     = "3e4f5a6b-7c8d-4e9f-8a1b-2c3d4e5f6a7b"
	unknownID    = "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"
	badPosterID  = "4f5a6b7c-8d9e-4f0a-9b2c-3d4e5f6a7b8c"
	// readyVersion is the version folder the ready movie plays from.
	readyVersion = "5a6b7c8d-9e0f-4a1b-8c2d-3e4f5a6b7c8d"
)

func ptr[T any](v T) *T { return &v }

// readyPosterHash is the ready movie's poster hash.
var readyPosterHash = sha256.Sum256([]byte("the ready movie's poster"))

// unpublishedID is a ready movie that is not published.
const unpublishedID = "5d1c3e0a-8f7b-4c2d-9a6e-1b2c3d4e5f60"

func catalogStore() *fakeStore {
	// A non-UTC zone, as pgx returns in the server's local time.
	at := time.Date(2026, 11, 1, 19, 0, 0, 0, time.FixedZone("ICT", 7*3600))
	movie := func(id string, st model.MovieStatus) model.Movie {
		return model.Movie{ID: id, Title: "Movie " + id[:4], Status: st, CreatedAt: at, UpdatedAt: at,
			SourcePath: "/mnt/media/inbox/secret-source-" + id[:4] + ".mkv"}
	}
	ready := movie(readyID, model.StatusReady)
	ready.Year, ready.TMDBID, ready.RuntimeSec = ptr(1999), ptr(603), ptr(8160)
	ready.Overview, ready.BackdropPath = ptr("A hacker learns the truth."), ptr(readyVersion+"/poster.jpg")
	ready.LibraryPath = ptr("library/" + readyID + "/" + readyVersion)
	ready.PublishedAt, ready.PosterHash, ready.PosterWidths = &at, readyPosterHash[:], []int{300, 600}
	bad := movie(badPosterID, model.StatusReady)
	bad.BackdropPath = ptr("../../etc/passwd")
	bad.PublishedAt = &at
	// Ready, with a poster and an overview, but not published: viewers
	// never see it.
	hidden := movie(unpublishedID, model.StatusReady)
	hidden.Overview, hidden.PosterHash, hidden.PosterWidths = ptr("Not out yet."), readyPosterHash[:], []int{300}
	hidden.LibraryPath = ptr("library/" + unpublishedID + "/" + readyVersion)

	return &fakeStore{
		movies: []model.Movie{ready, movie(pendingID, model.StatusPending),
			movie(processingID, model.StatusProcessing), movie(failedID, model.StatusFailed), bad, hidden},
		total: 6,
		posters: map[string][]byte{
			fmt.Sprintf("%s/300/%x", readyID, readyPosterHash): []byte("\xff\xd8\xff a 300 wide jpeg"),
			fmt.Sprintf("%s/600/%x", readyID, readyPosterHash): []byte("\xff\xd8\xff a 600 wide jpeg"),
		},
		subtitles: map[string][]model.SubtitleTrack{readyID: {
			{Path: readyVersion + "/subs/0_eng.vtt", Language: "eng", Label: ptr("SDH"), Position: 0},
			{Path: readyVersion + "/subs/1_vie.vtt", Language: "vie", Position: 1},
			{Path: readyVersion + "/subs/2_eng.vtt", Language: "eng", Position: 2},
			// Never a URL: a stored path that escapes the movie's folder is left out.
			{Path: "../other/subs/3_fre.vtt", Language: "fre", Position: 3},
		}},
		renditions: map[string][]model.Rendition{readyID: {
			{Height: 1080, BitrateKbps: ptr(5000), SizeBytes: ptr(int64(4_000_000_000)), PlaylistPath: "1080p/index.m3u8"},
			{Height: 480, PlaylistPath: "480p/index.m3u8"},
		}},
	}
}

func TestListMovies(t *testing.T) {
	tests := []struct {
		name      string
		query     string
		status    int
		wantLimit int64
		wantOff   int64
		wantState string // "" = no status filter
		wantCode  string
	}{
		{"defaults", "", 200, 20, 0, "", ""},
		{"filter and page", "?status=ready&limit=5&offset=10", 200, 5, 10, "ready", ""},
		{"max limit", "?limit=100", 200, 100, 0, "", ""},
		{"empty status means all", "?status=", 200, 20, 0, "", ""},
		{"limit above max rejected", "?limit=101", 400, 0, 0, "", "invalid_request"},
		{"limit zero rejected", "?limit=0", 400, 0, 0, "", "invalid_request"},
		{"negative limit rejected", "?limit=-1", 400, 0, 0, "", "invalid_request"},
		{"non-integer limit", "?limit=ten", 400, 0, 0, "", "invalid_request"},
		{"repeated limit", "?limit=1&limit=2", 400, 0, 0, "", "invalid_request"},
		{"negative offset", "?offset=-1", 400, 0, 0, "", "invalid_request"},
		{"overflowing offset", "?offset=99999999999999999999", 400, 0, 0, "", "invalid_request"},
		{"unknown status", "?status=deleted", 400, 0, 0, "", "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := catalogStore()
			r := doFull(t, newServerWith(t, &fakePinger{}, st), http.MethodGet, "/api/v1/movies"+tt.query)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.wantCode != "" {
				assertErrorCode(t, r.body, tt.wantCode)
				if st.gotFilter != nil {
					t.Fatal("store was queried for an invalid request")
				}
				return
			}
			f := st.gotFilter
			if f.Limit != tt.wantLimit || f.Offset != tt.wantOff {
				t.Errorf("filter limit/offset = %d/%d, want %d/%d", f.Limit, f.Offset, tt.wantLimit, tt.wantOff)
			}
			if got := ""; f.Status != nil {
				got = string(*f.Status)
				if got != tt.wantState {
					t.Errorf("filter status = %q, want %q", got, tt.wantState)
				}
			} else if tt.wantState != "" {
				t.Errorf("filter status = nil, want %q", tt.wantState)
			}

			var page movieListJSON
			decode(t, r.body, &page)
			if page.Total != 6 || page.Limit != tt.wantLimit || page.Offset != tt.wantOff || len(page.Items) != 6 {
				t.Errorf("page = total %d limit %d offset %d items %d", page.Total, page.Limit, page.Offset, len(page.Items))
			}
		})
	}
}

func TestListMoviesBody(t *testing.T) {
	r := doFull(t, newServerWith(t, &fakePinger{}, catalogStore()), http.MethodGet, "/api/v1/movies")
	body := string(r.body)
	for _, leak := range []string{"secret-source", "/mnt/media", "source_path", "library_path"} {
		if strings.Contains(body, leak) {
			t.Errorf("response leaks internal path data %q", leak)
		}
	}
	var page movieListJSON
	decode(t, r.body, &page)
	ready, bad := page.Items[0], page.Items[4]
	if ready.BackdropURL == nil || mediaFile(t, *ready.BackdropURL) != readyID+"/"+readyVersion+"/poster.jpg" {
		t.Errorf("backdrop_url = %v, want a signed /media/…/{id}/{version}/poster.jpg", ready.BackdropURL)
	}
	if r.header.Get("Cache-Control") != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store: the body carries signed URLs", r.header.Get("Cache-Control"))
	}
	if bad.BackdropURL != nil {
		t.Errorf("backdrop_path climbing out of the movie dir became %q, want null", *bad.BackdropURL)
	}
	if !strings.Contains(body, `"created_at":"2026-11-01T12:00:00Z"`) {
		t.Errorf("timestamps should be RFC 3339 UTC: %s", body)
	}
	// Nullable fields are present as null, not omitted.
	if !strings.Contains(body, `"year":null`) {
		t.Errorf("null year was omitted: %s", body)
	}

	// No movies: items is [], never null.
	r = doFull(t, newServerWith(t, &fakePinger{}, &fakeStore{}), http.MethodGet, "/api/v1/movies?offset=500")
	if !strings.Contains(string(r.body), `"items":[]`) {
		t.Errorf("empty page body = %s, want items []", r.body)
	}
}

func TestGetMovie(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		status   int
		wantCode string
	}{
		{"found", readyID, 200, ""},
		{"uppercase id accepted", strings.ToUpper(readyID), 200, ""},
		{"no renditions yet", pendingID, 200, ""},
		{"unknown id", unknownID, 404, "not_found"},
		{"not a uuid", "not-a-uuid", 400, "invalid_request"},
		{"traversal attempt", "..%2F..%2Fetc", 400, "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := doFull(t, newServerWith(t, &fakePinger{}, catalogStore()), http.MethodGet, "/api/v1/movies/"+tt.id)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.wantCode != "" {
				assertErrorCode(t, r.body, tt.wantCode)
			}
		})
	}

	r := doFull(t, newServerWith(t, &fakePinger{}, catalogStore()), http.MethodGet, "/api/v1/movies/"+readyID)
	var d movieDetailJSON
	decode(t, r.body, &d)
	if want := []string{"en", "vi"}; !slices.Equal(d.SubtitleLanguages, want) {
		t.Errorf("subtitle languages = %v, want %v (each once, in stream order, only playable tracks)", d.SubtitleLanguages, want)
	}
	if d.ID != readyID || d.Title == "" || *d.Year != 1999 || len(d.Renditions) != 2 ||
		d.Renditions[0].Height != 1080 || d.Renditions[1].BitrateKbps != nil {
		t.Fatalf("detail = %+v", d)
	}
	if strings.Contains(string(r.body), "index.m3u8") {
		t.Error("rendition playlist paths are internal; the player uses the master URL")
	}

	r = doFull(t, newServerWith(t, &fakePinger{}, catalogStore()), http.MethodGet, "/api/v1/movies/"+pendingID)
	if !strings.Contains(string(r.body), `"renditions":[]`) {
		t.Errorf("movie without renditions: body = %s, want renditions []", r.body)
	}
}

func TestPlayback(t *testing.T) {
	tests := []struct {
		name     string
		id       string
		status   int
		wantCode string
	}{
		{"ready", readyID, 200, ""},
		// The URL must carry the stored (lowercase) id, never the request string.
		{"uppercase id returns stored id", strings.ToUpper(readyID), 200, ""},
		{"pending", pendingID, 409, "not_ready"},
		{"processing", processingID, 409, "not_ready"},
		{"failed", failedID, 409, "not_ready"},
		{"unknown id", unknownID, 404, "not_found"},
		{"not a uuid", "x", 400, "invalid_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := doFull(t, newServerWith(t, &fakePinger{}, catalogStore()), http.MethodGet,
				"/api/v1/movies/"+tt.id+"/playback")
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.wantCode != "" {
				assertErrorCode(t, r.body, tt.wantCode)
				return
			}
			var p playbackJSON
			decode(t, r.body, &p)
			if p.MovieID != readyID || mediaFile(t, p.PlaylistURL) != readyID+"/"+readyVersion+"/master.m3u8" ||
				mediaFile(t, p.ThumbnailsURL) != readyID+"/"+readyVersion+"/thumbnails.vtt" {
				t.Fatalf("playback = %+v", p)
			}
			if r.header.Get("Cache-Control") != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", r.header.Get("Cache-Control"))
			}
			// Subtitles: signed like the playlist (same token, same folder),
			// BCP 47 languages, the stream title as label.
			if len(p.Subtitles) != 3 {
				t.Fatalf("subtitles = %+v, want 3 (the unsafe path left out)", p.Subtitles)
			}
			for i, want := range []struct{ file, lang string }{{"0_eng.vtt", "en"}, {"1_vie.vtt", "vi"}, {"2_eng.vtt", "en"}} {
				s := p.Subtitles[i]
				if mediaFile(t, s.URL) != readyID+"/"+readyVersion+"/subs/"+want.file || s.Language != want.lang {
					t.Errorf("subtitle %d = %+v, want %s %s", i, s, want.file, want.lang)
				}
			}
			if p.Subtitles[0].Label == nil || *p.Subtitles[0].Label != "SDH" || p.Subtitles[1].Label != nil {
				t.Errorf("labels = %v, %v", p.Subtitles[0].Label, p.Subtitles[1].Label)
			}
		})
	}
}

// Playback URLs follow the stored library_path: a movie published before
// versions plays from library/{id}/ itself; a library_path of any other
// shape is a 500, never a guess.
func TestPlaybackFollowsLibraryPath(t *testing.T) {
	for _, tt := range []struct {
		name, path string
		status     int
		want       string
	}{
		{"old layout", "library/" + readyID, http.StatusOK, readyID + "/master.m3u8"},
		{"no library_path", "", http.StatusOK, readyID + "/master.m3u8"},
		{"another movie's folder", "library/" + pendingID + "/" + readyVersion, http.StatusInternalServerError, ""},
		{"nested", "library/" + readyID + "/a/b", http.StatusInternalServerError, ""},
		{"traversal in the version", "library/" + readyID + "/..", http.StatusInternalServerError, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st := catalogStore()
			st.movies[0].LibraryPath = nil
			if tt.path != "" {
				st.movies[0].LibraryPath = ptr(tt.path)
			}
			r := doFull(t, newServerWith(t, &fakePinger{}, st), http.MethodGet, "/api/v1/movies/"+readyID+"/playback")
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.want == "" {
				return
			}
			var p playbackJSON
			decode(t, r.body, &p)
			if mediaFile(t, p.PlaylistURL) != tt.want {
				t.Fatalf("playlist = %s, want %s", p.PlaylistURL, tt.want)
			}
		})
	}
}

// Any /api/ failure uses the one JSON error shape, including the ones
// ServeMux generates itself.
func TestAPIErrorShape(t *testing.T) {
	broken := catalogStore()
	broken.err = errors.New(`pq: connection to 10.0.0.9 refused (password=hunter2)`)
	tests := []struct {
		name      string
		st        *fakeStore
		method    string
		path      string
		status    int
		wantCode  string
		wantAllow string
	}{
		{"list store failure", broken, http.MethodGet, "/api/v1/movies", 500, "internal", ""},
		{"detail store failure", broken, http.MethodGet, "/api/v1/movies/" + readyID, 500, "internal", ""},
		{"playback store failure", broken, http.MethodGet, "/api/v1/movies/" + readyID + "/playback", 500, "internal", ""},
		{"POST list", catalogStore(), http.MethodPost, "/api/v1/movies", 405, "method_not_allowed", "GET, HEAD"},
		{"DELETE movie", catalogStore(), http.MethodDelete, "/api/v1/movies/" + readyID, 405, "method_not_allowed", "GET, HEAD"},
		{"PUT playback", catalogStore(), http.MethodPut, "/api/v1/movies/" + readyID + "/playback", 405, "method_not_allowed", "GET, HEAD"},
		{"GET login", catalogStore(), http.MethodGet, "/api/v1/auth/login", 405, "method_not_allowed", "POST"},
		{"GET refresh", catalogStore(), http.MethodGet, "/api/v1/auth/refresh", 405, "method_not_allowed", "POST"},
		{"DELETE logout", catalogStore(), http.MethodDelete, "/api/v1/auth/logout", 405, "method_not_allowed", "POST"},
		{"unknown endpoint", catalogStore(), http.MethodGet, "/api/v1/nope", 404, "not_found", ""},
		{"unknown version", catalogStore(), http.MethodGet, "/api/v2/movies", 404, "not_found", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := doFull(t, newServerWith(t, &fakePinger{}, tt.st), tt.method, tt.path)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if ct := r.header.Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			assertErrorCode(t, r.body, tt.wantCode)
			if tt.status == http.StatusMethodNotAllowed && tt.wantAllow == "" {
				t.Fatal("every 405 case must assert its Allow header")
			}
			if tt.wantAllow != "" && r.header.Get("Allow") != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", r.header.Get("Allow"), tt.wantAllow)
			}
			if strings.Contains(string(r.body), "hunter2") || strings.Contains(string(r.body), "10.0.0.9") {
				t.Error("internal error detail leaked to the client")
			}
		})
	}
}

func assertErrorCode(t *testing.T, body []byte, want string) {
	t.Helper()
	var e errorBody
	decode(t, body, &e)
	if e.Error.Code != want || e.Error.Message == "" {
		t.Fatalf("error = %+v, want code %q with a message", e.Error, want)
	}
}

func decode(t *testing.T, body []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(body, v); err != nil {
		t.Fatalf("decoding %s: %v", body, err)
	}
}

// Request ids never become paths: anything that is not a UUID is a 400
// before the store is asked, on every endpoint that takes an id.
func TestIDTraversalRejected(t *testing.T) {
	ids := map[string]string{
		"../ traversal, escaped slash": "..%2F..%2Fetc%2Fpasswd",
		"url-encoded dots":             "%2e%2e%2f%2e%2e%2fetc",
		"double-encoded traversal":     "%252e%252e%252fetc",
		"absolute path":                "%2Fetc%2Fpasswd",
		"null byte":                    readyID + "%00",
		"null byte alone":              "%00",
		"uuid with traversal suffix":   readyID + "%2F..%2F..",
	}
	for name, id := range ids {
		for _, suffix := range []string{"", "/playback"} {
			t.Run(name+suffix, func(t *testing.T) {
				st := catalogStore()
				r := doFull(t, newServerWith(t, &fakePinger{}, st), http.MethodGet, "/api/v1/movies/"+id+suffix)
				if r.status != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400: %s", r.status, r.body)
				}
				assertErrorCode(t, r.body, "invalid_request")
			})
		}
	}
}

// backdrop_path comes from the database; an unsafe value is never turned into
// a /media/ URL, whatever form the escape takes.
func TestBackdropPathConfined(t *testing.T) {
	tests := []struct{ name, poster string }{
		{"../ traversal", "../../etc/passwd"},
		{"absolute path", "/etc/passwd"},
		{"url-encoded traversal", "%2e%2e%2f%2e%2e%2fetc%2fpasswd"},
		{"null byte", "poster.jpg\x00.png"},
		{"other origin", "http://evil.example/poster.jpg"},
		{"backslash traversal", `..\..\etc\passwd`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := catalogStore()
			st.movies[0].BackdropPath = ptr(tt.poster)
			r := doFull(t, newServerWith(t, &fakePinger{}, st), http.MethodGet, "/api/v1/movies/"+readyID)
			if r.status != http.StatusOK {
				t.Fatalf("status = %d: %s", r.status, r.body)
			}
			if !strings.Contains(string(r.body), `"backdrop_url":null`) {
				t.Fatalf("unsafe backdrop_path %q was exposed: %s", tt.poster, r.body)
			}
		})
	}
}

// ?sort=, ?year_from= and ?year_to= reach the store as typed values; bad
// values are a 400 before the store is asked.
func TestListMoviesSortAndYear(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		status   int
		wantSort store.MovieSort
		from, to *int // nil = no bound
	}{
		{"defaults", "", 200, "", nil, nil},
		{"added_desc", "?sort=added_desc", 200, store.SortAddedDesc, nil, nil},
		{"title_asc", "?sort=title_asc", 200, store.SortTitleAsc, nil, nil},
		{"year_desc", "?sort=year_desc", 200, store.SortYearDesc, nil, nil},
		{"year_asc", "?sort=year_asc", 200, store.SortYearAsc, nil, nil},
		{"empty sort means default", "?sort=", 200, "", nil, nil},
		{"range", "?year_from=1990&year_to=1999", 200, "", ptr(1990), ptr(1999)},
		{"from only", "?year_from=2000", 200, "", ptr(2000), nil},
		{"to only", "?year_to=2000", 200, "", nil, ptr(2000)},
		{"one year", "?year_from=2010&year_to=2010", 200, "", ptr(2010), ptr(2010)},
		{"bounds of the range", "?year_from=1880&year_to=2100", 200, "", ptr(1880), ptr(2100)},
		{"empty years mean no bound", "?year_from=&year_to=", 200, "", nil, nil},
		{"all together", "?q=a&sort=year_desc&year_from=1950&year_to=2000", 200, store.SortYearDesc, ptr(1950), ptr(2000)},
		{"unknown sort", "?sort=rating_desc", 400, "", nil, nil},
		{"sort is case-sensitive", "?sort=TITLE_ASC", 400, "", nil, nil},
		{"sql in sort", "?sort=id%3B%20DROP%20TABLE%20movies", 400, "", nil, nil},
		{"repeated sort", "?sort=title_asc&sort=year_asc", 400, "", nil, nil},
		{"year before 1880", "?year_from=1879", 400, "", nil, nil},
		{"year after 2100", "?year_to=2101", 400, "", nil, nil},
		{"from after to", "?year_from=2001&year_to=2000", 400, "", nil, nil},
		{"non-integer year", "?year_from=199x", 400, "", nil, nil},
		{"fractional year", "?year_to=1999.5", 400, "", nil, nil},
		{"repeated year", "?year_from=1990&year_from=1991", 400, "", nil, nil},
		{"overflowing year", "?year_from=99999999999999999999", 400, "", nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := catalogStore()
			r := doFull(t, newServerWith(t, &fakePinger{}, st), http.MethodGet, "/api/v1/movies"+tt.query)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.status != http.StatusOK {
				assertErrorCode(t, r.body, "invalid_request")
				if st.gotFilter != nil {
					t.Fatal("store was queried for an invalid request")
				}
				return
			}
			f := st.gotFilter
			if f.Sort != tt.wantSort {
				t.Errorf("sort = %q, want %q", f.Sort, tt.wantSort)
			}
			for _, b := range []struct {
				name      string
				got, want *int
			}{{"year_from", f.YearFrom, tt.from}, {"year_to", f.YearTo, tt.to}} {
				if (b.got == nil) != (b.want == nil) || (b.got != nil && *b.got != *b.want) {
					t.Errorf("%s = %v, want %v", b.name, b.got, b.want)
				}
			}
		})
	}
}

// ?q= reaches the store trimmed and unchanged otherwise (escaping is the
// store's job); bad values are a 400 before the store is asked.
func TestListMoviesSearch(t *testing.T) {
	long := strings.Repeat("a", 101)
	tests := []struct {
		name      string
		query     string
		status    int
		wantTitle *string // nil = no title filter
	}{
		{"no q", "", 200, nil},
		{"plain", "?q=matrix", 200, ptr("matrix")},
		{"trimmed", "?q=%20%20matrix%20", 200, ptr("matrix")},
		{"spaces only means no filter", "?q=%20%20", 200, nil},
		{"empty means no filter", "?q=", 200, nil},
		{"percent and underscore passed through literally", "?q=100%25_x", 200, ptr("100%_x")},
		{"backslash passed through", "?q=a%5Cb", 200, ptr(`a\b`)},
		{"combined with status", "?q=tie&status=ready", 200, ptr("tie")},
		{"100 characters accepted", "?q=" + strings.Repeat("a", 100), 200, ptr(strings.Repeat("a", 100))},
		{"100 multibyte characters accepted", "?q=" + strings.Repeat("%C3%A9", 100), 200, ptr(strings.Repeat("é", 100))},
		{"101 characters rejected", "?q=" + long, 400, nil},
		{"repeated q rejected", "?q=a&q=b", 400, nil},
		{"null byte rejected", "?q=a%00b", 400, nil},
		{"invalid utf-8 rejected", "?q=%FF%FE", 400, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := catalogStore()
			r := doFull(t, newServerWith(t, &fakePinger{}, st), http.MethodGet, "/api/v1/movies"+tt.query)
			if r.status != tt.status {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.status, r.body)
			}
			if tt.status != http.StatusOK {
				assertErrorCode(t, r.body, "invalid_request")
				if st.gotFilter != nil {
					t.Fatal("store was queried for an invalid request")
				}
				return
			}
			got := st.gotFilter.Title
			switch {
			case tt.wantTitle == nil && got != nil:
				t.Fatalf("title filter = %q, want none", *got)
			case tt.wantTitle != nil && (got == nil || *got != *tt.wantTitle):
				t.Fatalf("title filter = %v, want %q", got, *tt.wantTitle)
			}
		})
	}
}
