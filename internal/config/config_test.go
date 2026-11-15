package config

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// testDSN is deliberately not a real connection string: config only checks
// presence, and test fixtures must never contain credentials.
const testDSN = "test-database-url"

// testKey is generated at test time: never a literal secret.
var testKey = func() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b) // 48 bytes
}()

// The Secure cookie flag follows where the api listens.
func TestSecureCookies(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:8080": false, "[::1]:8080": false, "localhost:8080": false,
		":8080": true, "0.0.0.0:8080": true, "127.0.0.1": true, "10.0.0.5:8080": true, "media.example:8080": true, "": true,
	} {
		if got := (API{HTTPAddr: addr}).SecureCookies(); got != want {
			t.Errorf("SecureCookies(%q) = %v, want %v", addr, got, want)
		}
	}
}

func TestLoadAPI(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, c API)
	}{
		{
			name:    "missing DATABASE_URL fails",
			env:     map[string]string{},
			wantErr: "DATABASE_URL",
		},
		{
			name:    "empty DATABASE_URL fails",
			env:     map[string]string{"DATABASE_URL": "", "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey},
			wantErr: "DATABASE_URL",
		},
		{
			name:    "missing JWT_SIGNING_KEY fails",
			env:     map[string]string{"DATABASE_URL": testDSN},
			wantErr: "JWT_SIGNING_KEY",
		},
		{
			name:    "a JWT_SIGNING_KEY under 32 bytes fails",
			env:     map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey[:31], "MEDIA_SIGNING_KEY": testKey},
			wantErr: "at least 32 bytes",
		},
		{
			name: "defaults applied",
			env:  map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey},
			check: func(t *testing.T, c API) {
				if c.HTTPAddr != "127.0.0.1:8080" {
					t.Errorf("HTTPAddr = %q, want 127.0.0.1:8080 (loopback unless set)", c.HTTPAddr)
				}
				if c.ShutdownTimeout != 15*time.Second {
					t.Errorf("ShutdownTimeout = %v, want 15s", c.ShutdownTimeout)
				}
				if c.LogLevel != slog.LevelInfo {
					t.Errorf("LogLevel = %v, want info", c.LogLevel)
				}
			},
		},
		{
			// .env.example ships optional vars empty; `set -a; . .env` exports them as "".
			name: "empty optional vars fall back to defaults",
			env: map[string]string{
				"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey, "HTTP_ADDR": "", "SHUTDOWN_TIMEOUT": "", "LOG_LEVEL": "",
			},
			check: func(t *testing.T, c API) {
				if c.HTTPAddr != "127.0.0.1:8080" || c.ShutdownTimeout != 15*time.Second || c.LogLevel != slog.LevelInfo {
					t.Errorf("empty vars did not fall back to defaults: %+v", c)
				}
			},
		},
		{
			// What the Kubernetes manifests set: every interface, explicitly.
			name: "explicit :8080 is honoured",
			env:  map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey, "HTTP_ADDR": ":8080"},
			check: func(t *testing.T, c API) {
				if c.HTTPAddr != ":8080" {
					t.Errorf("HTTPAddr = %q, want :8080", c.HTTPAddr)
				}
			},
		},
		{
			name: "overrides parsed",
			env: map[string]string{
				"DATABASE_URL":      testDSN,
				"JWT_SIGNING_KEY":   testKey,
				"MEDIA_SIGNING_KEY": testKey,
				"HTTP_ADDR":         ":9999",
				"SHUTDOWN_TIMEOUT":  "3s",
				"LOG_LEVEL":         "debug",
			},
			check: func(t *testing.T, c API) {
				if c.HTTPAddr != ":9999" || c.ShutdownTimeout != 3*time.Second || c.LogLevel != slog.LevelDebug {
					t.Errorf("unexpected config: %+v", c)
				}
			},
		},
		{
			name:    "missing MEDIA_SIGNING_KEY fails",
			env:     map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey},
			wantErr: "MEDIA_SIGNING_KEY",
		},
		{
			name:    "a MEDIA_SIGNING_KEY under 32 bytes fails",
			env:     map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey[:31]},
			wantErr: "MEDIA_SIGNING_KEY must be at least 32 bytes",
		},
		{
			// The key is written into nginx's config: a quote would end the string.
			name:    "a MEDIA_SIGNING_KEY with non-base64 characters fails",
			env:     map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey + `"; return 200;`},
			wantErr: "only base64 characters",
		},
		{
			name: "TRUSTED_PROXIES parsed",
			env: map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey,
				"TRUSTED_PROXIES": "10.8.0.1/32, 10.244.0.0/16"},
			check: func(t *testing.T, c API) {
				if c.TrustedProxies != "10.8.0.1/32, 10.244.0.0/16" {
					t.Errorf("TrustedProxies = %q", c.TrustedProxies)
				}
			},
		},
		{
			name: "a malformed TRUSTED_PROXIES fails",
			env: map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey,
				"TRUSTED_PROXIES": "10.8.0.1/32, edge"},
			wantErr: `TRUSTED_PROXIES: "edge"`,
		},
		{
			name: "DEV_MEDIA_URL_TTL on loopback",
			env: map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey,
				"DEV_MEDIA_URL_TTL": "2m"},
			check: func(t *testing.T, c API) {
				if c.DevMediaURLTTL != 2*time.Minute {
					t.Errorf("DevMediaURLTTL = %v, want 2m", c.DevMediaURLTTL)
				}
			},
		},
		{
			name: "DEV_MEDIA_URL_TTL refused off loopback",
			env: map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey,
				"DEV_MEDIA_URL_TTL": "2m", "HTTP_ADDR": ":8080"},
			wantErr: "DEV_MEDIA_URL_TTL is for local development only",
		},
		{
			name: "a negative DEV_MEDIA_URL_TTL fails",
			env: map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey,
				"DEV_MEDIA_URL_TTL": "-1m"},
			wantErr: "DEV_MEDIA_URL_TTL must be positive",
		},
		{
			name:    "malformed duration fails",
			env:     map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey, "SHUTDOWN_TIMEOUT": "soon"},
			wantErr: "invalid duration",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			// The registration settings every api needs; registrationTests
			// covers them.
			t.Setenv("REGISTRATION_OPEN", "false")
			t.Setenv("TRUSTED_PROXIES", "")
			t.Setenv("MAIL_BACKEND", "resend")
			t.Setenv("RESEND_API_KEY", testKey)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}

			cfg, err := Load[API]()
			if err == nil {
				err = cfg.Validate()
			}
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error %q does not mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// The watcher and devingest need the same two variables, neither with a default.
func TestLoadMediaTools(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"missing MEDIA_ROOT fails", map[string]string{"DATABASE_URL": testDSN}, "MEDIA_ROOT"},
		{"empty MEDIA_ROOT fails", map[string]string{"DATABASE_URL": testDSN, "MEDIA_ROOT": ""}, "MEDIA_ROOT"},
		{"missing DATABASE_URL fails", map[string]string{"MEDIA_ROOT": "var/media"}, "DATABASE_URL"},
		{"both set", map[string]string{"DATABASE_URL": testDSN, "MEDIA_ROOT": "var/media"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			dev, devErr := Load[DevIngest]()
			w, wErr := Load[Watcher]()
			for name, err := range map[string]error{"DevIngest": devErr, "Watcher": wErr} {
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Errorf("%s: err = %v, want it to mention %s", name, err, tt.wantErr)
					}
				} else if err != nil {
					t.Errorf("%s: %v", name, err)
				}
			}
			if tt.wantErr == "" && (dev.MediaRoot != "var/media" || dev.DatabaseURL != testDSN ||
				w.MediaRoot != "var/media" || w.DatabaseURL != testDSN) {
				t.Fatalf("dev = %+v, watcher = %+v", dev, w)
			}
		})
	}
}

// clearEnv unsets every variable the config reads so the developer's shell
// can't leak into the test. t.Setenv restores the originals afterwards.
func clearEnv(t *testing.T) {
	t.Helper()
	for _, k := range []string{"DATABASE_URL", "HTTP_ADDR", "SHUTDOWN_TIMEOUT", "LOG_LEVEL", "MEDIA_ROOT", "JWT_SIGNING_KEY", "MEDIA_SIGNING_KEY", "DEV_MEDIA_URL_TTL",
		"TRUSTED_PROXIES",
		"REGISTRATION_OPEN", "MAIL_BACKEND", "MAIL_FROM", "PUBLIC_BASE_URL", "RESEND_API_KEY", "MAIL_DAILY_CAP",
		"TURNSTILE_SITE_KEY", "TURNSTILE_SECRET_KEY"} {
		t.Setenv(k, "") // registers restore on cleanup
		if err := os.Unsetenv(k); err != nil {
			t.Fatalf("unsetenv %s: %v", k, err)
		}
	}
}

func TestLoadDispatcher(t *testing.T) {
	local := map[string]string{"DATABASE_URL": testDSN, "DISPATCH_BACKEND": "local",
		"TRANSCODER_BIN": "bin/transcoder", "MEDIA_ROOT": "var/media"}
	kube := map[string]string{"DATABASE_URL": testDSN, "DISPATCH_BACKEND": "kubernetes",
		"K8S_NAMESPACE": "media", "TRANSCODER_IMAGE": "img@sha256:x", "DATABASE_SECRET_NAME": "pg",
		"DATABASE_SECRET_KEY": "url", "MEDIA_PVC": "media", "TRANSCODER_SERVICE_ACCOUNT": "transcoder"}
	with := func(base map[string]string, k, v string) map[string]string {
		m := map[string]string{}
		for a, b := range base {
			m[a] = b
		}
		if v == "<unset>" {
			delete(m, k)
		} else {
			m[k] = v
		}
		return m
	}
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
	}{
		{"local", local, ""},
		{"kubernetes", kube, ""},
		{"no backend: no guessing", with(local, "DISPATCH_BACKEND", "<unset>"), "DISPATCH_BACKEND"},
		{"empty backend", with(local, "DISPATCH_BACKEND", ""), "DISPATCH_BACKEND"},
		{"unknown backend", with(local, "DISPATCH_BACKEND", "docker"), `"local" or "kubernetes"`},
		{"no DATABASE_URL", with(local, "DATABASE_URL", "<unset>"), "DATABASE_URL"},
		{"local without the binary", with(local, "TRANSCODER_BIN", "<unset>"), "TRANSCODER_BIN"},
		{"local without MEDIA_ROOT", with(local, "MEDIA_ROOT", "<unset>"), "MEDIA_ROOT"},
		{"kubernetes without an image", with(kube, "TRANSCODER_IMAGE", "<unset>"), "TRANSCODER_IMAGE"},
		{"kubernetes without the Secret name", with(kube, "DATABASE_SECRET_NAME", "<unset>"), "DATABASE_SECRET_NAME"},
		{"kubernetes without the media claim", with(kube, "MEDIA_PVC", "<unset>"), "MEDIA_PVC"},
		{"kubernetes without the transcoder ServiceAccount", with(kube, "TRANSCODER_SERVICE_ACCOUNT", "<unset>"), "TRANSCODER_SERVICE_ACCOUNT"},
		{"zero concurrency", with(local, "MAX_CONCURRENT", "0"), "MAX_CONCURRENT"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			for _, k := range []string{"DISPATCH_BACKEND", "MAX_CONCURRENT", "POLL_INTERVAL", "JOB_DEADLINE",
				"TRANSCODER_BIN", "K8S_NAMESPACE", "TRANSCODER_IMAGE", "DATABASE_SECRET_NAME", "DATABASE_SECRET_KEY",
				"MEDIA_PVC", "MEDIA_MOUNT_PATH", "JOB_TTL", "TRANSCODER_RUN_AS_USER", "TRANSCODER_RUN_AS_GROUP",
				"TRANSCODER_SERVICE_ACCOUNT", "IMAGE_PULL_SECRET", "TRANSCODER_CPU_REQUEST", "TRANSCODER_CPU_LIMIT",
				"TRANSCODER_MEMORY_REQUEST", "TRANSCODER_MEMORY_LIMIT"} {
				t.Setenv(k, "")
				if err := os.Unsetenv(k); err != nil {
					t.Fatal(err)
				}
			}
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := Load[Dispatcher]()
			if err == nil {
				err = cfg.Validate()
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cfg.HTTPAddr != "127.0.0.1:8090" || cfg.MaxConcurrent != 1 || cfg.PollInterval != 5*time.Second ||
				cfg.JobDeadline != 6*time.Hour || cfg.JobTTL != 24*time.Hour || cfg.RunAsUser != 65532 ||
				cfg.TranscoderCPURequest != "2" || cfg.TranscoderCPULimit != "4" ||
				cfg.TranscoderMemoryRequest != "1Gi" || cfg.TranscoderMemoryLimit != "2Gi" || cfg.ImagePullSecret != "" {
				t.Fatalf("defaults = %+v", cfg)
			}
		})
	}
}

func TestLoadAPIRegistration(t *testing.T) {
	base := map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey}
	open := map[string]string{"REGISTRATION_OPEN": "true", "MAIL_BACKEND": "resend", "RESEND_API_KEY": testKey,
		"MAIL_FROM": "HLS VOD <noreply@media.example>", "PUBLIC_BASE_URL": "https://media.example/",
		"TURNSTILE_SITE_KEY": testKey, "TURNSTILE_SECRET_KEY": testKey, "HTTP_ADDR": ":8080"}
	with := func(m map[string]string, kv ...string) map[string]string {
		out := map[string]string{}
		for k, v := range base {
			out[k] = v
		}
		for k, v := range m {
			out[k] = v
		}
		for i := 0; i+1 < len(kv); i += 2 {
			if kv[i+1] == "-" {
				delete(out, kv[i])
			} else {
				out[kv[i]] = kv[i+1]
			}
		}
		return out
	}
	closed := map[string]string{"REGISTRATION_OPEN": "false", "MAIL_BACKEND": "log"}
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(t *testing.T, c API)
	}{
		{name: "REGISTRATION_OPEN is required", env: with(closed, "REGISTRATION_OPEN", "-"), wantErr: "REGISTRATION_OPEN"},
		{name: "REGISTRATION_OPEN must not be empty", env: with(closed, "REGISTRATION_OPEN", ""), wantErr: "REGISTRATION_OPEN"},
		{name: "REGISTRATION_OPEN must be a bool", env: with(closed, "REGISTRATION_OPEN", "maybe"), wantErr: `"RegistrationOpen" of type "bool"`},
		{name: "MAIL_BACKEND is required", env: with(closed, "MAIL_BACKEND", "-"), wantErr: "MAIL_BACKEND"},
		{name: "MAIL_BACKEND must be known", env: with(closed, "MAIL_BACKEND", "smtp"), wantErr: `MAIL_BACKEND must be "resend" or "log"`},
		{name: "closed, log backend, nothing else", env: with(closed), check: func(t *testing.T, c API) {
			if c.RegistrationOpen || c.MailBackend != "log" || c.MailDailyCap != 90 {
				t.Errorf("config = %+v", c)
			}
		}},
		{name: "log backend refused off loopback", env: with(closed, "HTTP_ADDR", ":8080"), wantErr: "MAIL_BACKEND=log"},
		{name: "open with everything", env: with(open), check: func(t *testing.T, c API) {
			if !c.RegistrationOpen || c.BaseURL() != "https://media.example" {
				t.Errorf("config = %+v, BaseURL %q", c, c.BaseURL())
			}
		}},
		{name: "resend needs RESEND_API_KEY", env: with(open, "RESEND_API_KEY", "-"), wantErr: "RESEND_API_KEY is required when MAIL_BACKEND=resend"},
		{name: "resend needs RESEND_API_KEY even when closed", env: with(open, "REGISTRATION_OPEN", "false", "RESEND_API_KEY", "-"), wantErr: "RESEND_API_KEY"},
		{name: "open needs MAIL_FROM", env: with(open, "MAIL_FROM", "-"), wantErr: "MAIL_FROM is required"},
		{name: "open needs PUBLIC_BASE_URL", env: with(open, "PUBLIC_BASE_URL", "-"), wantErr: "PUBLIC_BASE_URL is required"},
		{name: "open needs the Turnstile secret", env: with(open, "TURNSTILE_SECRET_KEY", "-"), wantErr: "TURNSTILE_SECRET_KEY is required"},
		{name: "open needs the Turnstile site key", env: with(open, "TURNSTILE_SITE_KEY", "-"), wantErr: "TURNSTILE_SITE_KEY is required"},
		{name: "Turnstile keys go together", env: with(closed, "TURNSTILE_SITE_KEY", testKey), wantErr: "set both or neither"},
		{name: "MAIL_FROM must be an address", env: with(open, "MAIL_FROM", "not an address"), wantErr: "MAIL_FROM"},
		{name: "PUBLIC_BASE_URL must be an origin", env: with(open, "PUBLIC_BASE_URL", "https://media.example/app?x=1"), wantErr: "must be an origin"},
		{name: "PUBLIC_BASE_URL https off loopback", env: with(open, "PUBLIC_BASE_URL", "http://media.example"), wantErr: "must use https"},
		{name: "PUBLIC_BASE_URL http on loopback", env: with(closed, "PUBLIC_BASE_URL", "http://127.0.0.1:5173"), check: func(t *testing.T, c API) {
			if c.BaseURL() != "http://127.0.0.1:5173" {
				t.Errorf("BaseURL = %q", c.BaseURL())
			}
		}},
		{name: "MAIL_DAILY_CAP at least 1", env: with(closed, "MAIL_DAILY_CAP", "0"), wantErr: "MAIL_DAILY_CAP"},
		{name: "Google off by default", env: with(closed), check: func(t *testing.T, c API) {
			if c.GoogleEnabled() {
				t.Error("Google enabled without a client")
			}
		}},
		{name: "Google client id and secret go together", env: with(open, "GOOGLE_CLIENT_ID", testKey), wantErr: "GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET go together"},
		{name: "Google needs PUBLIC_BASE_URL", env: with(closed, "GOOGLE_CLIENT_ID", testKey, "GOOGLE_CLIENT_SECRET", testKey),
			wantErr: "PUBLIC_BASE_URL is required with GOOGLE_CLIENT_ID"},
		{name: "Google with everything", env: with(open, "GOOGLE_CLIENT_ID", testKey, "GOOGLE_CLIENT_SECRET", testKey), check: func(t *testing.T, c API) {
			if !c.GoogleEnabled() {
				t.Error("Google not enabled")
			}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("TRUSTED_PROXIES", "")
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			cfg, err := Load[API]()
			if err == nil {
				err = cfg.Validate()
			}
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

// TRUSTED_PROXIES has no default: set, possibly empty, or the api refuses to
// start. Whether to believe X-Forwarded-For is never a guess.
func TestTrustedProxiesRequired(t *testing.T) {
	clearEnv(t)
	for k, v := range map[string]string{"DATABASE_URL": testDSN, "JWT_SIGNING_KEY": testKey, "MEDIA_SIGNING_KEY": testKey,
		"REGISTRATION_OPEN": "false", "MAIL_BACKEND": "resend", "RESEND_API_KEY": testKey, "TRUSTED_PROXIES": ""} {
		t.Setenv(k, v)
	}
	if _, err := Load[API](); err != nil {
		t.Fatalf("empty TRUSTED_PROXIES: %v", err)
	}
	if err := os.Unsetenv("TRUSTED_PROXIES"); err != nil { // t.Setenv restores it
		t.Fatal(err)
	}
	if _, err := Load[API](); err == nil || !strings.Contains(err.Error(), "TRUSTED_PROXIES") {
		t.Fatalf("unset TRUSTED_PROXIES: err = %v", err)
	}
}
