package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/vtnHAN-SOLO/hls-vod/internal/store"
)

// Labels on every transcoder Job and its pod. Sweep finds Jobs by appLabel.
const (
	appLabel     = "app"
	appValue     = "transcoder"
	movieIDLabel = "movie-id"
	jobPrefix    = "transcode-"
)

// KubeConfig is how transcoder Jobs are built. There is deliberately no
// field for a database URL: DATABASE_URL reaches the Job only as a
// secretKeyRef to an existing Secret, never as a value in the Job spec.
type KubeConfig struct {
	Namespace string
	// Image is the transcoder image, pinned by digest (name@sha256:…).
	Image string
	// DatabaseSecretName / DatabaseSecretKey: the Secret holding DATABASE_URL.
	DatabaseSecretName string
	DatabaseSecretKey  string
	// MediaClaimName is the PersistentVolumeClaim with the media tree,
	// mounted read-write at MediaMountPath (the transcoder's MEDIA_ROOT).
	MediaClaimName string
	MediaMountPath string
	// Deadline becomes activeDeadlineSeconds; TTL ttlSecondsAfterFinished.
	Deadline time.Duration
	TTL      time.Duration
	LogLevel string
	// RunAsUser / RunAsGroup: a non-root uid and gid for the pod.
	RunAsUser  int64
	RunAsGroup int64
	// ServiceAccount is the pod's own ServiceAccount: dedicated, with no
	// RBAC bindings. Its token is never mounted either way.
	ServiceAccount string
	// ImagePullSecret, if set, is added to imagePullSecrets (private GHCR images).
	ImagePullSecret string
	// CPU and memory requests and limits, as Kubernetes quantities ("2",
	// "500m", "1Gi"). Parsed by Validate, so a typo fails at start-up.
	CPURequest, CPULimit       string
	MemoryRequest, MemoryLimit string
}

// Validate reports every missing or unsafe field.
func (c KubeConfig) Validate() error {
	var errs []error
	for name, v := range map[string]string{
		"namespace": c.Namespace, "image": c.Image, "database secret name": c.DatabaseSecretName,
		"database secret key": c.DatabaseSecretKey, "media claim name": c.MediaClaimName,
		"media mount path": c.MediaMountPath,
	} {
		if v == "" {
			errs = append(errs, fmt.Errorf("%s is required", name))
		}
	}
	if c.Image != "" && !strings.Contains(c.Image, "@sha256:") {
		errs = append(errs, fmt.Errorf("image %q must be pinned by digest (name@sha256:…)", c.Image))
	}
	if c.Deadline <= 0 || c.TTL <= 0 {
		errs = append(errs, errors.New("deadline and TTL must be positive"))
	}
	if c.TTL > maxTTL {
		errs = append(errs, fmt.Errorf("TTL must be at most %s", maxTTL))
	}
	if c.RunAsUser <= 0 || c.RunAsGroup <= 0 {
		errs = append(errs, errors.New("run-as user and group must be non-root (> 0)"))
	}
	if c.ServiceAccount == "" {
		errs = append(errs, errors.New("service account is required"))
	}
	if _, err := c.resources(); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

// resources parses the quantities and checks each request is at most its limit.
func (c KubeConfig) resources() (corev1.ResourceRequirements, error) {
	var errs []error
	parse := func(name, v string) resource.Quantity {
		q, err := resource.ParseQuantity(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s %q: %w", name, v, err))
		} else if q.Sign() <= 0 {
			errs = append(errs, fmt.Errorf("%s %q must be positive", name, v))
		}
		return q
	}
	cpuReq, cpuLim := parse("cpu request", c.CPURequest), parse("cpu limit", c.CPULimit)
	memReq, memLim := parse("memory request", c.MemoryRequest), parse("memory limit", c.MemoryLimit)
	if len(errs) == 0 {
		if cpuReq.Cmp(cpuLim) > 0 {
			errs = append(errs, fmt.Errorf("cpu request %s exceeds its limit %s", c.CPURequest, c.CPULimit))
		}
		if memReq.Cmp(memLim) > 0 {
			errs = append(errs, fmt.Errorf("memory request %s exceeds its limit %s", c.MemoryRequest, c.MemoryLimit))
		}
	}
	if len(errs) > 0 {
		return corev1.ResourceRequirements{}, errors.Join(errs...)
	}
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{corev1.ResourceCPU: cpuReq, corev1.ResourceMemory: memReq},
		Limits:   corev1.ResourceList{corev1.ResourceCPU: cpuLim, corev1.ResourceMemory: memLim},
	}, nil
}

// Kubernetes runs each transcode as a batch/v1 Job. The dispatcher's
// ServiceAccount needs create, get, list and delete on jobs in Namespace,
// and nothing else; the api has no Kubernetes permissions at all.
type Kubernetes struct {
	Client kubernetes.Interface
	Cfg    KubeConfig
	Log    *slog.Logger
}

// JobName implements Backend: the Job is named after the job row, so the
// name is known (and recorded) before the Job exists.
func (k *Kubernetes) JobName() func(string) string {
	return func(jobID string) string { return jobPrefix + jobID }
}

// Start implements Backend. A Job that already exists under the recorded
// name (a retried start) counts as started, never as a second Job.
func (k *Kubernetes) Start(ctx context.Context, job store.RunningJob) (int, error) {
	if job.K8sJobName == nil {
		return 0, errors.New("no k8s_job_name recorded for the job")
	}
	spec, err := k.jobSpec(job)
	if err != nil {
		return 0, err
	}
	_, err = k.Client.BatchV1().Jobs(k.Cfg.Namespace).Create(ctx, spec, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("creating Job %s: %w", *job.K8sJobName, err)
	}
	return 0, nil
}

// Status implements Backend.
func (k *Kubernetes) Status(ctx context.Context, job store.RunningJob) (Status, error) {
	if job.K8sJobName == nil {
		return Status{State: Missing, Detail: "no k8s_job_name was recorded"}, nil
	}
	j, err := k.Client.BatchV1().Jobs(k.Cfg.Namespace).Get(ctx, *job.K8sJobName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// Possibly not created yet (another replica between claim and
		// create), so StartGrace applies.
		return Status{State: Missing, Detail: "Job " + *job.K8sJobName + " not found", MayStillStart: true}, nil
	}
	if err != nil {
		return Status{}, fmt.Errorf("getting Job %s: %w", *job.K8sJobName, err)
	}
	for _, c := range j.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobFailed:
			// activeDeadlineSeconds ends here with reason DeadlineExceeded.
			return Status{State: Exited, Detail: "Job failed: " + c.Reason + ": " + c.Message}, nil
		case batchv1.JobComplete:
			return Status{State: Exited, Detail: "Job completed"}, nil
		}
	}
	return Status{State: Running}, nil
}

// Stop implements Backend: deletes the Job and, in the background, its pod.
func (k *Kubernetes) Stop(ctx context.Context, job store.RunningJob) error {
	if job.K8sJobName == nil {
		return nil
	}
	return k.delete(ctx, *job.K8sJobName)
}

// Sweep implements Sweeper: every Job labelled app=transcoder whose name no
// transcode_jobs row records is deleted.
func (k *Kubernetes) Sweep(ctx context.Context, known func(context.Context, []string) (map[string]bool, error)) ([]string, error) {
	list, err := k.Client.BatchV1().Jobs(k.Cfg.Namespace).List(ctx, metav1.ListOptions{LabelSelector: appLabel + "=" + appValue})
	if err != nil {
		return nil, fmt.Errorf("listing transcoder Jobs: %w", err)
	}
	names := make([]string, len(list.Items))
	for i, j := range list.Items {
		names[i] = j.Name
	}
	ok, err := known(ctx, names)
	if err != nil {
		return nil, err
	}
	var deleted []string
	var errs []error
	for _, n := range names {
		if ok[n] {
			continue
		}
		if err := k.delete(ctx, n); err != nil {
			errs = append(errs, err)
			continue
		}
		deleted = append(deleted, n)
	}
	return deleted, errors.Join(errs...)
}

func (k *Kubernetes) delete(ctx context.Context, name string) error {
	bg := metav1.DeletePropagationBackground
	err := k.Client.BatchV1().Jobs(k.Cfg.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &bg})
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("deleting Job %s: %w", name, err)
	}
	return nil
}

// jobSpec is the Job for one transcode. The pod meets Pod Security
// Standards "restricted": non-root, no privilege escalation, every
// capability dropped, RuntimeDefault seccomp, read-only root filesystem.
func (k *Kubernetes) jobSpec(job store.RunningJob) (*batchv1.Job, error) {
	res, err := k.Cfg.resources()
	if err != nil {
		return nil, err
	}
	var pullSecrets []corev1.LocalObjectReference
	if k.Cfg.ImagePullSecret != "" {
		pullSecrets = []corev1.LocalObjectReference{{Name: k.Cfg.ImagePullSecret}}
	}
	labels := map[string]string{appLabel: appValue, movieIDLabel: job.MovieID}
	f := false
	t := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: *job.K8sJobName, Namespace: k.Cfg.Namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            ptr(int32(0)),
			ActiveDeadlineSeconds:   ptr(int64(k.Cfg.Deadline / time.Second)),
			TTLSecondsAfterFinished: ptr(seconds32(k.Cfg.TTL)),
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyNever,
					// A dedicated ServiceAccount with no RBAC, and its token not
					// mounted: the transcoder talks to Postgres, never to Kubernetes.
					ServiceAccountName:           k.Cfg.ServiceAccount,
					AutomountServiceAccountToken: &f,
					ImagePullSecrets:             pullSecrets,
					EnableServiceLinks:           &f,
					SecurityContext: &corev1.PodSecurityContext{
						RunAsNonRoot:   &t,
						RunAsUser:      &k.Cfg.RunAsUser,
						RunAsGroup:     &k.Cfg.RunAsGroup,
						SeccompProfile: &corev1.SeccompProfile{Type: corev1.SeccompProfileTypeRuntimeDefault},
					},
					Containers: []corev1.Container{{
						Name:            appValue,
						Image:           k.Cfg.Image,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Args:            []string{"--movie-id", job.MovieID},
						Resources:       res,
						Env: []corev1.EnvVar{
							{Name: "MEDIA_ROOT", Value: k.Cfg.MediaMountPath},
							{Name: "LOG_LEVEL", Value: k.Cfg.LogLevel},
							{Name: "DATABASE_URL", ValueFrom: &corev1.EnvVarSource{
								SecretKeyRef: &corev1.SecretKeySelector{
									LocalObjectReference: corev1.LocalObjectReference{Name: k.Cfg.DatabaseSecretName},
									Key:                  k.Cfg.DatabaseSecretKey,
								},
							}},
						},
						SecurityContext: &corev1.SecurityContext{
							AllowPrivilegeEscalation: &f,
							ReadOnlyRootFilesystem:   &t,
							RunAsNonRoot:             &t,
							Capabilities:             &corev1.Capabilities{Drop: []corev1.Capability{"ALL"}},
						},
						VolumeMounts: []corev1.VolumeMount{
							{Name: "media", MountPath: k.Cfg.MediaMountPath},
							{Name: "tmp", MountPath: "/tmp"},
						},
					}},
					Volumes: []corev1.Volume{
						{Name: "media", VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: k.Cfg.MediaClaimName},
						}},
						{Name: "tmp", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
					},
				},
			},
		},
	}, nil
}

func ptr[T any](v T) *T { return &v }

// maxTTL keeps ttlSecondsAfterFinished well inside an int32.
const maxTTL = 365 * 24 * time.Hour

// seconds32 is d in whole seconds, clamped to [0, maxTTL].
func seconds32(d time.Duration) int32 {
	s := int64(min(max(d, 0), maxTTL) / time.Second)
	if s > math.MaxInt32 {
		s = math.MaxInt32
	}
	return int32(s)
}
