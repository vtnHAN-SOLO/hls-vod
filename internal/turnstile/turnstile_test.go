package turnstile

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func TestVerify(t *testing.T) {
	secret := randomSecret()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.FormValue("secret") != secret {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch r.FormValue("response") {
		case "good":
			_, _ = w.Write([]byte(`{"success":true,"error-codes":[]}`))
		case "bad":
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["invalid-input-response"]}`))
		case "internal":
			_, _ = w.Write([]byte(`{"success":false,"error-codes":["internal-error"]}`))
		case "garbage":
			_, _ = w.Write([]byte(`<html>`))
		case "slow":
			<-release
		default:
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) }) // runs first: lets the slow handler return
	v := New(secret, srv.URL, slog.New(slog.DiscardHandler))

	for _, tt := range []struct {
		token string
		want  error
	}{
		{"good", nil},
		{"bad", ErrRejected},
		{"", ErrRejected},
		{strings.Repeat("x", MaxTokenLen+1), ErrRejected},
		{"internal", ErrUnavailable},
		{"garbage", ErrUnavailable},
		{"502", ErrUnavailable},
	} {
		if err := v.Verify(t.Context(), tt.token, ""); !errors.Is(err, tt.want) || (tt.want == nil) != (err == nil) {
			t.Errorf("Verify(%.10q) = %v, want %v", tt.token, err, tt.want)
		}
	}

	t.Run("timeout is unavailable, within the 5 s bound", func(t *testing.T) {
		start := time.Now()
		if err := v.Verify(t.Context(), "slow", ""); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("slow siteverify = %v", err)
		}
		if d := time.Since(start); d > Timeout+500*time.Millisecond {
			t.Fatalf("took %v", d)
		}
	})

	t.Run("remoteip is sent when known, left out otherwise", func(t *testing.T) {
		var got []string
		var has []bool
		echo := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = r.ParseForm()
			got = append(got, r.PostForm.Get("remoteip"))
			has = append(has, r.PostForm.Has("remoteip"))
			_, _ = w.Write([]byte(`{"success":true}`))
		}))
		t.Cleanup(echo.Close)
		v := New(secret, echo.URL, slog.New(slog.DiscardHandler))
		if err := v.Verify(t.Context(), "good", "203.0.113.5"); err != nil {
			t.Fatal(err)
		}
		if err := v.Verify(t.Context(), "good", ""); err != nil {
			t.Fatal(err)
		}
		if got[0] != "203.0.113.5" || !has[0] || has[1] {
			t.Fatalf("remoteip = %q (sent %v)", got, has)
		}
	})

	t.Run("unreachable is unavailable, and the error never carries the secret", func(t *testing.T) {
		down := httptest.NewServer(http.NotFoundHandler())
		down.Close()
		err := New(secret, down.URL, slog.New(slog.DiscardHandler)).Verify(t.Context(), "good", "")
		if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), secret) {
			t.Fatalf("unreachable = %v", err)
		}
	})

	t.Run("a refused secret key is unavailable and logged once at ERROR without the key", func(t *testing.T) {
		for _, code := range []string{"invalid-input-secret", "missing-input-secret"} {
			t.Run(code, func(t *testing.T) {
				refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					_, _ = w.Write([]byte(`{"success":false,"error-codes":["` + code + `"]}`))
				}))
				t.Cleanup(refusing.Close)
				var buf bytes.Buffer
				v := New(secret, refusing.URL, slog.New(slog.NewJSONHandler(&buf, nil)))

				for range 2 {
					if err := v.Verify(t.Context(), "good", ""); !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), secret) {
						t.Fatalf("Verify = %v, want ErrUnavailable without the secret", err)
					}
				}
				if strings.Contains(buf.String(), secret) {
					t.Fatal("the log carries the secret key")
				}
				lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
				if len(lines) != 2 {
					t.Fatalf("want one log line per occurrence (2), got %d: %s", len(lines), buf.String())
				}
				for _, line := range lines {
					var rec struct {
						Level, Msg string
						Code       string `json:"error_code"`
					}
					if err := json.Unmarshal([]byte(line), &rec); err != nil {
						t.Fatal(err)
					}
					if rec.Level != "ERROR" || !strings.Contains(rec.Msg, "Turnstile secret key is invalid") || rec.Code != code {
						t.Fatalf("log line = %s", line)
					}
				}
			})
		}
	})
}
