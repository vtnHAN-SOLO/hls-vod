package dispatch

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/vtnHAN-SOLO/hls-vod/internal/model"
	"github.com/vtnHAN-SOLO/hls-vod/internal/pgtest"
	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

func kubeConfig() KubeConfig {
	return KubeConfig{ // #nosec G101 -- names of a Kubernetes Secret and its key, not credential values
		Namespace:          "media",
		Image:              "registry.example/transcoder@sha256:" + strings.Repeat("ab", 32),
		DatabaseSecretName: "postgres-app",
		DatabaseSecretKey:  "database-url",
		MediaClaimName:     "media",
		MediaMountPath:     "/mnt/media",
		Deadline:           6 * time.Hour,
		TTL:                24 * time.Hour,
		LogLevel:           "info",
		RunAsUser:          65532,
		RunAsGroup:         65532,
		ServiceAccount:     "transcoder",
		ImagePullSecret:    "ghcr-pull",
		CPURequest:         "2",
		CPULimit:           "4",
		MemoryRequest:      "1Gi",
		MemoryLimit:        "2Gi",
	}
}

func newKube(t *testing.T, objs ...*batchv1.Job) (*Kubernetes, *fake.Clientset) {
	t.Helper()
	client := fake.NewClientset()
	for _, o := range objs {
		if _, err := client.BatchV1().Jobs(o.Namespace).Create(t.Context(), o, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	client.ClearActions()
	return &Kubernetes{Client: client, Cfg: kubeConfig(), Log: slog.New(slog.DiscardHandler)}, client
}

func runningJob() store.RunningJob {
	id := model.NewID()
	name := jobPrefix + id
	return store.RunningJob{ID: id, MovieID: model.NewID(), K8sJobName: &name, StartedAt: time.Now()}
}

func TestKubeConfigValidate(t *testing.T) {
	if err := kubeConfig().Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*KubeConfig){
		"image by tag only":    func(c *KubeConfig) { c.Image = "registry.example/transcoder:1.2.3" },
		"no secret name":       func(c *KubeConfig) { c.DatabaseSecretName = "" },
		"no secret key":        func(c *KubeConfig) { c.DatabaseSecretKey = "" },
		"no media claim":       func(c *KubeConfig) { c.MediaClaimName = "" },
		"root user":            func(c *KubeConfig) { c.RunAsUser = 0 },
		"no deadline":          func(c *KubeConfig) { c.Deadline = 0 },
		"TTL beyond int32":     func(c *KubeConfig) { c.TTL = 100 * 365 * 24 * time.Hour },
		"no namespace":         func(c *KubeConfig) { c.Namespace = "" },
		"no media mount path":  func(c *KubeConfig) { c.MediaMountPath = "" },
		"no service account":   func(c *KubeConfig) { c.ServiceAccount = "" },
		"cpu not a quantity":   func(c *KubeConfig) { c.CPURequest = "two" },
		"memory in GB":         func(c *KubeConfig) { c.MemoryLimit = "2GB" },
		"empty memory":         func(c *KubeConfig) { c.MemoryRequest = "" },
		"negative cpu limit":   func(c *KubeConfig) { c.CPULimit = "-1" },
		"zero memory limit":    func(c *KubeConfig) { c.MemoryLimit = "0" },
		"zero cpu request":     func(c *KubeConfig) { c.CPURequest = "0" },
		"negative mem request": func(c *KubeConfig) { c.MemoryRequest = "-1Gi" },
		"cpu request > limit":  func(c *KubeConfig) { c.CPURequest = "8" },
		"mem request > limit":  func(c *KubeConfig) { c.MemoryRequest = "3Gi" },
	} {
		c := kubeConfig()
		mutate(&c)
		if c.Validate() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestKubernetesJobSpec(t *testing.T) {
	// The dispatcher's own DATABASE_URL must never be copied into a Job.
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	t.Setenv("DATABASE_URL", "postgres://dispatcher:"+hex.EncodeToString(secret)+"@db/media")

	k, client := newKube(t)
	j := runningJob()
	if _, err := k.Start(t.Context(), j); err != nil {
		t.Fatal(err)
	}
	got, err := client.BatchV1().Jobs("media").Get(t.Context(), *j.K8sJobName, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	spec := got.Spec
	pod := spec.Template.Spec

	checks := map[string]bool{
		"labels app=transcoder + movie-id": got.Labels[appLabel] == appValue && got.Labels[movieIDLabel] == j.MovieID &&
			spec.Template.Labels[appLabel] == appValue,
		"backoffLimit 0":                *spec.BackoffLimit == 0,
		"activeDeadlineSeconds 6h":      *spec.ActiveDeadlineSeconds == 6*3600,
		"ttlSecondsAfterFinished 1 day": *spec.TTLSecondsAfterFinished == 24*3600,
		"restartPolicy Never":           pod.RestartPolicy == corev1.RestartPolicyNever,
		"no service account token":      pod.AutomountServiceAccountToken != nil && !*pod.AutomountServiceAccountToken,
		"pod runAsNonRoot":              *pod.SecurityContext.RunAsNonRoot && *pod.SecurityContext.RunAsUser == 65532,
		"seccomp RuntimeDefault":        pod.SecurityContext.SeccompProfile.Type == corev1.SeccompProfileTypeRuntimeDefault,
		"one container":                 len(pod.Containers) == 1,
	}
	c := pod.Containers[0]
	sc := c.SecurityContext
	checks["no privilege escalation"] = !*sc.AllowPrivilegeEscalation
	checks["read-only root filesystem"] = *sc.ReadOnlyRootFilesystem
	checks["all capabilities dropped"] = slices.Equal(sc.Capabilities.Drop, []corev1.Capability{"ALL"}) && len(sc.Capabilities.Add) == 0
	checks["image pinned by digest"] = c.Image == kubeConfig().Image
	checks["args --movie-id"] = slices.Equal(c.Args, []string{"--movie-id", j.MovieID})
	checks["media PVC mounted at MEDIA_ROOT"] = pod.Volumes[0].PersistentVolumeClaim.ClaimName == "media" &&
		c.VolumeMounts[0].MountPath == "/mnt/media"
	checks["no hostPath"] = !slices.ContainsFunc(pod.Volumes, func(v corev1.Volume) bool { return v.HostPath != nil })
	checks["dedicated service account"] = pod.ServiceAccountName == "transcoder"
	checks["image pull secret"] = slices.Equal(pod.ImagePullSecrets, []corev1.LocalObjectReference{{Name: "ghcr-pull"}})
	req, lim := c.Resources.Requests, c.Resources.Limits
	checks["cpu 2 / 4"] = req.Cpu().Cmp(resource.MustParse("2")) == 0 && lim.Cpu().Cmp(resource.MustParse("4")) == 0
	checks["memory 1Gi / 2Gi"] = req.Memory().Cmp(resource.MustParse("1Gi")) == 0 && lim.Memory().Cmp(resource.MustParse("2Gi")) == 0
	for what, ok := range checks {
		if !ok {
			t.Errorf("Job spec: %s", what)
		}
	}

	env := map[string]corev1.EnvVar{}
	for _, e := range c.Env {
		env[e.Name] = e
	}
	db := env["DATABASE_URL"]
	if db.Value != "" || db.ValueFrom == nil || db.ValueFrom.SecretKeyRef == nil ||
		db.ValueFrom.SecretKeyRef.Name != "postgres-app" || db.ValueFrom.SecretKeyRef.Key != "database-url" {
		t.Fatalf("DATABASE_URL = %+v, want only a secretKeyRef", db)
	}
	if env["MEDIA_ROOT"].Value != "/mnt/media" {
		t.Errorf("MEDIA_ROOT = %+v", env["MEDIA_ROOT"])
	}
	raw, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), hex.EncodeToString(secret)) || strings.Contains(string(raw), "postgres://") {
		t.Fatal("a database URL value ended up in the Job spec")
	}

	// A retried start under the same name is not a second Job.
	if _, err := k.Start(t.Context(), j); err != nil {
		t.Fatalf("second Start: %v, want AlreadyExists treated as started", err)
	}
	list, _ := client.BatchV1().Jobs("media").List(t.Context(), metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatalf("%d Jobs, want 1", len(list.Items))
	}
}

// IMAGE_PULL_SECRET is optional: unset means no imagePullSecrets at all.
func TestKubernetesJobSpecWithoutPullSecret(t *testing.T) {
	k, _ := newKube(t)
	k.Cfg.ImagePullSecret = ""
	spec, err := k.jobSpec(runningJob())
	if err != nil {
		t.Fatal(err)
	}
	if ps := spec.Spec.Template.Spec.ImagePullSecrets; len(ps) != 0 {
		t.Fatalf("imagePullSecrets = %v, want none", ps)
	}
}

// A config that skipped Validate still cannot produce a Job with bad quantities.
func TestKubernetesStartRejectsBadQuantities(t *testing.T) {
	k, client := newKube(t)
	k.Cfg.MemoryLimit = "lots"
	if _, err := k.Start(t.Context(), runningJob()); err == nil {
		t.Fatal("Start with an invalid quantity succeeded")
	}
	if list, _ := client.BatchV1().Jobs("media").List(t.Context(), metav1.ListOptions{}); len(list.Items) != 0 {
		t.Fatal("a Job was created")
	}
}

func jobWith(name string, labels map[string]string, conds ...batchv1.JobCondition) *batchv1.Job {
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "media", Labels: labels},
		Status:     batchv1.JobStatus{Conditions: conds},
	}
}

func TestKubernetesStatus(t *testing.T) {
	ours := map[string]string{appLabel: appValue}
	running, complete, deadline, failedFalse := runningJob(), runningJob(), runningJob(), runningJob()
	k, _ := newKube(t,
		jobWith(*running.K8sJobName, ours),
		jobWith(*complete.K8sJobName, ours, batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}),
		jobWith(*deadline.K8sJobName, ours, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
			Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"}),
		jobWith(*failedFalse.K8sJobName, ours, batchv1.JobCondition{Type: batchv1.JobFailed, Status: corev1.ConditionFalse}),
	)
	noName := runningJob()
	noName.K8sJobName = nil
	tests := []struct {
		name   string
		job    store.RunningJob
		want   State
		detail string
		grace  bool
	}{
		{"active", running, Running, "", false},
		{"complete", complete, Exited, "Job completed", false},
		{"past activeDeadlineSeconds", deadline, Exited, "Job failed: DeadlineExceeded", false},
		{"a false condition is not an outcome", failedFalse, Running, "", false},
		{"not created (yet)", runningJob(), Missing, "not found", true},
		{"claimed by the local backend", noName, Missing, "no k8s_job_name", false},
	}
	for _, tt := range tests {
		st, err := k.Status(t.Context(), tt.job)
		if err != nil || st.State != tt.want || !strings.Contains(st.Detail, tt.detail) || st.MayStillStart != tt.grace {
			t.Errorf("%s: Status = %+v, %v; want state %d, detail %q, grace %v", tt.name, st, err, tt.want, tt.detail, tt.grace)
		}
	}
}

func TestKubernetesStopAndSweep(t *testing.T) {
	ours := map[string]string{appLabel: appValue}
	known, orphan, stuck := runningJob(), runningJob(), runningJob()
	unrelated := jobWith("backup-nightly", map[string]string{appLabel: "backup"})
	k, client := newKube(t,
		jobWith(*known.K8sJobName, ours), jobWith(*orphan.K8sJobName, ours), jobWith(*stuck.K8sJobName, ours), unrelated)

	if err := k.Stop(t.Context(), stuck); err != nil {
		t.Fatal(err)
	}
	var deletes []k8stesting.DeleteAction
	for _, a := range client.Actions() {
		if d, ok := a.(k8stesting.DeleteAction); ok {
			deletes = append(deletes, d)
		}
	}
	if len(deletes) != 1 || deletes[0].GetName() != *stuck.K8sJobName ||
		deletes[0].GetDeleteOptions().PropagationPolicy == nil ||
		*deletes[0].GetDeleteOptions().PropagationPolicy != metav1.DeletePropagationBackground {
		t.Fatalf("Stop deletes = %+v, want the Job with background propagation (its pod too)", deletes)
	}

	isKnown := func(_ context.Context, names []string) (map[string]bool, error) {
		m := map[string]bool{}
		for _, n := range names {
			m[n] = n == *known.K8sJobName
		}
		return m, nil
	}
	deleted, err := k.Sweep(t.Context(), isKnown)
	if err != nil || !slices.Equal(deleted, []string{*orphan.K8sJobName}) {
		t.Fatalf("Sweep deleted %v, %v; want only the orphan", deleted, err)
	}
	list, _ := client.BatchV1().Jobs("media").List(t.Context(), metav1.ListOptions{})
	var left []string
	for _, j := range list.Items {
		left = append(left, j.Name)
	}
	slices.Sort(left)
	want := []string{"backup-nightly", *known.K8sJobName}
	slices.Sort(want)
	if !slices.Equal(left, want) {
		t.Fatalf("Jobs left = %v, want %v (unlabelled Jobs are never touched)", left, want)
	}
}

// The dispatcher's states with the Kubernetes backend, against a real
// Postgres and the fake API server.
func TestDispatcherKubernetes(t *testing.T) {
	db := pgtest.Start(t)
	if _, err := db.Migrator(t).Up(t.Context()); err != nil {
		t.Fatalf("migrating: %v", err)
	}
	st, err := store.New(t.Context(), db.DSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	quiet := slog.New(slog.DiscardHandler)
	reset := func(t *testing.T) {
		t.Helper()
		if _, err := db.Pool.Exec(t.Context(), `TRUNCATE movies CASCADE`); err != nil {
			t.Fatal(err)
		}
	}
	queue := func(t *testing.T) string {
		t.Helper()
		id := model.NewID()
		if err := st.EnqueueMovie(t.Context(), store.PendingMovie{ID: id, Title: "T", SourcePath: "sources/" + id + ".mkv"},
			func() error { return nil }); err != nil {
			t.Fatal(err)
		}
		return id
	}
	dispatcher := func(k *Kubernetes) *Dispatcher {
		return &Dispatcher{Store: st, Backend: k, Log: quiet, MaxConcurrent: 1, Deadline: 6 * time.Hour, StartGrace: time.Minute}
	}
	jobRow := func(t *testing.T, movieID string) (status, name, errMsg string) {
		t.Helper()
		var n, m *string
		if err := db.Pool.QueryRow(t.Context(),
			`SELECT status, k8s_job_name, error_msg FROM transcode_jobs WHERE movie_id = $1`, movieID).Scan(&status, &n, &m); err != nil {
			t.Fatal(err)
		}
		if n != nil {
			name = *n
		}
		if m != nil {
			errMsg = *m
		}
		return status, name, errMsg
	}
	creates := func(c *fake.Clientset) int {
		n := 0
		for _, a := range c.Actions() {
			if a.GetVerb() == "create" && a.GetResource().Resource == "jobs" {
				n++
			}
		}
		return n
	}

	t.Run("queued to running: the Job is created under the recorded name", func(t *testing.T) {
		reset(t)
		m := queue(t)
		k, client := newKube(t)
		if err := dispatcher(k).Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		status, name, _ := jobRow(t, m)
		if status != "running" || !strings.HasPrefix(name, jobPrefix) {
			t.Fatalf("row = %s %q", status, name)
		}
		got, err := client.BatchV1().Jobs("media").Get(t.Context(), name, metav1.GetOptions{})
		if err != nil || got.Labels[movieIDLabel] != m {
			t.Fatalf("Job = %+v, %v", got, err)
		}
	})

	t.Run("restart: the Job exists, so no duplicate is created", func(t *testing.T) {
		reset(t)
		m := queue(t)
		k, client := newKube(t)
		if err := dispatcher(k).Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		// A new dispatcher process against the same cluster.
		again := &Kubernetes{Client: client, Cfg: kubeConfig(), Log: quiet}
		for range 2 {
			if err := dispatcher(again).Tick(t.Context()); err != nil {
				t.Fatal(err)
			}
		}
		if status, _, _ := jobRow(t, m); status != "running" || creates(client) != 1 {
			t.Fatalf("row %s, %d creates; want still running and one Job", status, creates(client))
		}
	})

	t.Run("stuck: activeDeadlineSeconds fails the Job, the row follows", func(t *testing.T) {
		reset(t)
		m := queue(t)
		k, client := newKube(t)
		d := dispatcher(k)
		if err := d.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		_, name, _ := jobRow(t, m)
		j, _ := client.BatchV1().Jobs("media").Get(t.Context(), name, metav1.GetOptions{})
		j.Status.Conditions = []batchv1.JobCondition{{Type: batchv1.JobFailed, Status: corev1.ConditionTrue,
			Reason: "DeadlineExceeded", Message: "Job was active longer than specified deadline"}}
		if _, err := client.BatchV1().Jobs("media").UpdateStatus(t.Context(), j, metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := d.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		status, _, msg := jobRow(t, m)
		if status != "failed" || !strings.Contains(msg, "DeadlineExceeded") {
			t.Fatalf("row = %s %q", status, msg)
		}
	})

	t.Run("orphaned Jobs with no row are deleted; known ones kept", func(t *testing.T) {
		reset(t)
		m := queue(t)
		orphan := runningJob()
		k, client := newKube(t, jobWith(*orphan.K8sJobName, map[string]string{appLabel: appValue}))
		if err := dispatcher(k).Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		_, name, _ := jobRow(t, m)
		list, _ := client.BatchV1().Jobs("media").List(t.Context(), metav1.ListOptions{})
		if len(list.Items) != 1 || list.Items[0].Name != name {
			t.Fatalf("Jobs = %v, want only %s", list.Items, name)
		}
	})

	t.Run("Job never appeared: after StartGrace the row is failed", func(t *testing.T) {
		reset(t)
		m := queue(t)
		if _, ok, err := st.ClaimJob(t.Context(), 5, (&Kubernetes{}).JobName()); !ok || err != nil {
			t.Fatal(ok, err)
		}
		k, _ := newKube(t)
		d := dispatcher(k)
		d.MaxConcurrent = 0 // only reconcile
		if err := d.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if status, _, _ := jobRow(t, m); status != "running" {
			t.Fatalf("failed inside StartGrace: %s", status)
		}
		d.Now = func() time.Time { return time.Now().Add(2 * time.Minute) }
		if err := d.Tick(t.Context()); err != nil {
			t.Fatal(err)
		}
		if status, _, msg := jobRow(t, m); status != "failed" || !strings.Contains(msg, "not found") {
			t.Fatalf("row = %s %q", status, msg)
		}
	})
}
