package queue

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
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

var log = logf.Log.WithName("queue-controller")

const (
	// AdmissionSchedulingGate is the gate removed from pods when the gang is admitted.
	AdmissionSchedulingGate = "meridian.dev/admission-gate"
)

// QueueReconciler reconciles a GPUQueue, managing quota tracking, DRF-lite fair-share admission,
// scheduling gate release, and reclaim preemption.
type QueueReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups=meridian.dev,resources=gpuqueues,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuqueues/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;update;patch

// Reconcile calculates quota usage for the queue and evaluates queued workloads for atomic admission.
func (r *QueueReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLog := log.WithValues("queue", req.Name)

	var q meridianv1alpha1.GPUQueue
	if err := r.Get(ctx, req.NamespacedName, &q); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to fetch GPUQueue: %w", err)
	}

	// 1. List all workloads assigned to this queue
	var allWorkloads meridianv1alpha1.GPUWorkloadList
	if err := r.List(ctx, &allWorkloads); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list workloads: %w", err)
	}

	queueWorkloads := make([]meridianv1alpha1.GPUWorkload, 0)
	for _, w := range allWorkloads.Items {
		if w.Spec.QueueName == q.Name {
			queueWorkloads = append(queueWorkloads, w)
		}
	}

	// 2. Aggregate current usage
	nominalUsed := make(map[string]int32)
	borrowedUsed := make(map[string]int32)
	admittedCount := int32(0)
	pendingCount := int32(0)

	var queuedWorkloads []*meridianv1alpha1.GPUWorkload

	for i := range queueWorkloads {
		w := &queueWorkloads[i]
		phase := w.Status.Phase

		switch phase {
		case meridianv1alpha1.WorkloadPhaseAdmitted,
			meridianv1alpha1.WorkloadPhaseScheduling,
			meridianv1alpha1.WorkloadPhaseRunning,
			meridianv1alpha1.WorkloadPhaseCheckpointing:
			admittedCount++
			gpus := CalculateWorkloadGPUs(w)
			gpuType := w.Spec.GPUType

			nominalQuota := q.Spec.NominalQuota[gpuType]
			if nominalUsed[gpuType]+gpus <= nominalQuota {
				nominalUsed[gpuType] += gpus
			} else {
				remNom := nominalQuota - nominalUsed[gpuType]
				if remNom > 0 {
					nominalUsed[gpuType] += remNom
					borrowedUsed[gpuType] += (gpus - remNom)
				} else {
					borrowedUsed[gpuType] += gpus
				}
			}

		case meridianv1alpha1.WorkloadPhaseQueued, meridianv1alpha1.WorkloadPhasePending:
			pendingCount++
			queuedWorkloads = append(queuedWorkloads, w)
		}
	}

	// 3. Update queue status
	origQueue := q.DeepCopy()
	q.Status.AdmittedWorkloads = admittedCount
	q.Status.PendingWorkloads = pendingCount

	if q.Status.Usage == nil {
		q.Status.Usage = make(map[string]meridianv1alpha1.GPUUsageBreakdown)
	}
	for gpuType, nominal := range q.Spec.NominalQuota {
		used := nominalUsed[gpuType] + borrowedUsed[gpuType]
		q.Status.Usage[gpuType] = meridianv1alpha1.GPUUsageBreakdown{
			Used:     used,
			Borrowed: borrowedUsed[gpuType],
			Lent:     0, // Computed cluster-wide
		}
		_ = nominal
	}

	if err := r.Status().Patch(ctx, &q, client.MergeFrom(origQueue)); err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to patch queue status: %w", err)
	}

	// 4. Evaluate admission for queued workloads
	qState := &QueueState{
		Name:           q.Name,
		Priority:       q.Spec.Priority,
		NominalQuota:   q.Spec.NominalQuota,
		BorrowingLimit: q.Spec.BorrowingLimit,
		LendingLimit:   q.Spec.LendingLimit,
		NominalUsed:    nominalUsed,
		BorrowedUsed:   borrowedUsed,
	}

	// Approximate cluster idle capacity as cluster-wide headroom
	clusterIdle := make(map[string]int32)
	for gpuType, nominal := range q.Spec.NominalQuota {
		idle := nominal - nominalUsed[gpuType]
		if idle < 0 {
			idle = 0
		}
		clusterIdle[gpuType] = idle
	}

	for _, w := range queuedWorkloads {
		gpus := CalculateWorkloadGPUs(w)
		gpuType := w.Spec.GPUType

		decision, deficit := EvaluateAdmission(qState, gpuType, gpus, clusterIdle)
		switch decision {
		case DecisionAdmitNominal, DecisionAdmitBorrowed:
			reqLog.Info("admitting workload", "workload", w.Name, "gpus", gpus, "borrowed", deficit)
			if err := r.admitWorkload(ctx, w, deficit > 0); err != nil {
				return ctrl.Result{}, fmt.Errorf("failed to admit workload %s: %w", w.Name, err)
			}
			// Update local accounting
			if decision == DecisionAdmitNominal {
				qState.NominalUsed[gpuType] += gpus
			} else {
				qState.BorrowedUsed[gpuType] += deficit
			}

		case DecisionReclaimNeeded:
			reqLog.Info("reclaim needed for workload", "workload", w.Name, "neededGPUs", deficit)
			// Trigger reclaim against victim workloads running on borrowed capacity
			r.initiateReclaim(ctx, &allWorkloads, q.Name, gpuType, deficit)
		}
	}

	return ctrl.Result{RequeueAfter: 10 * time.Second}, nil
}

// admitWorkload atomically admits the gang, records the Admitted condition, and unblocks pod scheduling gates.
func (r *QueueReconciler) admitWorkload(ctx context.Context, w *meridianv1alpha1.GPUWorkload, isBorrowed bool) error {
	orig := w.DeepCopy()
	now := metav1.Now()

	// 1. Mark Admitted condition
	admittedFound := false
	for i, c := range w.Status.Conditions {
		if c.Type == "Admitted" {
			w.Status.Conditions[i].Status = metav1.ConditionTrue
			w.Status.Conditions[i].LastTransitionTime = now
			w.Status.Conditions[i].Reason = "QuotaReserved"
			w.Status.Conditions[i].Message = fmt.Sprintf("Admitted by queue controller (borrowed=%t)", isBorrowed)
			admittedFound = true
			break
		}
	}
	if !admittedFound {
		w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
			Type:               "Admitted",
			Status:             metav1.ConditionTrue,
			LastTransitionTime: now,
			Reason:             "QuotaReserved",
			Message:            fmt.Sprintf("Admitted by queue controller (borrowed=%t)", isBorrowed),
		})
	}
	w.Status.AdmittedAt = &now

	if err := r.Status().Patch(ctx, w, client.MergeFrom(orig)); err != nil {
		return fmt.Errorf("failed to update workload admission status: %w", err)
	}

	// 2. Remove scheduling gate from all member pods
	var podList corev1.PodList
	if err := r.List(ctx, &podList,
		client.InNamespace(w.Namespace),
		client.MatchingLabels{"meridian.dev/workload": w.Name},
	); err == nil {
		for i := range podList.Items {
			pod := &podList.Items[i]
			if len(pod.Spec.SchedulingGates) > 0 {
				newGates := make([]corev1.PodSchedulingGate, 0, len(pod.Spec.SchedulingGates))
				for _, g := range pod.Spec.SchedulingGates {
					if g.Name != AdmissionSchedulingGate {
						newGates = append(newGates, g)
					}
				}
				if len(newGates) != len(pod.Spec.SchedulingGates) {
					origPod := pod.DeepCopy()
					pod.Spec.SchedulingGates = newGates
					_ = r.Patch(ctx, pod, client.MergeFrom(origPod))
				}
			}
		}
	}

	r.Recorder.Event(w, corev1.EventTypeNormal, "WorkloadAdmitted", "Gang quota admitted; scheduling gates released")
	return nil
}

// initiateReclaim identifies victim borrowed workloads and triggers preemption.
func (r *QueueReconciler) initiateReclaim(
	ctx context.Context,
	allWorkloads *meridianv1alpha1.GPUWorkloadList,
	requestingQueue string,
	gpuType string,
	neededGPUs int32,
) {
	candidates := make([]WorkloadCandidate, 0)
	for _, w := range allWorkloads.Items {
		if w.Spec.QueueName != requestingQueue && w.Spec.GPUType == gpuType {
			if w.Status.Phase == meridianv1alpha1.WorkloadPhaseRunning ||
				w.Status.Phase == meridianv1alpha1.WorkloadPhaseScheduling {
				candidates = append(candidates, WorkloadCandidate{
					Name:         w.Name,
					Namespace:    w.Namespace,
					QueueName:    w.Spec.QueueName,
					GPUType:      w.Spec.GPUType,
					TotalGPUs:    CalculateWorkloadGPUs(&w),
					IsBorrowed:   true, // Simplified heuristic for borrowers
					DominantRate: 0.5,
					AdmittedAt:   time.Now(),
				})
			}
		}
	}

	victims := SelectVictims(candidates, neededGPUs)
	for _, v := range victims {
		var victimW meridianv1alpha1.GPUWorkload
		if err := r.Get(ctx, client.ObjectKey{Namespace: v.Namespace, Name: v.Name}, &victimW); err == nil {
			origVictim := victimW.DeepCopy()
			victimW.Status.Phase = meridianv1alpha1.WorkloadPhasePreempting
			_ = r.Status().Patch(ctx, &victimW, client.MergeFrom(origVictim))
			r.Recorder.Event(&victimW, corev1.EventTypeWarning, "PreemptionInitiated",
				fmt.Sprintf("Preempted by queue %s reclaim", requestingQueue))
		}
	}
}

// SetupWithManager registers the QueueReconciler with the controller manager.
func (r *QueueReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&meridianv1alpha1.GPUQueue{}).
		Watches(
			&meridianv1alpha1.GPUWorkload{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				w, ok := obj.(*meridianv1alpha1.GPUWorkload)
				if !ok || w.Spec.QueueName == "" {
					return nil
				}
				return []reconcile.Request{
					{NamespacedName: client.ObjectKey{Name: w.Spec.QueueName}},
				}
			}),
		).
		Complete(r)
}
