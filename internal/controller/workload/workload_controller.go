package workload

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/fsm"
)

var log = logf.Log.WithName("workload-controller")

// WorkloadReconciler reconciles a GPUWorkload object through its lifecycle state machine.
type WorkloadReconciler struct {
	client.Client
	Scheme                  *runtime.Scheme
	Recorder                record.EventRecorder
	MaxConcurrentReconciles int
}

// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads/finalizers,verbs=update
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile executes the control loop for a single GPUWorkload.
func (r *WorkloadReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLog := log.WithValues("workload", req.NamespacedName)

	var w meridianv1alpha1.GPUWorkload
	if err := r.Get(ctx, req.NamespacedName, &w); err != nil {
		if errors.IsNotFound(err) {
			// Workload deleted from etcd; child pods cleaned up via GC / finalizer.
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to fetch GPUWorkload: %w", err)
	}

	// ──────────────────────────────────────────────────────────────────────────
	// Deletion handling & finalizers
	// ──────────────────────────────────────────────────────────────────────────
	if !w.DeletionTimestamp.IsZero() {
		return r.handleDeletion(ctx, &w, reqLog)
	}

	// Ensure finalizer is attached
	if !controllerutil.ContainsFinalizer(&w, WorkloadFinalizer) {
		controllerutil.AddFinalizer(&w, WorkloadFinalizer)
		if err := r.Update(ctx, &w); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to attach finalizer: %w", err)
		}
		return ctrl.Result{}, nil
	}

	// ──────────────────────────────────────────────────────────────────────────
	// Lifecycle State Machine Execution
	// ──────────────────────────────────────────────────────────────────────────
	phase := w.Status.Phase
	if phase == "" {
		phase = meridianv1alpha1.WorkloadPhasePending
	}

	switch phase {
	case meridianv1alpha1.WorkloadPhasePending:
		return r.reconcilePending(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseQueued:
		return r.reconcileQueued(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseAdmitted:
		return r.reconcileAdmitted(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseScheduling:
		return r.reconcileScheduling(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseRunning:
		return r.reconcileRunning(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseCheckpointing:
		return r.reconcileCheckpointing(ctx, &w)

	case meridianv1alpha1.WorkloadPhasePreempting:
		return r.reconcilePreempting(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseRecovering:
		return r.reconcileRecovering(ctx, &w)

	case meridianv1alpha1.WorkloadPhaseSucceeded, meridianv1alpha1.WorkloadPhaseFailed:
		// Terminal states: no further transitions
		return ctrl.Result{}, nil

	default:
		reqLog.Info("unknown workload phase", "phase", phase)
		return ctrl.Result{}, nil
	}
}

// reconcilePending transitions a newly submitted workload to Queued.
func (r *WorkloadReconciler) reconcilePending(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	orig := w.DeepCopy()
	now := time.Now()

	if err := fsm.ApplyTransition(&w.Status, fsm.EventEnqueue, "WorkloadEnqueued", "Workload enqueued for queue quota admission", now); err != nil {
		return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
	}

	r.setCondition(w, "Enqueued", metav1.ConditionTrue, "WorkloadEnqueued", "Enrolled in queue "+w.Spec.QueueName)
	if err := r.patchStatus(ctx, orig, w); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Event(w, corev1.EventTypeNormal, "WorkloadEnqueued", "Workload enrolled in queue "+w.Spec.QueueName)
	return ctrl.Result{}, nil
}

// reconcileQueued waits for the queue admission controller to grant quota admission.
func (r *WorkloadReconciler) reconcileQueued(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	// If queue admission has already flagged AdmittedAt or set condition, trigger Admit
	for _, cond := range w.Status.Conditions {
		if cond.Type == "Admitted" && cond.Status == metav1.ConditionTrue {
			orig := w.DeepCopy()
			now := time.Now()
			if err := fsm.ApplyTransition(&w.Status, fsm.EventAdmit, "QuotaGranted", "Quota successfully reserved", now); err != nil {
				return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
			}
			if err := r.patchStatus(ctx, orig, w); err != nil {
				return ctrl.Result{}, err
			}
			r.Recorder.Event(w, corev1.EventTypeNormal, "WorkloadAdmitted", "Quota reserved for gang")
			return ctrl.Result{Requeue: true}, nil
		}
	}

	// Still waiting in queue
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// reconcileAdmitted creates the gang of pods with scheduling gates and advances to Scheduling.
func (r *WorkloadReconciler) reconcileAdmitted(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	gangSize := int(w.Spec.Gang.Size)
	existingPods, err := r.getWorkloadPods(ctx, w)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Create any missing gang member pods
	existingByIndex := make(map[int32]*corev1.Pod)
	for i := range existingPods {
		p := &existingPods[i]
		if idxStr, ok := p.Labels[LabelGangIndex]; ok {
			var idx int32
			if _, scanErr := fmt.Sscanf(idxStr, "%d", &idx); scanErr == nil {
				existingByIndex[idx] = p
			}
		}
	}

	for i := int32(0); i < int32(gangSize); i++ {
		if _, exists := existingByIndex[i]; !exists {
			pod := r.buildGangPod(w, i, w.Status.Generation)
			if err := r.Create(ctx, pod); err != nil {
				if !errors.IsAlreadyExists(err) {
					return ctrl.Result{}, fmt.Errorf("failed to create gang pod %d: %w", i, err)
				}
			}
		}
	}

	// Transition to Scheduling
	orig := w.DeepCopy()
	now := time.Now()
	if err := fsm.ApplyTransition(&w.Status, fsm.EventStartScheduling, "PodsCreated", fmt.Sprintf("Created %d gated gang member pods", gangSize), now); err != nil {
		return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
	}

	r.setCondition(w, "PodsCreated", metav1.ConditionTrue, "PodsCreated", fmt.Sprintf("%d gang pods ready for scheduling", gangSize))
	if err := r.patchStatus(ctx, orig, w); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Event(w, corev1.EventTypeNormal, "PodsCreated", fmt.Sprintf("Created %d gated gang pods", gangSize))
	return ctrl.Result{Requeue: true}, nil
}

// reconcileScheduling monitors gang placement and unblocks gates when ready.
func (r *WorkloadReconciler) reconcileScheduling(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	pods, err := r.getWorkloadPods(ctx, w)
	if err != nil {
		return ctrl.Result{}, err
	}

	gangSize := int(w.Spec.Gang.Size)
	minAvailable := int(w.Spec.Gang.MinAvailable)
	if minAvailable == 0 {
		minAvailable = gangSize
	}

	// Check for any failed pods
	for _, p := range pods {
		if p.Status.Phase == corev1.PodFailed {
			return r.handlePodFailure(ctx, w, &p)
		}
	}

	// Check how many pods are running
	runningCount := 0
	assignments := make([]meridianv1alpha1.PodAssignment, 0, len(pods))

	for _, p := range pods {
		if p.Spec.NodeName != "" {
			assignments = append(assignments, meridianv1alpha1.PodAssignment{
				Pod:  p.Name,
				Node: p.Spec.NodeName,
			})
		}
		if p.Status.Phase == corev1.PodRunning {
			runningCount++
		}
	}

	// If at least minAvailable pods are running, transition to Running
	if runningCount >= minAvailable {
		orig := w.DeepCopy()
		now := time.Now()
		if err := fsm.ApplyTransition(&w.Status, fsm.EventAllRunning, "AllRunning", fmt.Sprintf("%d/%d gang pods running", runningCount, gangSize), now); err != nil {
			return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
		}

		w.Status.Assignment = assignments
		r.setCondition(w, "Scheduled", metav1.ConditionTrue, "GangScheduled", "Gang placed on nodes")
		r.setCondition(w, "Running", metav1.ConditionTrue, "GangRunning", "Gang executing")

		if err := r.patchStatus(ctx, orig, w); err != nil {
			return ctrl.Result{}, err
		}

		r.Recorder.Event(w, corev1.EventTypeNormal, "WorkloadRunning", "All required gang pods running")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// reconcileRunning monitors running pods for completion or hardware faults.
func (r *WorkloadReconciler) reconcileRunning(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	pods, err := r.getWorkloadPods(ctx, w)
	if err != nil {
		return ctrl.Result{}, err
	}

	gangSize := int(w.Spec.Gang.Size)
	succeededCount := 0

	for _, p := range pods {
		if p.Status.Phase == corev1.PodFailed {
			return r.handlePodFailure(ctx, w, &p)
		}
		if p.Status.Phase == corev1.PodSucceeded {
			succeededCount++
		}
	}

	// If all pods succeeded, workload succeeded
	if succeededCount == gangSize && gangSize > 0 {
		orig := w.DeepCopy()
		now := time.Now()
		if err := fsm.ApplyTransition(&w.Status, fsm.EventSucceed, "WorkloadCompleted", "All gang pods terminated cleanly with code 0", now); err != nil {
			return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
		}

		r.setCondition(w, "Completed", metav1.ConditionTrue, "WorkloadCompleted", "Workload finished successfully")
		if err := r.patchStatus(ctx, orig, w); err != nil {
			return ctrl.Result{}, err
		}

		r.Recorder.Event(w, corev1.EventTypeNormal, "WorkloadCompleted", "Workload finished successfully")
		return ctrl.Result{}, nil
	}

	return ctrl.Result{RequeueAfter: 5 * time.Second}, nil
}

// reconcileCheckpointing coordinates cooperative checkpoint acknowledgments.
func (r *WorkloadReconciler) reconcileCheckpointing(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	// Handled cooperatively with checkpoint controller; if all acked, return to Running
	return ctrl.Result{RequeueAfter: 2 * time.Second}, nil
}

// reconcilePreempting drains pods and returns workload to the queue.
func (r *WorkloadReconciler) reconcilePreempting(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	pods, err := r.getWorkloadPods(ctx, w)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete victim pods
	for _, p := range pods {
		if p.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &p); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, err
			}
		}
	}

	// When pods are gone, return to Queued
	if len(pods) == 0 {
		orig := w.DeepCopy()
		now := time.Now()
		if err := fsm.ApplyTransition(&w.Status, fsm.EventEvictToQueue, "PreemptionDrained", "Victim pods deleted, workload requeued", now); err != nil {
			return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
		}
		if err := r.patchStatus(ctx, orig, w); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(w, corev1.EventTypeNormal, "RequeuedAfterPreemption", "Workload returned to queue")
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
}

// reconcileRecovering drains old pods, bumps generation, and restarts scheduling.
func (r *WorkloadReconciler) reconcileRecovering(ctx context.Context, w *meridianv1alpha1.GPUWorkload) (ctrl.Result, error) {
	pods, err := r.getWorkloadPods(ctx, w)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete old pods
	for _, p := range pods {
		if p.DeletionTimestamp.IsZero() {
			_ = r.Delete(ctx, &p)
		}
	}

	// When old pods are cleaned up, transition back to Scheduling
	if len(pods) == 0 {
		orig := w.DeepCopy()
		now := time.Now()
		if err := fsm.ApplyTransition(&w.Status, fsm.EventRestart, "GenerationRestarted", fmt.Sprintf("Restarted at generation %d", w.Status.Generation), now); err != nil {
			return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
		}
		if err := r.patchStatus(ctx, orig, w); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(w, corev1.EventTypeNormal, "GenerationRestarted", fmt.Sprintf("Restarted under generation %d", w.Status.Generation))
		return ctrl.Result{Requeue: true}, nil
	}

	return ctrl.Result{RequeueAfter: 1 * time.Second}, nil
}

// handlePodFailure evaluates restart policy when a pod fails.
func (r *WorkloadReconciler) handlePodFailure(ctx context.Context, w *meridianv1alpha1.GPUWorkload, failedPod *corev1.Pod) (ctrl.Result, error) {
	maxRestarts := int32(3)
	if w.Spec.RestartPolicy.MaxRestarts > 0 {
		maxRestarts = w.Spec.RestartPolicy.MaxRestarts
	}

	orig := w.DeepCopy()
	now := time.Now()

	if int32(w.Status.Generation) < maxRestarts {
		// Recoverable: transition to Recovering
		if err := fsm.ApplyTransition(&w.Status, fsm.EventNodeFailure, "PodFailure", fmt.Sprintf("Pod %s failed, initiating recovery generation %d", failedPod.Name, w.Status.Generation+1), now); err != nil {
			return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
		}
		if err := r.patchStatus(ctx, orig, w); err != nil {
			return ctrl.Result{}, err
		}
		r.Recorder.Event(w, corev1.EventTypeWarning, "PodFailed", fmt.Sprintf("Pod %s failed; restarting generation %d", failedPod.Name, w.Status.Generation))
		return ctrl.Result{Requeue: true}, nil
	}

	// Exceeded max restarts: transition to Failed
	if err := fsm.ApplyTransition(&w.Status, fsm.EventFail, "MaxRestartsExceeded", fmt.Sprintf("Pod %s failed and maximum %d restarts exceeded", failedPod.Name, maxRestarts), now); err != nil {
		return ctrl.Result{}, fmt.Errorf("FSM transition failed: %w", err)
	}

	r.setCondition(w, "Failed", metav1.ConditionTrue, "MaxRestartsExceeded", "Maximum restart attempts exhausted")
	if err := r.patchStatus(ctx, orig, w); err != nil {
		return ctrl.Result{}, err
	}

	r.Recorder.Event(w, corev1.EventTypeWarning, "WorkloadFailed", "Maximum restarts exceeded")
	return ctrl.Result{}, nil
}

// handleDeletion cleans up gang member pods and removes the finalizer.
func (r *WorkloadReconciler) handleDeletion(ctx context.Context, w *meridianv1alpha1.GPUWorkload, reqLog any) (ctrl.Result, error) {
	pods, err := r.getWorkloadPods(ctx, w)
	if err != nil {
		return ctrl.Result{}, err
	}

	// Delete any remaining gang pods
	for _, p := range pods {
		if p.DeletionTimestamp.IsZero() {
			if err := r.Delete(ctx, &p); err != nil && !errors.IsNotFound(err) {
				return ctrl.Result{}, fmt.Errorf("failed to delete pod %s: %w", p.Name, err)
			}
		}
	}

	// Remove finalizer once pods are deleted
	if controllerutil.ContainsFinalizer(w, WorkloadFinalizer) {
		controllerutil.RemoveFinalizer(w, WorkloadFinalizer)
		if err := r.Update(ctx, w); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to remove finalizer: %w", err)
		}
	}

	return ctrl.Result{}, nil
}

// buildGangPod constructs an individual gang member pod stamped with scheduling gates and labels.
func (r *WorkloadReconciler) buildGangPod(w *meridianv1alpha1.GPUWorkload, index int32, generation int64) *corev1.Pod {
	podName := fmt.Sprintf("%s-%d", w.Name, index)

	labels := make(map[string]string)
	for k, v := range w.Spec.PodTemplate.Labels {
		labels[k] = v
	}
	labels[LabelWorkload] = w.Name
	labels[LabelGangSize] = fmt.Sprintf("%d", w.Spec.Gang.Size)
	labels[LabelGangIndex] = fmt.Sprintf("%d", index)
	labels[LabelGeneration] = fmt.Sprintf("%d", generation)
	labels[LabelQueue] = w.Spec.QueueName

	podSpec := *w.Spec.PodTemplate.Spec.DeepCopy()

	// Ensure admission scheduling gate is attached
	hasGate := false
	for _, g := range podSpec.SchedulingGates {
		if g.Name == AdmissionSchedulingGate {
			hasGate = true
			break
		}
	}
	if !hasGate {
		podSpec.SchedulingGates = append(podSpec.SchedulingGates, corev1.PodSchedulingGate{
			Name: AdmissionSchedulingGate,
		})
	}

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        podName,
			Namespace:   w.Namespace,
			Labels:      labels,
			Annotations: w.Spec.PodTemplate.Annotations,
		},
		Spec: podSpec,
	}

	_ = ctrl.SetControllerReference(w, pod, r.Scheme)
	return pod
}

// getWorkloadPods queries the informer cache via the custom IndexPodWorkload field indexer.
func (r *WorkloadReconciler) getWorkloadPods(ctx context.Context, w *meridianv1alpha1.GPUWorkload) ([]corev1.Pod, error) {
	var podList corev1.PodList
	err := r.List(ctx, &podList,
		client.InNamespace(w.Namespace),
		client.MatchingFields{IndexPodWorkload: w.Name},
	)
	if err != nil {
		return nil, fmt.Errorf("failed to list pods for workload %s: %w", w.Name, err)
	}
	return podList.Items, nil
}

// patchStatus persists status updates using MergeFrom to ensure crash-recovery idempotency.
func (r *WorkloadReconciler) patchStatus(ctx context.Context, orig, updated *meridianv1alpha1.GPUWorkload) error {
	return r.Status().Patch(ctx, updated, client.MergeFrom(orig))
}

// setCondition updates or appends a condition in status.conditions.
func (r *WorkloadReconciler) setCondition(w *meridianv1alpha1.GPUWorkload, condType string, status metav1.ConditionStatus, reason, message string) {
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

// SetupWithManager sets up the controller with the Manager, configuring concurrency and watches.
func (r *WorkloadReconciler) SetupWithManager(mgr ctrl.Manager) error {
	concurrency := r.MaxConcurrentReconciles
	if concurrency <= 0 {
		concurrency = 4
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&meridianv1alpha1.GPUWorkload{}).
		Owns(&corev1.Pod{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: concurrency,
		}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestForOwner(mgr.GetScheme(), mgr.GetRESTMapper(), &meridianv1alpha1.GPUWorkload{}, handler.OnlyControllerOwner()),
		).
		Complete(r)
}
