// Command api serves the catalog, auth, progress and admin HTTP API.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"

	"github.com/vtnHAN-SOLO/hls-vod/internal/auth"
	"github.com/vtnHAN-SOLO/hls-vod/internal/clientip"
	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/handler"
	"github.com/vtnHAN-SOLO/hls-vod/internal/mail"
	"github.com/vtnHAN-SOLO/hls-vod/internal/service"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
	"github.com/vtnHAN-SOLO/hls-vod/internal/turnstile"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load[config.API]()
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		logger.Error("startup failed", "err", err)
		os.Exit(1)
	}
	logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		logger.Error("startup failed", "err", fmt.Errorf("listening on %s: %w", cfg.HTTPAddr, err))
		os.Exit(1)
	}

	if err := run(ctx, cfg, logger, ln); err != nil {
		logger.Error("api exited with error", "err", err)
		os.Exit(1)
	}
}

// maxConcurrentHashes caps argon2id work per api process.
const maxConcurrentHashes = 2

// run serves HTTP on ln until ctx is cancelled, then drains in-flight requests
// for up to cfg.ShutdownTimeout.
//
// Postgres being down at startup is not fatal: the pool dials lazily, the
// process stays live, and /readyz reports 503 until the database answers.
func run(ctx context.Context, cfg config.API, logger *slog.Logger, ln net.Listener) error {
	db, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	signer, err := auth.NewSigner([]byte(cfg.JWTSigningKey))
	if err != nil {
		return err
	}
	// Two argon2id hashes at once at most: 19 MiB each, and the pod's
	// memory limit is 256Mi. Login, password change and sign-up share them.
	hasher := auth.NewHasher(maxConcurrentHashes)
	authn := service.NewAuth(db, hasher, signer, auth.NewThrottle(), auth.NewThrottle(), logger)

	// Turnstile: without the keys, register and resend answer 503 and
	// login never asks for a challenge.
	var captcha service.Captcha
	if cfg.TurnstileSecretKey != "" {
		v := turnstile.New(cfg.TurnstileSecretKey, cfg.SiteverifyEndpoint, logger)
		captcha = v
		authn.WithCaptcha(v)
	}
	var mailer service.Mailer
	if outbox := newOutbox(ctx, cfg, db, logger); outbox != nil {
		mailer = outbox
		defer func() {
			// After the server drained: send what is queued, for a bounded time.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
			defer cancel()
			if err := outbox.Close(ctx); err != nil {
				logger.Warn("mail queue not drained before shutdown", "err", err)
			}
		}()
	}
	signup := service.NewRegistration(db, hasher, captcha, mailer, authn, service.RegistrationConfig{
		Open: cfg.RegistrationOpen, BaseURL: cfg.BaseURL(), TurnstileSiteKey: cfg.TurnstileSiteKey,
		GoogleEnabled: cfg.GoogleEnabled(),
	}, logger)
	proxies, err := clientip.Parse(cfg.TrustedProxies)
	if err != nil {
		return err
	}
	if !proxies.Trusts() {
		logger.Info("TRUSTED_PROXIES is empty: the client address is the TCP peer, X-Forwarded-For is ignored")
	}
	opts := handler.Options{SecureCookies: cfg.SecureCookies(), Admin: service.NewAdmin(db, logger), ClientIP: proxies}
	if cfg.GoogleEnabled() {
		// Nothing is fetched from Google until the first sign-in.
		opts.Google = service.NewGoogle(ctx, db, authn, mailer, service.GoogleConfig{
			ClientID: cfg.GoogleClientID, ClientSecret: cfg.GoogleClientSecret, Issuer: cfg.GoogleIssuer,
			RedirectURL: cfg.BaseURL() + "/api/v1/auth/google/callback", BaseURL: cfg.BaseURL(),
			RegistrationOpen: cfg.RegistrationOpen,
		}, logger)
	}

	media, err := service.NewMediaSigner(cfg.MediaSigningKey, cfg.DevMediaURLTTL)
	if err != nil {
		return err
	}
	if cfg.DevMediaURLTTL != 0 {
		logger.Warn("DEV_MEDIA_URL_TTL is set: every signed media URL expires early (local development only)",
			"ttl", cfg.DevMediaURLTTL.String())
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)

	srv := &http.Server{
		Handler:           handler.NewRouter(logger, reg, db, service.NewCatalog(db, media), authn, signup, opts),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.Info("api listening", "addr", ln.Addr().String())
		serveErr <- srv.Serve(ln)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serving http: %w", err)
	case <-ctx.Done():
	}

	logger.Info("shutdown signal received, draining", "timeout", cfg.ShutdownTimeout.String())
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving http: %w", err)
	}
	logger.Info("api stopped cleanly")
	return nil
}

// newOutbox starts the mail queue, or returns nil when mail is not
// configured (MAIL_FROM and PUBLIC_BASE_URL are needed for any mail; config
// requires them while registration is open).
func newOutbox(ctx context.Context, cfg config.API, db *store.Store, logger *slog.Logger) *mail.Outbox {
	if cfg.MailFrom == "" || cfg.PublicBaseURL == "" {
		logger.Info("mail not configured: no mail will be sent")
		return nil
	}
	var sender mail.Sender
	switch cfg.MailBackend {
	case config.MailBackendResend:
		sender = mail.NewResend(cfg.ResendAPIKey, cfg.MailFrom, cfg.ResendEndpoint)
	case config.MailBackendLog:
		logger.Warn("MAIL_BACKEND=log: mails, links included, are written to this log and not sent (local development only)")
		sender = mail.NewLog(logger)
	default:
		return nil
	}
	return mail.NewOutbox(ctx, sender, db, cfg.MailDailyCap, logger)
}
