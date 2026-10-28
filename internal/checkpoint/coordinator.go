/*
Package checkpoint implements the controller-side cooperative checkpoint protocol
and preemption coordinator for Meridian GPU workloads.

Meridian workloads undergo cooperative checkpointing before preemption or during
periodic safety intervals. The coordinator ensures that:
1. Checkpoint requests are broadcast across all gang member pods simultaneously
   via Server-Side Apply / patch annotations.
2. Acknowledgments from all gang members are validated and tracked.
3. Timeouts trigger forced eviction and mark CheckpointHealthy=False without
   stalling the cluster queue.
4. Resumed workloads receive the last acknowledged checkpoint path and step
   injected via MERIDIAN_RESUME_FROM and MERIDIAN_RESUME_STEP environment variables.
*/
package checkpoint

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/pkg/ckptclient"
)

const (
	// AnnotationCheckpointPath specifies the checkpoint output directory for pods.
	AnnotationCheckpointPath = "meridian.dev/checkpoint-path"

	// AnnotationCheckpointRequestedAt tracks the timestamp when checkpointing began.
	AnnotationCheckpointRequestedAt = "meridian.dev/checkpoint-requested-at"

	// AnnotationCheckpointSeq tracks the current monotonic checkpoint sequence number.
	AnnotationCheckpointSeq = "meridian.dev/checkpoint-seq"

	// AnnotationPreemptRequested flags that the current checkpoint cycle is a precursor to preemption.
	AnnotationPreemptRequested = "meridian.dev/preempt-requested"

	// AnnotationLastCheckpointPath stores the path of the latest valid acknowledged checkpoint.
	AnnotationLastCheckpointPath = "meridian.dev/last-checkpoint-path"

	// AnnotationLastCheckpointStep stores the training step of the latest valid acknowledged checkpoint.
	AnnotationLastCheckpointStep = "meridian.dev/last-checkpoint-step"

	// ConditionCheckpointHealthy indicates whether the most recent checkpoint succeeded.
	ConditionCheckpointHealthy = "CheckpointHealthy"

	// DefaultAckTimeout is the default duration to wait for all gang pods to ack a checkpoint.
	DefaultAckTimeout = 60 * time.Second

	// DefaultStragglerWindow is the time after which a pod without heartbeats is considered a straggler.
	DefaultStragglerWindow = 60 * time.Second
)

// AckStatus represents the verification outcome of gang member acknowledgments.
type AckStatus string

const (
	// AckStatusPending means not all gang members have acknowledged yet, and timeout has not expired.
	AckStatusPending AckStatus = "Pending"

	// AckStatusComplete means every gang member has successfully written and acknowledged the checkpoint.
	AckStatusComplete AckStatus = "Complete"

	// AckStatusTimeout means the deadline expired before all gang members acknowledged.
	AckStatusTimeout AckStatus = "Timeout"
)

// AckSummary encapsulates the consolidated results from all gang member acknowledgments.
type AckSummary struct {
	// Generation is the workload generation at the time of the checkpoint.
	Generation int64

	// Seq is the monotonic sequence number of the checkpoint.
	Seq int64

	// Path is the common or primary checkpoint artifact path.
	Path string

	// Step is the highest or primary training step reported across members.
	Step int64

	// PendingPods lists the names of pods that have not yet acknowledged.
	PendingPods []string

	// AckedPods lists the names of pods that have successfully acknowledged.
	AckedPods []string
}

// Coordinator manages cooperative checkpoint cycles for the workload controller.
type Coordinator struct {
	client   client.Client
	recorder record.EventRecorder
}

// NewCoordinator creates a new checkpoint Coordinator.
func NewCoordinator(c client.Client, r record.EventRecorder) *Coordinator {
	return &Coordinator{
		client:   c,
		recorder: r,
	}
}

// RequestCheckpoint initiates a cooperative checkpoint cycle across all gang member pods.
// It patches the request annotation onto each pod and updates the workload's tracking annotations.
func (c *Coordinator) RequestCheckpoint(
	ctx context.Context,
	w *meridianv1alpha1.GPUWorkload,
	pods []corev1.Pod,
	isPreemption bool,
) error {
	if len(pods) == 0 {
		return fmt.Errorf("cannot request checkpoint: workload %s has no active pods", w.Name)
	}

	seq := getNextSeq(w)
	gen := w.Status.Generation
	requestVal := fmt.Sprintf("%d:%d", gen, seq)
	now := time.Now().UTC()

	ckptPath := w.Spec.Checkpoint.Path
	if ckptPath == "" {
		ckptPath = fmt.Sprintf("/var/log/meridian/checkpoints/%s", w.Name)
	}

	// 1. Broadcast checkpoint-request to all gang pods
	for i := range pods {
		pod := &pods[i]
		patchData := fmt.Sprintf(
			`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
			ckptclient.AnnotationCheckpointRequest, requestVal,
			AnnotationCheckpointPath, ckptPath,
		)
		if err := c.client.Patch(ctx, pod, client.RawPatch(types.MergePatchType, []byte(patchData))); err != nil {
			return fmt.Errorf("failed to patch checkpoint request on pod %s: %w", pod.Name, err)
		}
	}

	// 2. Annotate workload with tracking metadata
	if w.Annotations == nil {
		w.Annotations = make(map[string]string)
	}
	w.Annotations[AnnotationCheckpointSeq] = strconv.FormatInt(seq, 10)
	w.Annotations[AnnotationCheckpointRequestedAt] = now.Format(time.RFC3339)
	if isPreemption {
		w.Annotations[AnnotationPreemptRequested] = "true"
	} else {
		delete(w.Annotations, AnnotationPreemptRequested)
	}

	if c.recorder != nil {
		c.recorder.Event(w, corev1.EventTypeNormal, "CheckpointRequested",
			fmt.Sprintf("Dispatched checkpoint request seq %d (gen %d) to %d gang pods", seq, gen, len(pods)))
	}

	return nil
}

// EvaluateAcks inspects all gang pods to evaluate whether the current checkpoint cycle has
// completed, is still pending, or has timed out.
func (c *Coordinator) EvaluateAcks(
	w *meridianv1alpha1.GPUWorkload,
	pods []corev1.Pod,
) (AckStatus, *AckSummary, error) {
	if len(pods) == 0 {
		return AckStatusPending, nil, nil
	}

	seqStr := w.Annotations[AnnotationCheckpointSeq]
	if seqStr == "" {
		return AckStatusPending, nil, fmt.Errorf("workload %s has no active checkpoint seq annotation", w.Name)
	}
	seq, err := strconv.ParseInt(seqStr, 10, 64)
	if err != nil {
		return AckStatusPending, nil, fmt.Errorf("invalid checkpoint seq %q: %w", seqStr, err)
	}
	gen := w.Status.Generation
	expectedPrefix := fmt.Sprintf("%d:%d", gen, seq)

	summary := &AckSummary{
		Generation: gen,
		Seq:        seq,
	}

	var maxStep int64
	var commonPath string

	for i := range pods {
		pod := &pods[i]
		ackVal := pod.Annotations[ckptclient.AnnotationCheckpointAck]
		if strings.HasPrefix(ackVal, expectedPrefix) {
			summary.AckedPods = append(summary.AckedPods, pod.Name)
			// Format is "<generation>:<seq>:<path>"
			parts := strings.SplitN(ackVal, ":", 3)
			if len(parts) == 3 && parts[2] != "" {
				commonPath = parts[2]
			}
			// Parse step if heartbeat step annotation is present
			if stepVal, ok := pod.Annotations[ckptclient.AnnotationCheckpointStep]; ok {
				if s, err := strconv.ParseInt(stepVal, 10, 64); err == nil && s > maxStep {
					maxStep = s
				}
			}
		} else {
			summary.PendingPods = append(summary.PendingPods, pod.Name)
		}
	}

	summary.Path = commonPath
	summary.Step = maxStep

	// All gang members acked
	if len(summary.PendingPods) == 0 {
		return AckStatusComplete, summary, nil
	}

	// Check timeout deadline
	timeout := DefaultAckTimeout
	if w.Spec.Checkpoint.AckTimeout.Duration > 0 {
		timeout = w.Spec.Checkpoint.AckTimeout.Duration
	}

	reqAtStr := w.Annotations[AnnotationCheckpointRequestedAt]
	if reqAtStr != "" {
		reqAt, err := time.Parse(time.RFC3339, reqAtStr)
		if err == nil && time.Since(reqAt) > timeout {
			return AckStatusTimeout, summary, nil
		}
	}

	return AckStatusPending, summary, nil
}

// CompleteCheckpoint finalizes a successful checkpoint cycle, updating workload status
// and annotations with the saved checkpoint coordinates.
func (c *Coordinator) CompleteCheckpoint(
	ctx context.Context,
	w *meridianv1alpha1.GPUWorkload,
	summary *AckSummary,
) error {
	now := metav1.Now()
	w.Status.LastCheckpointAt = &now

	if w.Annotations == nil {
		w.Annotations = make(map[string]string)
	}
	if summary.Path != "" {
		w.Annotations[AnnotationLastCheckpointPath] = summary.Path
	}
	if summary.Step > 0 {
		w.Annotations[AnnotationLastCheckpointStep] = strconv.FormatInt(summary.Step, 10)
	}

	setCondition(w, ConditionCheckpointHealthy, metav1.ConditionTrue, "CheckpointAcknowledged",
		fmt.Sprintf("Checkpoint seq %d acked by all %d gang members at %s", summary.Seq, len(summary.AckedPods), summary.Path))

	if c.recorder != nil {
		c.recorder.Event(w, corev1.EventTypeNormal, "CheckpointAcknowledged",
			fmt.Sprintf("Checkpoint seq %d fully acked at step %d: %s", summary.Seq, summary.Step, summary.Path))
	}

	return nil
}

// HandleTimeout marks the checkpoint cycle as timed out and flags the workload condition.
func (c *Coordinator) HandleTimeout(
	ctx context.Context,
	w *meridianv1alpha1.GPUWorkload,
	summary *AckSummary,
) error {
	setCondition(w, ConditionCheckpointHealthy, metav1.ConditionFalse, "AckTimeout",
		fmt.Sprintf("Checkpoint seq %d timed out waiting for pods: %s", summary.Seq, strings.Join(summary.PendingPods, ", ")))

	if c.recorder != nil {
		c.recorder.Event(w, corev1.EventTypeWarning, "CheckpointTimeout",
			fmt.Sprintf("Checkpoint seq %d timed out; %d pods unacknowledged: %s",
				summary.Seq, len(summary.PendingPods), strings.Join(summary.PendingPods, ", ")))
	}

	return nil
}

// InjectResumeEnv attaches the MERIDIAN_RESUME_FROM and MERIDIAN_RESUME_STEP environment
// variables to all containers within the pod spec, ensuring continuity upon recovery or restart.
func InjectResumeEnv(podSpec *corev1.PodSpec, resumePath string, resumeStep int64) {
	if resumePath == "" && resumeStep <= 0 {
		return
	}

	for i := range podSpec.Containers {
		c := &podSpec.Containers[i]
		if resumePath != "" {
			c.Env = setOrUpdateEnv(c.Env, ckptclient.EnvResumeFrom, resumePath)
		}
		if resumeStep > 0 {
			c.Env = setOrUpdateEnv(c.Env, ckptclient.EnvResumeStep, strconv.FormatInt(resumeStep, 10))
		}
	}
}

// DetectStragglers identifies gang member pods that have failed to emit a liveness heartbeat
// within the configured grace period.
func DetectStragglers(pods []corev1.Pod, maxAge time.Duration) []string {
	if maxAge <= 0 {
		maxAge = DefaultStragglerWindow
	}
	cutoff := time.Now().UTC().Add(-maxAge)
	var stragglers []string

	for _, pod := range pods {
		hbStr, ok := pod.Annotations[ckptclient.AnnotationCheckpointHeartbeat]
		if !ok || hbStr == "" {
			continue // No heartbeat recorded yet
		}
		t, err := time.Parse(time.RFC3339, hbStr)
		if err != nil || t.Before(cutoff) {
			stragglers = append(stragglers, pod.Name)
		}
	}
	return stragglers
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

func getNextSeq(w *meridianv1alpha1.GPUWorkload) int64 {
	if w.Annotations == nil {
		return 1
	}
	val, ok := w.Annotations[AnnotationCheckpointSeq]
	if !ok || val == "" {
		return 1
	}
	seq, err := strconv.ParseInt(val, 10, 64)
	if err != nil {
		return 1
	}
	return seq + 1
}

func setOrUpdateEnv(envs []corev1.EnvVar, key, val string) []corev1.EnvVar {
	for i, e := range envs {
		if e.Name == key {
			envs[i].Value = val
			return envs
		}
	}
	return append(envs, corev1.EnvVar{Name: key, Value: val})
}

func setCondition(w *meridianv1alpha1.GPUWorkload, condType string, status metav1.ConditionStatus, reason, message string) {
	now := metav1.Now()
	for i, c := range w.Status.Conditions {
		if c.Type == condType {
			w.Status.Conditions[i].Status = status
			w.Status.Conditions[i].LastTransitionTime = now
			w.Status.Conditions[i].Reason = reason
			w.Status.Conditions[i].Message = message
			return
		}
	}
	w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
		Type:               condType,
		Status:             status,
		LastTransitionTime: now,
		Reason:             reason,
		Message:            message,
	})
}
