// Command dispatcher starts the transcoder for queued transcode jobs and
// watches it until it finishes. It is the only component allowed to create
// Kubernetes Jobs; the api, which faces the internet, has no Kubernetes
// permissions at all.
//
// DISPATCH_BACKEND (required) picks how a transcoder runs:
//
//	local       `$TRANSCODER_BIN --movie-id <id>` as a child process (a laptop)
//	kubernetes  a batch/v1 Job per transcode, in-cluster config
//
// It serves /healthz, /readyz (Postgres) and /metrics on HTTP_ADDR. On
// SIGTERM it stops claiming work and exits; running transcoders are left
// running, and the next dispatcher start adopts them.
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
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/vtnHAN-SOLO/hls-vod/internal/config"
	"github.com/vtnHAN-SOLO/hls-vod/internal/dispatch"
	"github.com/vtnHAN-SOLO/hls-vod/internal/handler"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// startGrace covers the moment between a claim's commit and its process or
// Job existing; see dispatch.Dispatcher.StartGrace.
const startGrace = 2 * time.Minute

// stopGrace is how long a local transcoder gets after SIGTERM.
const stopGrace = 30 * time.Second

func main() {
	os.Exit(realMain())
}

func realMain() int {
	logger := slog.New(slog.NewJSONHandler(os.Stderr, nil))

	cfg, err := config.Load[config.Dispatcher]()
	if err == nil {
		err = cfg.Validate()
	}
	if err != nil {
		logger.Error("startup failed", "err", err)
		return 1
	}
	logger = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: cfg.LogLevel}))
	slog.SetDefault(logger)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.HTTPAddr)
	if err != nil {
		logger.Error("startup failed", "err", fmt.Errorf("listening on %s: %w", cfg.HTTPAddr, err))
		return 1
	}
	if err := run(ctx, cfg, logger, ln); err != nil {
		logger.Error("dispatcher exited with error", "err", err)
		return 1
	}
	return 0
}

func run(ctx context.Context, cfg config.Dispatcher, logger *slog.Logger, ln net.Listener) error {
	db, err := store.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer db.Close()

	backend, local, err := newBackend(cfg, logger)
	if err != nil {
		return err
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	d := &dispatch.Dispatcher{
		Store: db, Backend: backend, Log: logger,
		MaxConcurrent: cfg.MaxConcurrent, Deadline: cfg.JobDeadline, StartGrace: startGrace,
	}
	d.RegisterMetrics(reg)

	srv := &http.Server{
		Handler:           handler.NewProbeRouter(logger, reg, db),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      10 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelError),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	logger.Info("dispatcher started", "backend", cfg.Backend, "addr", ln.Addr().String(),
		"max_concurrent", cfg.MaxConcurrent, "poll_interval", cfg.PollInterval.String(),
		"job_deadline", cfg.JobDeadline.String())
	loopDone := make(chan struct{})
	go func() {
		d.Run(ctx, cfg.PollInterval)
		close(loopDone)
	}()

	select {
	case err := <-serveErr:
		return fmt.Errorf("serving http: %w", err)
	case <-ctx.Done():
	}
	<-loopDone
	if local != nil {
		if pids := local.Active(); len(pids) > 0 {
			logger.Info("transcoders keep running; the next dispatcher start adopts them", "pids", pids)
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutting down http server: %w", err)
	}
	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serving http: %w", err)
	}
	logger.Info("dispatcher stopped cleanly")
	return nil
}

// newBackend builds the configured backend; local is non-nil for the local one.
func newBackend(cfg config.Dispatcher, logger *slog.Logger) (dispatch.Backend, *dispatch.Local, error) {
	switch cfg.Backend {
	case "local":
		l := &dispatch.Local{Bin: cfg.TranscoderBin, Out: os.Stderr, Log: logger, StopGrace: stopGrace}
		return l, l, nil
	case "kubernetes":
		kc := dispatch.KubeConfig{
			Namespace: cfg.K8sNamespace, Image: cfg.TranscoderImage,
			DatabaseSecretName: cfg.DatabaseSecretName, DatabaseSecretKey: cfg.DatabaseSecretKey,
			MediaClaimName: cfg.MediaClaim, MediaMountPath: cfg.MediaMountPath,
			Deadline: cfg.JobDeadline, TTL: cfg.JobTTL, LogLevel: cfg.LogLevel.String(),
			RunAsUser: cfg.RunAsUser, RunAsGroup: cfg.RunAsGroup,
			ServiceAccount: cfg.TranscoderServiceAccount, ImagePullSecret: cfg.ImagePullSecret,
			CPURequest: cfg.TranscoderCPURequest, CPULimit: cfg.TranscoderCPULimit,
			MemoryRequest: cfg.TranscoderMemoryRequest, MemoryLimit: cfg.TranscoderMemoryLimit,
		}
		if err := kc.Validate(); err != nil {
			return nil, nil, fmt.Errorf("kubernetes backend: %w", err)
		}
		rc, err := rest.InClusterConfig()
		if err != nil {
			return nil, nil, fmt.Errorf("kubernetes backend: in-cluster config: %w", err)
		}
		client, err := kubernetes.NewForConfig(rc)
		if err != nil {
			return nil, nil, fmt.Errorf("kubernetes backend: %w", err)
		}
		return &dispatch.Kubernetes{Client: client, Cfg: kc, Log: logger}, nil, nil
	}
	return nil, nil, fmt.Errorf("unknown backend %q", cfg.Backend)
}
