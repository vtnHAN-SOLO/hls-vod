// Package config loads service configuration from environment variables.
//
// Secrets are always tagged `required,notEmpty` and never carry an envDefault:
// a missing secret must fail fast at startup, not fall back to a placeholder.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"

	"github.com/vtnHAN-SOLO/hls-vod/internal/clientip"
)

// Common holds settings shared by every binary.
type Common struct {
	LogLevel slog.Level `env:"LOG_LEVEL" envDefault:"info"`
}

// API is the configuration for cmd/api.
type API struct {
	Common

	// HTTPAddr defaults to loopback: a bare `make run` is never reachable
	// from the LAN. Containers must set HTTP_ADDR=:8080 explicitly; if a
	// manifest forgets, the probes fail visibly instead of anything being
	// exposed silently.
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:"127.0.0.1:8080"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// DatabaseURL is a secret (it embeds credentials). No default.
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`

	// JWTSigningKey signs and verifies access tokens (HS256). A secret: no
	// default, at least MinJWTKeyLen bytes.
	JWTSigningKey string `env:"JWT_SIGNING_KEY,required,notEmpty"`

	// MediaSigningKey signs /media/ URLs; media-delivery verifies them with
	// the same key. A secret: no default, see ValidMediaSigningKey.
	MediaSigningKey string `env:"MEDIA_SIGNING_KEY,required,notEmpty"`

	// TrustedProxies is the comma-separated CIDR list of the proxies in
	// front of the api (clientip.Parse). Required, with no default, but may
	// be empty: then the client is the TCP peer and X-Forwarded-For is
	// ignored (local development). In production it covers the edge
	// proxy's address as the cluster sees it (through the tunnel) and the
	// cluster's pod CIDR (the ingress controller).
	TrustedProxies string `env:"TRUSTED_PROXIES,required"`

	// DevMediaURLTTL, when set, replaces the lifetime of every signed media
	// URL (normally 6 h for playback, 24 h for posters), so expiry can be
	// seen in minutes. Local development only: refused unless the api
	// listens on loopback.
	DevMediaURLTTL time.Duration `env:"DEV_MEDIA_URL_TTL"`

	// RegistrationOpen turns POST /api/v1/auth/register on or off (the kill
	// switch). Required with no default: whether strangers may sign up is
	// never a guess. Everything else (login, verify, resend) works either way.
	RegistrationOpen bool `env:"REGISTRATION_OPEN,required,notEmpty"`

	// MailBackend is "resend" (send through Resend) or "log" (write every
	// mail, link included, to the api log; refused off loopback). Required,
	// no default.
	MailBackend string `env:"MAIL_BACKEND,required,notEmpty"`
	// MailFrom is the sender, e.g. "HLS VOD <noreply@DOMAIN>", and
	// PublicBaseURL the SPA's origin the mailed links point at. Both
	// required when registration is open.
	MailFrom      string `env:"MAIL_FROM"`
	PublicBaseURL string `env:"PUBLIC_BASE_URL"`
	// ResendAPIKey is a secret: no default, required when MAIL_BACKEND=resend.
	ResendAPIKey string `env:"RESEND_API_KEY"`
	// MailDailyCap: at most this many mails in any 24 hours, across
	// replicas; under Resend's free 100 a day.
	MailDailyCap int `env:"MAIL_DAILY_CAP" envDefault:"90"`

	// Turnstile (Cloudflare's challenge). Both required when registration is
	// open; the secret is a secret. Without them, register and resend answer
	// 503 and login skips its challenge (the throttle still applies).
	TurnstileSiteKey   string `env:"TURNSTILE_SITE_KEY"`
	TurnstileSecretKey string `env:"TURNSTILE_SECRET_KEY"`

	// Sign in with Google: both or neither; unset means Google is off and
	// its button hidden. The secret is a secret. The redirect URI is
	// {PUBLIC_BASE_URL}/api/v1/auth/google/callback, so PUBLIC_BASE_URL is
	// required with them.
	GoogleClientID     string `env:"GOOGLE_CLIENT_ID"`
	GoogleClientSecret string `env:"GOOGLE_CLIENT_SECRET"`

	// Tests only, never from the environment: where Resend, siteverify and
	// the OpenID provider are; empty means the real services.
	ResendEndpoint     string
	SiteverifyEndpoint string
	GoogleIssuer       string
}

// GoogleEnabled reports whether sign in with Google is configured.
func (a API) GoogleEnabled() bool { return a.GoogleClientID != "" }

// Mail backends.
const (
	MailBackendResend = "resend"
	MailBackendLog    = "log"
)

// MinJWTKeyLen is the shortest JWT_SIGNING_KEY accepted, in bytes.
const MinJWTKeyLen = 32

// MinMediaKeyLen is the shortest MEDIA_SIGNING_KEY accepted, in bytes.
const MinMediaKeyLen = 32

// ValidMediaSigningKey checks MEDIA_SIGNING_KEY: at least MinMediaKeyLen
// bytes of base64 or base64url characters. The character rule is not
// cosmetic: media-delivery writes the key into its nginx config inside a
// quoted string, where a quote, "$", ";" or a newline would change the
// config. docker/nginx-media/05-require-media-signing-key.sh applies the
// same rule before nginx starts.
func ValidMediaSigningKey(key string) error {
	if len(key) < MinMediaKeyLen {
		return fmt.Errorf("MEDIA_SIGNING_KEY must be at least %d bytes, got %d "+
			"(generate one with: openssl rand -base64 48)", MinMediaKeyLen, len(key))
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=_-"
	for _, c := range key {
		if !strings.ContainsRune(alphabet, c) {
			return errors.New("MEDIA_SIGNING_KEY may contain only base64 characters (A-Z a-z 0-9 + / = _ -) " +
				"(generate one with: openssl rand -base64 48)")
		}
	}
	return nil
}

// Validate checks what env tags cannot.
func (a API) Validate() error {
	var errs []error
	if len(a.JWTSigningKey) < MinJWTKeyLen {
		errs = append(errs, fmt.Errorf("JWT_SIGNING_KEY must be at least %d bytes, got %d "+
			"(generate one with: openssl rand -base64 48)", MinJWTKeyLen, len(a.JWTSigningKey)))
	}
	if err := ValidMediaSigningKey(a.MediaSigningKey); err != nil {
		errs = append(errs, err)
	}
	if _, err := clientip.Parse(a.TrustedProxies); err != nil {
		errs = append(errs, err)
	}
	if a.DevMediaURLTTL < 0 {
		errs = append(errs, errors.New("DEV_MEDIA_URL_TTL must be positive"))
	}
	if a.DevMediaURLTTL != 0 && !a.loopbackOnly() {
		errs = append(errs, fmt.Errorf("DEV_MEDIA_URL_TTL is for local development only: "+
			"refusing to start with it while HTTP_ADDR=%q is not a loopback address", a.HTTPAddr))
	}
	return errors.Join(append(errs, a.validateRegistration()...)...)
}

func (a API) validateRegistration() []error {
	var errs []error
	switch a.MailBackend {
	case MailBackendResend:
		if a.ResendAPIKey == "" {
			errs = append(errs, errors.New("RESEND_API_KEY is required when MAIL_BACKEND=resend"))
		}
	case MailBackendLog:
		if !a.loopbackOnly() {
			errs = append(errs, fmt.Errorf("MAIL_BACKEND=log writes mail links to the log, for local development only: "+
				"refusing to start with it while HTTP_ADDR=%q is not a loopback address", a.HTTPAddr))
		}
	default:
		errs = append(errs, fmt.Errorf(`MAIL_BACKEND must be "resend" or "log", got %q`, a.MailBackend))
	}
	if a.RegistrationOpen {
		for name, v := range map[string]string{
			"MAIL_FROM": a.MailFrom, "PUBLIC_BASE_URL": a.PublicBaseURL,
			"TURNSTILE_SITE_KEY": a.TurnstileSiteKey, "TURNSTILE_SECRET_KEY": a.TurnstileSecretKey,
		} {
			if v == "" {
				errs = append(errs, fmt.Errorf("%s is required when REGISTRATION_OPEN=true", name))
			}
		}
	}
	if (a.TurnstileSiteKey == "") != (a.TurnstileSecretKey == "") {
		errs = append(errs, errors.New("TURNSTILE_SITE_KEY and TURNSTILE_SECRET_KEY go together: set both or neither"))
	}
	if a.MailFrom != "" {
		if _, err := mail.ParseAddress(a.MailFrom); err != nil {
			errs = append(errs, fmt.Errorf("MAIL_FROM %q is not a mail address: %w", a.MailFrom, err))
		}
	}
	if a.PublicBaseURL != "" {
		if err := checkBaseURL(a.PublicBaseURL); err != nil {
			errs = append(errs, err)
		}
	}
	if a.MailDailyCap < 1 {
		errs = append(errs, errors.New("MAIL_DAILY_CAP must be at least 1"))
	}
	if (a.GoogleClientID == "") != (a.GoogleClientSecret == "") {
		errs = append(errs, errors.New("GOOGLE_CLIENT_ID and GOOGLE_CLIENT_SECRET go together: set both or neither"))
	}
	if a.GoogleClientID != "" && a.PublicBaseURL == "" {
		errs = append(errs, errors.New("PUBLIC_BASE_URL is required with GOOGLE_CLIENT_ID: "+
			"the redirect URI is {PUBLIC_BASE_URL}/api/v1/auth/google/callback"))
	}
	return errs
}

// checkBaseURL: an origin (scheme and host, no path, query or fragment),
// https unless the host is loopback.
func checkBaseURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		strings.Trim(u.Path, "/") != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return fmt.Errorf("PUBLIC_BASE_URL must be an origin such as https://media.example, got %q", raw)
	}
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return fmt.Errorf("PUBLIC_BASE_URL must use https except on loopback, got %q", raw)
	}
	return nil
}

// BaseURL is PUBLIC_BASE_URL without a trailing slash.
func (a API) BaseURL() string { return strings.TrimRight(a.PublicBaseURL, "/") }

// SecureCookies reports whether cookies need the Secure flag: always, unless
// the api listens on loopback only (local development over plain http).
// Containers listen on ":8080", which is not loopback, so they always set it.
func (a API) SecureCookies() bool { return !a.loopbackOnly() }

// loopbackOnly reports whether HTTPAddr is a loopback address, so nothing
// off this machine can reach the api.
func (a API) loopbackOnly() bool {
	host, _, err := net.SplitHostPort(a.HTTPAddr)
	if err != nil {
		return false
	}
	return loopbackHost(host)
}

func loopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Watcher is the configuration for cmd/watcher.
type Watcher struct {
	Common

	// MediaRoot holds inbox/, sources/ and library/ (see internal/medialayout).
	// Required with no default, like DevIngest: a guess would claim files
	// from the wrong tree.
	MediaRoot string `env:"MEDIA_ROOT,required,notEmpty"`

	// DatabaseURL is a secret (it embeds credentials). No default.
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
}

// Dispatcher is the configuration for cmd/dispatcher.
type Dispatcher struct {
	Common

	// HTTPAddr serves /healthz, /readyz and /metrics. Loopback by default,
	// on its own port so it runs beside a local api; containers set
	// HTTP_ADDR=:8080 explicitly, like the api.
	HTTPAddr        string        `env:"HTTP_ADDR" envDefault:"127.0.0.1:8090"`
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// DatabaseURL is a secret (it embeds credentials). No default.
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`

	// Backend is "local" or "kubernetes". Required, no default: a wrong
	// guess in the cluster would silently start transcoders the wrong way.
	Backend string `env:"DISPATCH_BACKEND,required,notEmpty"`

	// MaxConcurrent caps running jobs across all replicas. One encode
	// already uses every CPU core.
	MaxConcurrent int           `env:"MAX_CONCURRENT" envDefault:"1"`
	PollInterval  time.Duration `env:"POLL_INTERVAL" envDefault:"5s"`
	// JobDeadline: a job running longer is failed and stopped (Kubernetes:
	// also the Job's activeDeadlineSeconds).
	JobDeadline time.Duration `env:"JOB_DEADLINE" envDefault:"6h"`

	// Local backend: the transcoder binary, and MEDIA_ROOT for it (the child
	// inherits this environment).
	TranscoderBin string `env:"TRANSCODER_BIN"`
	MediaRoot     string `env:"MEDIA_ROOT"`

	// Kubernetes backend. DatabaseSecretName/Key name the existing Secret
	// the Job reads DATABASE_URL from; the value itself never enters a Job.
	K8sNamespace       string        `env:"K8S_NAMESPACE"`
	TranscoderImage    string        `env:"TRANSCODER_IMAGE"`
	DatabaseSecretName string        `env:"DATABASE_SECRET_NAME"`
	DatabaseSecretKey  string        `env:"DATABASE_SECRET_KEY"`
	MediaClaim         string        `env:"MEDIA_PVC"`
	MediaMountPath     string        `env:"MEDIA_MOUNT_PATH" envDefault:"/mnt/media"`
	JobTTL             time.Duration `env:"JOB_TTL" envDefault:"24h"`
	RunAsUser          int64         `env:"TRANSCODER_RUN_AS_USER" envDefault:"65532"`
	RunAsGroup         int64         `env:"TRANSCODER_RUN_AS_GROUP" envDefault:"65532"`
	// TranscoderServiceAccount: a dedicated ServiceAccount with no RBAC for
	// the Job's pod (its token is never mounted). Required for kubernetes.
	TranscoderServiceAccount string `env:"TRANSCODER_SERVICE_ACCOUNT"`
	// ImagePullSecret, optional: added to the Job's imagePullSecrets.
	ImagePullSecret string `env:"IMAGE_PULL_SECRET"`
	// Kubernetes quantities, parsed at start-up by the backend. The plan
	// budgets about 2 GB for one transcode.
	TranscoderCPURequest    string `env:"TRANSCODER_CPU_REQUEST" envDefault:"2"`
	TranscoderCPULimit      string `env:"TRANSCODER_CPU_LIMIT" envDefault:"4"`
	TranscoderMemoryRequest string `env:"TRANSCODER_MEMORY_REQUEST" envDefault:"1Gi"`
	TranscoderMemoryLimit   string `env:"TRANSCODER_MEMORY_LIMIT" envDefault:"2Gi"`
}

// Validate checks the rules env tags cannot express: the backend's value
// and the settings each backend needs.
func (d Dispatcher) Validate() error {
	var errs []error
	if d.MaxConcurrent < 1 {
		errs = append(errs, errors.New("MAX_CONCURRENT must be at least 1"))
	}
	if d.PollInterval <= 0 || d.JobDeadline <= 0 {
		errs = append(errs, errors.New("POLL_INTERVAL and JOB_DEADLINE must be positive"))
	}
	need := func(names ...string) {
		for i := 0; i+1 < len(names); i += 2 {
			if names[i+1] == "" {
				errs = append(errs, fmt.Errorf("%s is required when DISPATCH_BACKEND=%s", names[i], d.Backend))
			}
		}
	}
	switch d.Backend {
	case "local":
		need("TRANSCODER_BIN", d.TranscoderBin, "MEDIA_ROOT", d.MediaRoot)
	case "kubernetes":
		need("K8S_NAMESPACE", d.K8sNamespace, "TRANSCODER_IMAGE", d.TranscoderImage,
			"DATABASE_SECRET_NAME", d.DatabaseSecretName, "DATABASE_SECRET_KEY", d.DatabaseSecretKey,
			"MEDIA_PVC", d.MediaClaim, "TRANSCODER_SERVICE_ACCOUNT", d.TranscoderServiceAccount)
	default:
		errs = append(errs, fmt.Errorf(`DISPATCH_BACKEND must be "local" or "kubernetes", got %q`, d.Backend))
	}
	return errors.Join(errs...)
}

// TranscoderJob is the configuration for `transcoder --movie-id`, the mode
// the dispatcher starts. The --input/--output mode needs only Common.
type TranscoderJob struct {
	Common

	// MediaRoot holds sources/ and library/. Required, no default.
	MediaRoot string `env:"MEDIA_ROOT,required,notEmpty"`

	// DatabaseURL is a secret (it embeds credentials). No default.
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`

	// DevMediaURLTTL is the api's DEV_MEDIA_URL_TTL (local development
	// only): signed URLs then live that long, so a retranscode's old version
	// may go that much sooner (mediattl.OldVersionGrace). The local
	// dispatcher passes its environment on, so `make dispatch` picks it up
	// from .env; a Kubernetes Job never gets it (its spec is built in code).
	DevMediaURLTTL time.Duration `env:"DEV_MEDIA_URL_TTL"`
}

// Validate checks what parsing cannot.
func (t TranscoderJob) Validate() error {
	if t.DevMediaURLTTL < 0 {
		return errors.New("DEV_MEDIA_URL_TTL must be positive")
	}
	return nil
}

// UserAdmin is the configuration for cmd/useradmin.
type UserAdmin struct {
	Common

	// DatabaseURL is a secret (it embeds credentials). No default.
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
}

// DevIngest is the configuration for tools/devingest (local development only).
type DevIngest struct {
	Common

	// MediaRoot holds inbox/, sources/ and library/. Not a secret, but required with
	// no default: guessing would write movies somewhere unexpected.
	MediaRoot string `env:"MEDIA_ROOT,required,notEmpty"`

	// DatabaseURL is a secret (it embeds credentials). No default.
	DatabaseURL string `env:"DATABASE_URL,required,notEmpty"`
}

// Load parses environment variables into a T. It returns an error naming every
// missing or malformed variable so the process can exit before doing any work.
func Load[T any]() (T, error) {
	cfg, err := env.ParseAs[T]()
	if err != nil {
		var zero T
		return zero, fmt.Errorf("loading config from environment: %w", err)
	}
	return cfg, nil
}
