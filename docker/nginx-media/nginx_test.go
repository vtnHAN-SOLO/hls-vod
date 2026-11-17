// Package nginxmedia tests docker/nginx-media/default.conf.template by
// running it in the pinned nginx image the way compose and Kubernetes do:
// rendered by the image's envsubst entrypoint with a random signing key,
// read-only root filesystem, every capability dropped, only /tmp and
// /etc/nginx/conf.d writable. URLs are signed with the api's own signer.
package nginxmedia

import (
	"bufio"
	"crypto/rand"
	"encoding/base64"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/api/types/container"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
)

// image is the base of docker/media-delivery.Dockerfile, tag and digest:
// keep the two in sync. The test mounts the template and the guard into the
// base image instead of building the image, so it runs in go test alone.
const image = "nginxinc/nginx-unprivileged:1.30.6-alpine3.24@sha256:f5116ed4606f3eb015dd4a8e28cb9b351476dc0ad4bc6e9f93b19cec130cdf03"

// user is the media-delivery image's USER.
const user = "65532:65532"

const (
	movieID = "0b9d6b52-2d2c-4a7b-9e0e-6a4f39a0c1d2"
	otherID = "9f8e7d6c-5b4a-4392-8170-6f5e4d3c2b1a"
	secret  = "TOP-SECRET-OUTSIDE-LIBRARY"
)

// randomKey is a MEDIA_SIGNING_KEY generated at test time, in the alphabet
// `openssl rand -base64` prints (so "+" and "/" go through envsubst too).
func randomKey(t *testing.T) string {
	t.Helper()
	b := make([]byte, 36)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b)
}

// confTmpfs is the writable /etc/nginx/conf.d the template is rendered into,
// owned by the image's user (65532), as in docker-compose.yml.
const confTmpfs = "rw,mode=0755,uid=65532,gid=65532"

// nginxOptions mounts the template and the key guard and passes env, as
// docker-compose.yml does.
func nginxOptions(t *testing.T, env map[string]string, binds []string, confMode string) []testcontainers.ContainerCustomizer {
	t.Helper()
	var mounts []string
	for src, dst := range map[string]string{
		"default.conf.template":           "/etc/nginx/templates/default.conf.template",
		"05-require-media-signing-key.sh": "/docker-entrypoint.d/05-require-media-signing-key.sh",
	} {
		abs, err := filepath.Abs(src)
		if err != nil {
			t.Fatal(err)
		}
		mounts = append(mounts, abs+":"+dst+":ro")
	}
	return []testcontainers.ContainerCustomizer{
		testcontainers.WithExposedPorts("8080/tcp"),
		testcontainers.WithEnv(env),
		testcontainers.WithTmpfs(map[string]string{"/tmp": "rw", "/etc/nginx/conf.d": confMode}),
		testcontainers.WithConfigModifier(func(c *container.Config) { c.User = user }),
		testcontainers.WithHostConfigModifier(func(hc *container.HostConfig) {
			hc.Binds = append(mounts, binds...)
			hc.ReadonlyRootfs = true
			hc.CapDrop = []string{"ALL"}
			hc.SecurityOpt = []string{"no-new-privileges"}
		}),
	}
}

// startNginx runs the template with key and the given extra bind mounts and
// returns host:port of its 8080.
func startNginx(t *testing.T, key string, binds ...string) string {
	t.Helper()
	opts := append(nginxOptions(t, map[string]string{
		"MEDIA_SIGNING_KEY": key, "NGINX_ENVSUBST_FILTER": "^MEDIA_SIGNING_KEY$",
	}, binds, confTmpfs), testcontainers.WithWaitStrategy(wait.ForHTTP("/healthz").WithPort("8080/tcp").
		WithStartupTimeout(30*time.Second)))
	ctr, err := testcontainers.Run(t.Context(), image, opts...)
	testcontainers.CleanupContainer(t, ctr)
	if err != nil {
		t.Fatalf("starting nginx (is Docker running?): %v", err)
	}
	host, err := ctr.Host(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	port, err := ctr.MappedPort(t.Context(), "8080/tcp")
	if err != nil {
		t.Fatal(err)
	}
	return net.JoinHostPort(host, port.Port())
}

// library builds a movie dir as the pipeline writes it, plus the things
// nginx must never serve: hidden temp files, a partial dir, a symlink out
// and a non-media file. Returns the library dir and a secret file that sits
// next to it in the container (/srv/media/secret.txt).
func library(t *testing.T) (lib, secretFile string) {
	t.Helper()
	root := t.TempDir()
	lib = filepath.Join(root, "library")
	movie := filepath.Join(lib, movieID)
	files := map[string]string{
		"master.m3u8":          "#EXTM3U\n720p/index.m3u8\n",
		"720p/index.m3u8":      "#EXTM3U\n#EXTINF:6.0,\nseg_0000.ts\n",
		"720p/seg_0000.ts":     strings.Repeat("S", 1000),
		"poster.jpg":           "JPEG",
		"thumbnails.vtt":       "WEBVTT\n",
		"subs/0_eng.vtt":       "WEBVTT\n",
		"notes.txt":            "not a media file",
		".master.m3u8.tmp-123": "half-written",
		"../." + movieID + ".partial-1/master.m3u8": "unpublished",
	}
	for name, body := range files {
		p := filepath.Join(movie, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { // #nosec G301 -- nginx (uid 65532) must traverse the fixture
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil { // #nosec G306 -- nginx (uid 65532) must read the fixture
			t.Fatal(err)
		}
	}
	// A symlink whose target exists inside the container, outside the library.
	if err := os.Symlink("/srv/media/secret.txt", filepath.Join(movie, "720p", "seg_link.ts")); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{lib, movie, filepath.Join(movie, "720p"), filepath.Join(movie, "subs"),
		filepath.Join(lib, "."+movieID+".partial-1")} {
		if err := os.Chmod(d, 0o755); err != nil { // #nosec G302 -- nginx (uid 65532) must traverse the fixture
			t.Fatal(err)
		}
	}
	secretFile = filepath.Join(root, "secret.txt")
	if err := os.WriteFile(secretFile, []byte(secret), 0o644); err != nil { // #nosec G306 -- must be readable to prove nginx refuses it
		t.Fatal(err)
	}
	return lib, secretFile
}

type response struct {
	status int
	header http.Header
	body   string
}

// do sends one request with the path exactly as given: no client-side
// cleaning or re-encoding, which is what an attacker would send.
func do(t *testing.T, addr, method, rawPath string, header map[string]string) response {
	t.Helper()
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(t.Context(), "tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := method + " " + rawPath + " HTTP/1.1\r\nHost: media.test\r\nConnection: close\r\n"
	for k, v := range header {
		req += k + ": " + v + "\r\n"
	}
	if _, err := io.WriteString(conn, req+"\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: method})
	if err != nil {
		t.Fatalf("%s %s: %v", method, rawPath, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return response{resp.StatusCode, resp.Header, string(body)}
}

// signedDir is the signed prefix of a movie's folder, valid for ttl (negative:
// already expired), as the api hands it out: /media/{token}/{expires}/{id}/.
func signedDir(t *testing.T, s *service.MediaSigner, id string, ttl time.Duration) string {
	t.Helper()
	exp := time.Now().Add(ttl).Unix()
	return "/media/" + s.Token(id, exp) + "/" + strconv.FormatInt(exp, 10) + "/" + id + "/"
}

func TestMediaDelivery(t *testing.T) {
	lib, secretFile := library(t)
	key := randomKey(t)
	signer, err := service.NewMediaSigner(key, 0)
	if err != nil {
		t.Fatal(err)
	}
	addr := startNginx(t, key, lib+":/srv/media/library:ro", secretFile+":/srv/media/secret.txt:ro")
	m := signedDir(t, signer, movieID, time.Hour)
	// The URL the api really returns, rounded expiry and all.
	if u := signer.URL(movieID, "master.m3u8", service.PlaybackURLTTL); do(t, addr, http.MethodGet, u, nil).status != http.StatusOK {
		t.Fatalf("GET %s (from MediaSigner.URL) is not 200", u)
	}

	t.Run("serves media with type and cache policy", func(t *testing.T) {
		tests := []struct {
			path, contentType, cache string
		}{
			{m + "master.m3u8", "application/vnd.apple.mpegurl", "private, max-age=10"},
			{m + "720p/index.m3u8", "application/vnd.apple.mpegurl", "private, max-age=10"},
			{m + "720p/seg_0000.ts", "video/mp2t", "private, max-age=31536000, immutable"},
			{m + "poster.jpg", "image/jpeg", "private, max-age=3600"},
			{m + "thumbnails.vtt", "text/vtt", "private, max-age=3600"},
			{m + "subs/0_eng.vtt", "text/vtt", "private, max-age=3600"},
		}
		for _, tt := range tests {
			t.Run(tt.path, func(t *testing.T) {
				r := do(t, addr, http.MethodGet, tt.path, nil)
				if r.status != http.StatusOK {
					t.Fatalf("status = %d, body %q", r.status, r.body)
				}
				if got := r.header.Get("Content-Type"); got != tt.contentType {
					t.Errorf("Content-Type = %q, want %q", got, tt.contentType)
				}
				if got := r.header.Get("Cache-Control"); got != tt.cache {
					t.Errorf("Cache-Control = %q, want %q", got, tt.cache)
				}
				if got := r.header.Get("Access-Control-Allow-Origin"); got != "*" {
					t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
				}
				if got := r.header.Get("X-Content-Type-Options"); got != "nosniff" {
					t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
				}
			})
		}
	})

	t.Run("range request on a segment", func(t *testing.T) {
		r := do(t, addr, http.MethodGet, m+"720p/seg_0000.ts", map[string]string{"Range": "bytes=0-99"})
		if r.status != http.StatusPartialContent || r.header.Get("Content-Range") != "bytes 0-99/1000" ||
			len(r.body) != 100 || r.header.Get("Access-Control-Allow-Origin") != "*" {
			t.Fatalf("status %d, Content-Range %q, %d bytes", r.status, r.header.Get("Content-Range"), len(r.body))
		}
	})

	t.Run("CORS preflight", func(t *testing.T) {
		r := do(t, addr, http.MethodOptions, m+"master.m3u8", map[string]string{
			"Origin": "https://hlsjs.video-dev.org", "Access-Control-Request-Method": "GET",
			"Access-Control-Request-Headers": "range",
		})
		if r.status != http.StatusNoContent || r.header.Get("Access-Control-Allow-Origin") != "*" ||
			!strings.Contains(r.header.Get("Access-Control-Allow-Methods"), "GET") ||
			!strings.EqualFold(r.header.Get("Access-Control-Allow-Headers"), "Range") {
			t.Fatalf("preflight = %d %v", r.status, r.header)
		}
	})

	t.Run("a missing segment is not cached as immutable", func(t *testing.T) {
		r := do(t, addr, http.MethodGet, m+"720p/seg_9999.ts", nil)
		if r.status != http.StatusNotFound || r.header.Get("Cache-Control") != "" {
			t.Fatalf("status %d, Cache-Control %q; want 404 with no cache header", r.status, r.header.Get("Cache-Control"))
		}
		if r.header.Get("Access-Control-Allow-Origin") != "*" {
			t.Error("errors must carry CORS too, or the player cannot read the status")
		}
	})

	t.Run("only GET, HEAD and OPTIONS", func(t *testing.T) {
		for _, method := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
			if r := do(t, addr, method, m+"master.m3u8", map[string]string{"Content-Length": "0"}); r.status != http.StatusMethodNotAllowed {
				t.Errorf("%s = %d, want 405", method, r.status)
			}
		}
		if r := do(t, addr, http.MethodHead, m+"master.m3u8", nil); r.status != http.StatusOK || r.body != "" {
			t.Errorf("HEAD = %d with %d body bytes", r.status, len(r.body))
		}
	})

	t.Run("token", func(t *testing.T) {
		tok, rest, _ := strings.Cut(strings.TrimPrefix(m, "/media/"), "/")
		flip := func(c byte) byte {
			if c == 'A' {
				return 'B'
			}
			return 'A'
		}
		tampered := []byte(tok)
		tampered[5] = flip(tampered[5])
		expRest := strings.SplitN(rest, "/", 2)
		laterExp, _ := strconv.ParseInt(expRest[0], 10, 64)
		other := signedDir(t, signer, otherID, time.Hour)
		otherTok, otherRest, _ := strings.Cut(strings.TrimPrefix(other, "/media/"), "/")
		wrongKey, err := service.NewMediaSigner(randomKey(t), 0)
		if err != nil {
			t.Fatal(err)
		}
		tests := []struct {
			name   string
			path   string
			status int
		}{
			{"valid: master playlist", m + "master.m3u8", 200},
			{"valid: rung playlist", m + "720p/index.m3u8", 200},
			{"valid: segment", m + "720p/seg_0000.ts", 200},
			{"valid: poster", m + "poster.jpg", 200},
			{"valid: thumbnails", m + "thumbnails.vtt", 200},
			{"valid token, missing file", m + "720p/seg_9999.ts", 404},
			{"one character of the token changed", "/media/" + string(tampered) + "/" + rest + "720p/seg_0000.ts", 403},
			{"the expiry moved later", "/media/" + tok + "/" + strconv.FormatInt(laterExp+3600, 10) + "/" + movieID + "/master.m3u8", 403},
			{"another movie's token", "/media/" + otherTok + "/" + strings.Replace(otherRest, otherID, movieID, 1) + "master.m3u8", 403},
			{"this token on another movie", m[:len(m)-len(movieID)-1] + otherID + "/master.m3u8", 403},
			{"signed with another key", signedDir(t, wrongKey, movieID, time.Hour) + "master.m3u8", 403},
			{"expired", signedDir(t, signer, movieID, -time.Minute) + "720p/seg_0000.ts", 410},
			{"expired poster", signedDir(t, signer, movieID, -time.Minute) + "poster.jpg", 410},
			{"no token", "/media/" + movieID + "/master.m3u8", 403},
			{"no token, poster", "/media/" + movieID + "/poster.jpg", 403},
			{"token in the query string", "/media/" + movieID + "/master.m3u8?st=" + tok + "&e=" + expRest[0], 403},
			{"empty token", "/media//" + rest + "master.m3u8", 403},
			{"padded token", "/media/" + tok + "==/" + rest + "master.m3u8", 403},
			{"no expiry", "/media/" + tok + "/" + movieID + "/master.m3u8", 403},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				r := do(t, addr, http.MethodGet, tt.path, nil)
				if r.status != tt.status {
					t.Fatalf("GET %s = %d, want %d; body %q", tt.path, r.status, tt.status, r.body)
				}
				if tt.status >= 400 {
					if r.header.Get("Cache-Control") != "" {
						t.Errorf("a %d must not be cacheable: Cache-Control %q", r.status, r.header.Get("Cache-Control"))
					}
					if r.header.Get("Access-Control-Allow-Origin") != "*" {
						t.Errorf("a %d needs CORS too, or the player cannot read the status", r.status)
					}
					if r.header.Get("X-Content-Type-Options") != "nosniff" {
						t.Errorf("a %d has no X-Content-Type-Options: nosniff", r.status)
					}
					if strings.Contains(r.body, "#EXTM3U") || strings.Contains(r.body, "SSSS") {
						t.Errorf("a %d leaked media: %q", r.status, r.body)
					}
				}
			})
		}
	})

	t.Run("never escapes the library or serves what is not published", func(t *testing.T) {
		paths := []string{
			// ../ out of the signed folder, valid token
			m + "../" + otherID + "/master.m3u8",
			m + "%2e%2e/" + otherID + "/master.m3u8",
			m + "../../../../secret.txt",
			// ../ traversal, plain and in every encoding
			"/media/../../etc/passwd",
			m + "../../secret.txt",
			m + "../../../etc/passwd",
			"/media/%2e%2e/%2e%2e/etc/passwd",
			m + "%2e%2e%2f%2e%2e%2fsecret.txt",
			m + "..%2f..%2f..%2fetc%2fpasswd",
			m + "%2E%2E%2F%2E%2E%2Fsecret.txt",
			m + "%252e%252e%252fsecret.txt",
			"/media/..%2f..%2fetc%2fpasswd",
			m + "..\\..\\secret.txt",
			// absolute paths and odd slashes
			"/media//etc/passwd",
			m + "/etc/passwd",
			"/media/%2fetc%2fpasswd",
			// null bytes
			m + "master.m3u8%00.jpg",
			m + "%00",
			"/media/%00",
			// symlink inside the library pointing outside it
			m + "720p/seg_link.ts",
			// unpublished and hidden files
			strings.Replace(m, movieID, "."+movieID+".partial-1", 1) + "master.m3u8",
			m + ".master.m3u8.tmp-123",
			// not a media file, not a canonical id, directory listings
			m + "notes.txt",
			strings.Replace(m, movieID, strings.ToUpper(movieID), 1) + "master.m3u8",
			m + "",
			"/media/",
			// internal endpoints
			"/_library_check/",
			"/stub_status",
		}
		for _, p := range paths {
			r := do(t, addr, http.MethodGet, p, nil)
			if r.status == http.StatusOK || r.status == http.StatusPartialContent {
				t.Errorf("GET %s = %d, want a refusal; body %q", p, r.status, r.body)
			}
			for _, leak := range []string{secret, "root:", "unpublished", "half-written", movieID + "\""} {
				if strings.Contains(r.body, leak) {
					t.Errorf("GET %s leaked %q", p, leak)
				}
			}
		}
	})

	t.Run("probes with a readable library", func(t *testing.T) {
		if r := do(t, addr, http.MethodGet, "/healthz", nil); r.status != http.StatusOK {
			t.Fatalf("/healthz = %d", r.status)
		}
		r := do(t, addr, http.MethodGet, "/readyz", nil)
		if r.status != http.StatusOK || r.body != "{\"status\":\"ready\"}\n" || r.header.Get("Cache-Control") != "no-store" {
			t.Fatalf("/readyz = %d %q", r.status, r.body)
		}
		if strings.Contains(r.body, movieID) {
			t.Fatal("/readyz leaked the library listing")
		}
	})
}

// /readyz must fail when the mount is gone or unreadable, while /healthz
// stays up: restarting nginx would not fix a mount.
func TestReadinessFollowsTheMount(t *testing.T) {
	// t.TempDir is 0775; make it private to the test user. nginx runs as
	// uid 65532 and so can no longer open it.
	unreadable := t.TempDir()
	if err := os.Chmod(unreadable, 0o700); err != nil { // #nosec G302 -- a directory needs x; 0700 is the most private mode that is still a usable dir
		t.Fatal(err)
	}
	tests := []struct {
		name  string
		binds []string
	}{
		{"no library mount", nil},
		{"library not readable by nginx", []string{unreadable + ":/srv/media/library:ro"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			addr := startNginx(t, randomKey(t), tt.binds...)
			if r := do(t, addr, http.MethodGet, "/healthz", nil); r.status != http.StatusOK {
				t.Fatalf("/healthz = %d; liveness must not depend on the mount", r.status)
			}
			r := do(t, addr, http.MethodGet, "/readyz", nil)
			if r.status != http.StatusServiceUnavailable || !strings.Contains(r.body, `"not ready"`) {
				t.Fatalf("/readyz = %d %q, want 503 not ready", r.status, r.body)
			}
		})
	}
}

// The container must not start without a usable key: a missing one would
// leave the placeholder in the config, an empty one would make every token
// computable, and a quote or "$" would rewrite the config itself.
func TestRefusesToStartWithoutAUsableKey(t *testing.T) {
	filter := "^MEDIA_SIGNING_KEY$"
	good := map[string]string{"MEDIA_SIGNING_KEY": randomKey(t), "NGINX_ENVSUBST_FILTER": filter}
	tests := []struct {
		name     string
		env      map[string]string
		confMode string
		want     string
	}{
		{"missing", map[string]string{"NGINX_ENVSUBST_FILTER": filter}, confTmpfs, "MEDIA_SIGNING_KEY is not set"},
		{"empty", map[string]string{"MEDIA_SIGNING_KEY": "", "NGINX_ENVSUBST_FILTER": filter}, confTmpfs, "MEDIA_SIGNING_KEY is not set"},
		{"short", map[string]string{"MEDIA_SIGNING_KEY": randomKey(t)[:31], "NGINX_ENVSUBST_FILTER": filter}, confTmpfs, "at least 32 bytes"},
		{"breaks out of the string", map[string]string{
			"MEDIA_SIGNING_KEY": randomKey(t) + `"; return 200 "`, "NGINX_ENVSUBST_FILTER": filter}, confTmpfs, "only base64 characters"},
		{"nginx variable", map[string]string{
			"MEDIA_SIGNING_KEY": randomKey(t) + "$uri", "NGINX_ENVSUBST_FILTER": filter}, confTmpfs, "only base64 characters"},
		{"no envsubst filter", map[string]string{"MEDIA_SIGNING_KEY": randomKey(t)}, confTmpfs, "NGINX_ENVSUBST_FILTER"},
		// Without this check the image renders nothing, logs an ERROR and starts.
		{"conf.d not writable", good, "rw,mode=0755", "/etc/nginx/conf.d is not writable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := append(nginxOptions(t, tt.env, nil, tt.confMode),
				testcontainers.WithWaitStrategy(wait.ForExit().WithExitTimeout(30*time.Second)))
			ctr, err := testcontainers.Run(t.Context(), image, opts...)
			testcontainers.CleanupContainer(t, ctr)
			if err != nil {
				t.Fatalf("running nginx (is Docker running?): %v", err)
			}
			state, err := ctr.State(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			logs, err := ctr.Logs(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			out, _ := io.ReadAll(logs)
			if state.ExitCode == 0 || !strings.Contains(string(out), tt.want) {
				t.Fatalf("exit code %d, logs:\n%s\nwant a non-zero exit mentioning %q", state.ExitCode, out, tt.want)
			}
			if strings.Contains(string(out), "start worker process") {
				t.Fatal("nginx started anyway")
			}
		})
	}
}
