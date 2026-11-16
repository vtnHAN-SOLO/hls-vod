package handler

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// posterSig signs a URL of the ready movie's poster as docs/openapi.yaml
// describes it, written out independently of the service: a drift in either
// breaks this test.
func posterSig(t *testing.T, width int, hash string, exp int64) string {
	t.Helper()
	k := hmac.New(sha256.New, []byte(fixtures(t).mediaKey))
	k.Write([]byte("poster-url-signing-v1"))
	mac := hmac.New(sha256.New, k.Sum(nil))
	fmt.Fprintf(mac, "%s/%d/%s/%d", readyID, width, hash, exp)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func posterPath(id string, width int, hash string, exp int64, sig string) string {
	return fmt.Sprintf("/api/v1/posters/%s/%d/%s.jpg?exp=%d&sig=%s", id, width, hash, exp, url.QueryEscape(sig))
}

func TestPosterInMovieResponses(t *testing.T) {
	f := fixtures(t)
	srv := newServerWith(t, &fakePinger{}, catalogStore())
	r := doAs(t, srv, http.MethodGet, "/api/v1/movies/"+readyID, f.viewerToken)
	if r.status != http.StatusOK {
		t.Fatalf("detail = %d %s", r.status, r.body)
	}
	var m movieDetailJSON
	decode(t, r.body, &m)
	hash := fmt.Sprintf("%x", readyPosterHash)
	if m.Poster == nil || m.Poster.Hash != hash || len(m.Poster.Variants) != 2 {
		t.Fatalf("poster = %+v", m.Poster)
	}
	for i, want := range []int{300, 600} {
		v := m.Poster.Variants[i]
		if v.Width != want || v.Height != want*3/2 || !strings.HasPrefix(v.URL, fmt.Sprintf("/api/v1/posters/%s/%d/%s.jpg?exp=", readyID, want, hash)) {
			t.Errorf("variant %d = %+v", i, v)
		}
	}

	// The URL is the permission: no access token.
	img := doAs(t, srv, http.MethodGet, m.Poster.Variants[1].URL, "")
	if img.status != http.StatusOK || string(img.body) != "\xff\xd8\xff a 600 wide jpeg" {
		t.Fatalf("poster image = %d %q", img.status, img.body)
	}
	if ct := img.header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q", ct)
	}
	if img.header.Get("ETag") != `"`+hash+`"` {
		t.Errorf("ETag = %q", img.header.Get("ETag"))
	}
	cc := img.header.Get("Cache-Control")
	var maxAge int64
	if _, err := fmt.Sscanf(cc, "private, max-age=%d, immutable", &maxAge); err != nil ||
		maxAge < int64((24*time.Hour).Seconds())-5 || maxAge > int64((25*time.Hour).Seconds()) {
		t.Errorf("Cache-Control = %q: want private, up to the URL's expiry, immutable", cc)
	}

	req, _ := http.NewRequestWithContext(t.Context(), http.MethodGet, srv.URL+m.Poster.Variants[1].URL, nil)
	req.Header.Set("If-None-Match", `"`+hash+`"`)
	if r := send(t, srv, req); r.status != http.StatusNotModified || len(r.body) != 0 {
		t.Fatalf("If-None-Match = %d %q", r.status, r.body)
	}

	// A movie without a poster says null.
	decode(t, doAs(t, srv, http.MethodGet, "/api/v1/movies/"+badPosterID, f.viewerToken).body, &m)
	if m.Poster != nil {
		t.Fatalf("no poster = %+v", m.Poster)
	}
}

func TestPosterURLChecks(t *testing.T) {
	st := catalogStore()
	srv := newServerWith(t, &fakePinger{}, st)
	hash := fmt.Sprintf("%x", readyPosterHash)
	future := time.Now().Add(time.Hour).Unix()
	past := time.Now().Add(-time.Minute).Unix()
	good := posterSig(t, 300, hash, future)
	otherHash := fmt.Sprintf("%x", sha256.Sum256([]byte("an older upload")))

	tests := []struct {
		name string
		path string
		want int
		code string
	}{
		{"genuine", posterPath(readyID, 300, hash, future, good), http.StatusOK, ""},
		{"no signature", fmt.Sprintf("/api/v1/posters/%s/300/%s.jpg?exp=%d", readyID, hash, future), http.StatusForbidden, codeForbidden},
		{"no exp", fmt.Sprintf("/api/v1/posters/%s/300/%s.jpg?sig=%s", readyID, hash, good), http.StatusForbidden, codeForbidden},
		{"tampered signature", posterPath(readyID, 300, hash, future, "A"+good[1:]), http.StatusForbidden, codeForbidden},
		{"exp moved later", posterPath(readyID, 300, hash, future+3600, good), http.StatusForbidden, codeForbidden},
		{"another width under the same signature", posterPath(readyID, 600, hash, future, good), http.StatusForbidden, codeForbidden},
		{"another movie under the same signature", posterPath(unpublishedID, 300, hash, future, good), http.StatusForbidden, codeForbidden},
		{"expired, genuine", posterPath(readyID, 300, hash, past, posterSig(t, 300, hash, past)), http.StatusGone, codeExpired},
		{"expired, forged", posterPath(readyID, 300, hash, past, good), http.StatusForbidden, codeForbidden},
		// Signed, but no longer the movie's poster: a new upload changed the hash.
		{"stale hash", posterPath(readyID, 300, otherHash, future, posterSig(t, 300, otherHash, future)), http.StatusNotFound, codeNotFound},
		{"width with no variant", posterPath(readyID, 1000, hash, future, posterSig(t, 1000, hash, future)), http.StatusNotFound, codeNotFound},
		{"width no URL has", posterPath(readyID, 301, hash, future, posterSig(t, 301, hash, future)), http.StatusNotFound, codeNotFound},
		{"not a uuid", fmt.Sprintf("/api/v1/posters/x/300/%s.jpg?exp=%d&sig=%s", hash, future, good), http.StatusNotFound, codeNotFound},
		{"not .jpg", strings.Replace(posterPath(readyID, 300, hash, future, good), ".jpg", ".png", 1), http.StatusNotFound, codeNotFound},
		{"uppercase hash", posterPath(readyID, 300, strings.ToUpper(hash), future, good), http.StatusNotFound, codeNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := doAs(t, srv, http.MethodGet, tt.path, "")
			if r.status != tt.want {
				t.Fatalf("status = %d, want %d: %s", r.status, tt.want, r.body)
			}
			if tt.code != "" && errorCode(t, r) != tt.code {
				t.Fatalf("code = %q, want %q", errorCode(t, r), tt.code)
			}
		})
	}

	st.err = errors.New("db down")
	if r := doAs(t, srv, http.MethodGet, posterPath(readyID, 300, hash, future, good), ""); r.status != http.StatusInternalServerError {
		t.Fatalf("store down = %d", r.status)
	}
}

// Every viewer endpoint shows published movies only; an admin sees all.
func TestUnpublishedHiddenFromViewers(t *testing.T) {
	f := fixtures(t)
	st := catalogStore()
	srv := newServerWith(t, &fakePinger{}, st)
	for _, path := range []string{"/api/v1/movies/" + unpublishedID, "/api/v1/movies/" + unpublishedID + "/playback"} {
		if r := doAs(t, srv, http.MethodGet, path, f.viewerToken); r.status != http.StatusNotFound {
			t.Errorf("viewer %s = %d, want 404", path, r.status)
		}
		if r := doAs(t, srv, http.MethodGet, path, f.adminToken); r.status != http.StatusOK {
			t.Errorf("admin %s = %d, want 200: %s", path, r.status, r.body)
		}
	}
	if r := call(t, srv, http.MethodPost, "/api/v1/progress", f.viewerToken, `{"movie_id":"`+unpublishedID+`","position_sec":5}`); r.status != http.StatusNotFound {
		t.Errorf("viewer progress on an unpublished movie = %d, want 404", r.status)
	}
	// The list asks the store for published movies for a viewer only.
	doAs(t, srv, http.MethodGet, "/api/v1/movies", f.viewerToken)
	if st.gotFilter == nil || !st.gotFilter.PublishedOnly {
		t.Errorf("viewer list filter = %+v, want PublishedOnly", st.gotFilter)
	}
	doAs(t, srv, http.MethodGet, "/api/v1/movies", f.adminToken)
	if st.gotFilter == nil || st.gotFilter.PublishedOnly {
		t.Errorf("admin list filter = %+v, want every movie", st.gotFilter)
	}
	doAs(t, srv, http.MethodGet, "/api/v1/movies/featured", f.viewerToken)
	if st.gotFilter == nil || !st.gotFilter.PublishedOnly {
		t.Errorf("spotlight fallback filter = %+v, want PublishedOnly", st.gotFilter)
	}
}

// pngOf is a w×h PNG.
func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 90, A: 255}) // #nosec G115 -- wraps on purpose
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestAdminPosterAndPublish(t *testing.T) {
	f := fixtures(t)
	st := newFakeAdminStore(t)
	srv := newAdminServer(t, st)
	put := func(id, token, contentType string, body []byte) response {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPut, srv.URL+"/api/v1/admin/movies/"+id+"/poster", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", contentType)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return send(t, srv, req)
	}
	as := func(method, path string) response {
		t.Helper()
		return call(t, srv, method, "/api/v1/admin/movies/"+path, f.adminToken, "")
	}
	wantCode := func(r response, status int, code string) {
		t.Helper()
		if r.status != status || errorCode(t, r) != code {
			t.Fatalf("= %d %s, want %d %s", r.status, r.body, status, code)
		}
	}

	// The draft is ready but has no poster and no overview.
	wantCode(as(http.MethodPost, unpublishedID+"/publish"), http.StatusConflict, "missing_poster")
	wantCode(as(http.MethodPost, failedID+"/publish"), http.StatusConflict, "not_ready")
	wantCode(as(http.MethodPost, unpublishedID+"/feature"), http.StatusConflict, codeNotPublished)

	// A PNG sent as image/jpeg: the bytes decide, not the header.
	r := put(unpublishedID, f.adminToken, "image/jpeg", pngOf(t, 700, 1050))
	if r.status != http.StatusOK {
		t.Fatalf("upload = %d %s", r.status, r.body)
	}
	var m adminMovieJSON
	decode(t, r.body, &m)
	if m.Poster == nil || len(m.Poster.Variants) != 2 || m.Poster.Variants[1].Width != 600 || m.PublishedAt != nil {
		t.Fatalf("after upload: %+v", m)
	}
	first := m.Poster.Hash
	wantCode(as(http.MethodPost, unpublishedID+"/publish"), http.StatusConflict, "missing_overview")
	if r := call(t, srv, http.MethodPatch, "/api/v1/admin/movies/"+unpublishedID, f.adminToken, `{"overview":"Finally out."}`); r.status != http.StatusOK {
		t.Fatalf("overview = %d", r.status)
	}
	if r := as(http.MethodPost, unpublishedID+"/publish"); r.status != http.StatusNoContent {
		t.Fatalf("publish = %d %s", r.status, r.body)
	}
	published := st.movies[unpublishedID].PublishedAt
	if published == nil {
		t.Fatal("not published")
	}
	if r := as(http.MethodPost, unpublishedID+"/publish"); r.status != http.StatusNoContent || st.movies[unpublishedID].PublishedAt != published {
		t.Fatal("publishing again changed the publish time")
	}

	// A new upload changes the hash, so every URL.
	decode(t, put(unpublishedID, f.adminToken, "application/octet-stream", pngOf(t, 1200, 1800)).body, &m)
	if m.Poster == nil || m.Poster.Hash == first || len(m.Poster.Variants) != 3 {
		t.Fatalf("re-upload: %+v", m.Poster)
	}

	// Featured needs published; unpublishing takes it out of the spotlight.
	if r := as(http.MethodPost, unpublishedID+"/feature"); r.status != http.StatusNoContent {
		t.Fatalf("feature = %d %s", r.status, r.body)
	}
	wantCode(as(http.MethodDelete, unpublishedID+"/poster"), http.StatusConflict, codeMoviePublished)
	if r := as(http.MethodDelete, unpublishedID+"/publish"); r.status != http.StatusNoContent {
		t.Fatalf("unpublish = %d %s", r.status, r.body)
	}
	if mv := st.movies[unpublishedID]; mv.PublishedAt != nil || mv.FeaturedAt != nil {
		t.Fatalf("after unpublish: published %v featured %v", mv.PublishedAt, mv.FeaturedAt)
	}
	if r := as(http.MethodDelete, unpublishedID+"/publish"); r.status != http.StatusNoContent {
		t.Fatalf("unpublish again = %d", r.status)
	}
	if r := as(http.MethodDelete, unpublishedID+"/poster"); r.status != http.StatusNoContent || st.movies[unpublishedID].PosterHash != nil {
		t.Fatalf("delete poster = %d %s", r.status, r.body)
	}

	// Refused uploads.
	wantCode(put(readyID, f.adminToken, "image/png", []byte("a text file named poster.png")), http.StatusBadRequest, codeInvalidImage)
	wantCode(put(readyID, f.adminToken, "image/png", pngOf(t, 299, 600)), http.StatusBadRequest, codeInvalidImage)
	wantCode(put(readyID, f.adminToken, "image/png", make([]byte, 10<<20+1)), http.StatusRequestEntityTooLarge, codeTooLarge)
	wantCode(put(readyID, f.viewerToken, "image/png", pngOf(t, 300, 450)), http.StatusForbidden, codeForbidden)
	if r := put(readyID, "", "image/png", pngOf(t, 300, 450)); r.status != http.StatusUnauthorized {
		t.Fatalf("no token = %d", r.status)
	}

	// Ids, roles and a failing store, for every new endpoint.
	unknown := "9a8b7c6d-5e4f-4a3b-8c2d-1e0f9a8b7c6d"
	for _, ep := range []struct{ method, suffix string }{
		{http.MethodPost, "/publish"}, {http.MethodDelete, "/publish"}, {http.MethodDelete, "/poster"}, {http.MethodPut, "/poster"},
	} {
		send := func(id, token string) response {
			t.Helper()
			if ep.method == http.MethodPut {
				return put(id, token, "image/png", pngOf(t, 300, 450))
			}
			return call(t, srv, ep.method, "/api/v1/admin/movies/"+id+ep.suffix, token, "")
		}
		name := ep.method + " " + ep.suffix
		if r := send("not-a-uuid", f.adminToken); r.status != http.StatusBadRequest {
			t.Errorf("%s bad id = %d", name, r.status)
		}
		if r := send(unknown, f.adminToken); r.status != http.StatusNotFound {
			t.Errorf("%s unknown = %d %s", name, r.status, r.body)
		}
		if r := send(readyID, f.viewerToken); r.status != http.StatusForbidden {
			t.Errorf("%s viewer = %d", name, r.status)
		}
		if r := send(readyID, ""); r.status != http.StatusUnauthorized {
			t.Errorf("%s no token = %d", name, r.status)
		}
		st.err = errors.New("db down")
		if r := send(readyID, f.adminToken); r.status != http.StatusInternalServerError {
			t.Errorf("%s store down = %d", name, r.status)
		}
		st.err = nil
	}
}
