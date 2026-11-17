// Package buildinfo carries the version a binary was built from: the git
// commit, stamped at build time by the Makefile and docker/Dockerfile with
//
//	-ldflags "-X github.com/vtnHAN-SOLO/hls-vod/internal/buildinfo.Version=<sha>"
//
// A plain `go build` or `go run` leaves it at "dev". Every binary logs it at
// start; the long-running services expose it on /metrics as build_info.
package buildinfo

import (
	"log/slog"
	"runtime"

	"github.com/prometheus/client_golang/prometheus"
)

// Version is the git commit the binary was built from, or "dev".
var Version = "dev"

// LogStart writes the one "starting" line every binary begins with.
func LogStart(logger *slog.Logger, binary string) {
	logger.Info("starting", "binary", binary, "version", Version, "go_version", runtime.Version())
}

// Collector is the build_info gauge: always 1, the version as a label, so a
// dashboard or an alert can tell which build each pod runs.
func Collector() prometheus.Collector {
	g := prometheus.NewGauge(prometheus.GaugeOpts{
		Name:        "build_info",
		Help:        "Always 1; the version label is the git commit the binary was built from.",
		ConstLabels: prometheus.Labels{"version": Version},
	})
	g.Set(1)
	return g
}
