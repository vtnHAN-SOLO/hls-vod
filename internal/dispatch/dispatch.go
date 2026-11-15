// Package dispatch turns queued transcode jobs into running transcoders and
// watches them until they finish.
//
// Every tick (and once at start-up, which is what makes a restart safe):
//
//  1. reconcile: each running job is checked against the backend's view of
//     reality. A transcoder that exited without recording a result, one
//     that is gone, or one past its deadline is marked failed (job and
//     movie); a deadline also stops it. A job whose transcoder is still
//     running is left alone, never started a second time.
//  2. sweep (Kubernetes): Jobs labelled app=transcoder that no database row
//     knows are deleted.
//  3. fill: queued jobs are claimed, oldest first, until MAX_CONCURRENT are
//     running, and their transcoder is started.
//
// The transcoder records success and most failures itself; the dispatcher
// only fills in when it cannot. Every write is guarded on the job still being
// running, so whoever reports first wins.
package dispatch

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// State is a started transcoder as its backend sees it.
type State int

const (
	// Running means the process or Kubernetes Job is still going.
	Running State = iota
	// Exited means it finished (Detail says how). If its row is still
	// running, it did not record a result.
	Exited
	// Missing means no process or Job can be found for the row.
	Missing
)

// Status is a backend's answer for one job.
type Status struct {
	State  State
	Detail string // e.g. "exit status 1", "Job failed: DeadlineExceeded"
	// MayStillStart, for Missing, means the transcoder may simply not exist
	// yet (no pid recorded, no Job created yet): the dispatcher then waits
	// StartGrace before failing the job. A recorded pid that is gone is
	// definite and fails at once.
	MayStillStart bool
}

// Backend starts and observes transcoders. Implementations: Local (a child
// process) and Kubernetes (a batch/v1 Job).
type Backend interface {
	// JobName is the k8s_job_name to record when a job is claimed, or nil
	// for a backend that has none.
	JobName() func(jobID string) string
	// Start launches the transcoder for a claimed job. It returns the
	// local pid, or 0 when there is none to record.
	Start(ctx context.Context, job store.RunningJob) (pid int, err error)
	Status(ctx context.Context, job store.RunningJob) (Status, error)
	// Stop ends a transcoder that ran past its deadline.
	Stop(ctx context.Context, job store.RunningJob) error
}

// Sweeper is a Backend that can find its own launches with no database row
// (Kubernetes Jobs, by label) and delete them.
type Sweeper interface {
	Sweep(ctx context.Context, known func(ctx context.Context, names []string) (map[string]bool, error)) (deleted []string, err error)
}

// Store is the slice of the store the dispatcher needs.
type Store interface {
	ClaimJob(ctx context.Context, maxRunning int, jobName func(jobID string) string) (store.RunningJob, bool, error)
	SetJobPID(ctx context.Context, jobID string, pid int) error
	RunningJobs(ctx context.Context) ([]store.RunningJob, error)
	FailJob(ctx context.Context, jobID, msg string) (bool, error)
	KnownJobNames(ctx context.Context, names []string) (map[string]bool, error)
}

// Dispatcher runs the loop. All fields are required except Now and metrics.
type Dispatcher struct {
	Store         Store
	Backend       Backend
	Log           *slog.Logger
	MaxConcurrent int
	// Deadline: a job running longer is failed and stopped. Matches the
	// Kubernetes Job's activeDeadlineSeconds.
	Deadline time.Duration
	// StartGrace: a running row whose transcoder may not exist yet (see
	// Status.MayStillStart) is given this long before it counts as gone. Covers the moment between
	// the claim's commit and the Job or process appearing, on this replica
	// or another one.
	StartGrace time.Duration
	Now        func() time.Time

	metrics *metrics
}

type metrics struct {
	started  prometheus.Counter
	failed   *prometheus.CounterVec
	orphans  prometheus.Counter
	errors   prometheus.Counter
	running  prometheus.Gauge
	lastTick prometheus.Gauge
}

// RegisterMetrics adds the dispatcher's metrics to reg.
func (d *Dispatcher) RegisterMetrics(reg prometheus.Registerer) {
	d.metrics = &metrics{
		started: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dispatcher_jobs_started_total", Help: "Transcoders started."}),
		failed: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "dispatcher_jobs_failed_total", Help: "Jobs the dispatcher marked failed, by reason."}, []string{"reason"}),
		orphans: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dispatcher_orphans_deleted_total", Help: "Kubernetes Jobs deleted for having no database row."}),
		errors: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "dispatcher_tick_errors_total", Help: "Errors while reconciling, sweeping or starting jobs."}),
		running: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dispatcher_running_jobs", Help: "Jobs in status running at the last tick, across all replicas."}),
		lastTick: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "dispatcher_last_tick_timestamp_seconds", Help: "When the last tick finished."}),
	}
	reg.MustRegister(d.metrics.started, d.metrics.failed, d.metrics.orphans, d.metrics.errors,
		d.metrics.running, d.metrics.lastTick)
}

func (d *Dispatcher) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// Run ticks immediately (the restart reconcile) and then every poll until
// ctx is cancelled. A failing tick is logged and retried on the next one.
func (d *Dispatcher) Run(ctx context.Context, poll time.Duration) {
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		if err := d.Tick(ctx); err != nil && ctx.Err() == nil {
			d.Log.Error("tick failed; retrying at the next one", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick runs one reconcile, sweep and fill. It returns the first error that
// stopped a step; per-job problems are logged and do not stop the others.
func (d *Dispatcher) Tick(ctx context.Context) error {
	defer func() {
		if d.metrics != nil {
			d.metrics.lastTick.SetToCurrentTime()
		}
	}()
	if err := d.reconcile(ctx); err != nil {
		d.countError()
		return fmt.Errorf("reconciling running jobs: %w", err)
	}
	if sw, ok := d.Backend.(Sweeper); ok {
		deleted, err := sw.Sweep(ctx, d.Store.KnownJobNames)
		for _, name := range deleted {
			d.Log.Warn("deleted a Kubernetes Job with no database row", "k8s_job_name", name)
			if d.metrics != nil {
				d.metrics.orphans.Inc()
			}
		}
		if err != nil {
			d.countError()
			d.Log.Error("sweeping orphaned Jobs failed", "err", err)
		}
	}
	return d.fill(ctx)
}

func (d *Dispatcher) reconcile(ctx context.Context) error {
	jobs, err := d.Store.RunningJobs(ctx)
	if err != nil {
		return err
	}
	if d.metrics != nil {
		d.metrics.running.Set(float64(len(jobs)))
	}
	for _, j := range jobs {
		log := d.Log.With("job_id", j.ID, "movie_id", j.MovieID)
		st, err := d.Backend.Status(ctx, j)
		if err != nil {
			d.countError()
			log.Error("checking the transcoder failed", "err", err)
			continue
		}
		age := d.now().Sub(j.StartedAt)
		switch st.State {
		case Running:
			if age <= d.Deadline {
				continue
			}
			// Record first, then stop: the transcoder's own "interrupted"
			// report then finds the job already failed and changes nothing.
			msg := fmt.Sprintf("deadline exceeded: running for %s, limit %s", age.Round(time.Second), d.Deadline)
			d.fail(ctx, log, j, "deadline", msg)
			if err := d.Backend.Stop(ctx, j); err != nil {
				d.countError()
				log.Error("stopping the transcoder failed", "err", err)
			}
		case Exited:
			d.fail(ctx, log, j, "exited", "transcoder ended without recording a result: "+st.Detail)
		case Missing:
			if st.MayStillStart && age < d.StartGrace {
				continue
			}
			d.fail(ctx, log, j, "missing", "transcoder is gone: "+st.Detail)
		}
	}
	return nil
}

func (d *Dispatcher) fail(ctx context.Context, log *slog.Logger, j store.RunningJob, reason, msg string) {
	ok, err := d.Store.FailJob(ctx, j.ID, msg)
	switch {
	case err != nil:
		d.countError()
		log.Error("marking the job failed", "reason", msg, "err", err)
	case ok:
		log.Error("job failed", "reason", msg)
		if d.metrics != nil {
			d.metrics.failed.WithLabelValues(reason).Inc()
		}
	}
	// !ok: the transcoder reported first. Nothing to do.
}

func (d *Dispatcher) fill(ctx context.Context) error {
	for {
		j, ok, err := d.Store.ClaimJob(ctx, d.MaxConcurrent, d.Backend.JobName())
		if err != nil {
			d.countError()
			return fmt.Errorf("claiming a queued job: %w", err)
		}
		if !ok {
			return nil
		}
		log := d.Log.With("job_id", j.ID, "movie_id", j.MovieID)
		pid, err := d.Backend.Start(ctx, j)
		if err != nil {
			d.fail(ctx, log, j, "start", "starting the transcoder: "+err.Error())
			continue
		}
		attrs := []any{}
		if j.K8sJobName != nil {
			attrs = append(attrs, "k8s_job_name", *j.K8sJobName)
		}
		if pid > 0 {
			attrs = append(attrs, "pid", pid)
			if err := d.Store.SetJobPID(ctx, j.ID, pid); err != nil {
				d.countError()
				log.Error("recording the pid failed; a restart will not find this process", "pid", pid, "err", err)
			}
		}
		if d.metrics != nil {
			d.metrics.started.Inc()
		}
		log.Info("job running", attrs...)
	}
}

func (d *Dispatcher) countError() {
	if d.metrics != nil {
		d.metrics.errors.Inc()
	}
}
