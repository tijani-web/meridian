/*
Package health implements the Kubernetes controller reconciling Node accelerator health
with active Meridian GPUWorkloads.

When a node agent flags a node condition meridian.dev/GPUHealthy=False or attaches a
meridian.dev/gpu-unhealthy taint, this controller:
1. Identifies all gang member pods residing on the failed node via the IndexPodNode field indexer.
2. Evaluates fault severity (Fatal vs Recoverable).
3. Transitions affected workloads from Running/Scheduling to Recovering with a generation bump.
4. Cleans up victim pods on the failed node so the gang can safely reschedule on healthy hardware.
*/
package health

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	agenthealth "github.com/tijani-web/meridian/internal/agent/health"
	"github.com/tijani-web/meridian/internal/controller/workload"
	"github.com/tijani-web/meridian/internal/fsm"
)

var log = logf.Log.WithName("node-health-controller")

// NodeHealthReconciler reconciles Node objects and evicts/recovers affected workloads.
type NodeHealthReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
}

// +kubebuilder:rbac:groups="",resources=nodes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups="",resources=nodes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=meridian.dev,resources=gpuworkloads/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile handles Node health changes.
func (r *NodeHealthReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	reqLog := log.WithValues("node", req.Name)

	var node corev1.Node
	if err := r.Get(ctx, req.NamespacedName, &node); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("failed to fetch Node %s: %w", req.Name, err)
	}

	// 1. Inspect Node condition & taints
	isUnhealthy, isFatal, faultReason := r.evaluateNodeHealth(&node)
	if !isUnhealthy {
		return ctrl.Result{}, nil // Node is healthy
	}

	reqLog.Info("GPU node hardware fault detected; discovering affected gang pods",
		"fatal", isFatal, "reason", faultReason)

	// 2. Find all pods placed on this node via indexer
	var podList corev1.PodList
	err := r.List(ctx, &podList, client.MatchingFields{workload.IndexPodNode: node.Name})
	if err != nil {
		return ctrl.Result{}, fmt.Errorf("failed to list pods on node %s: %w", node.Name, err)
	}

	if len(podList.Items) == 0 {
		return ctrl.Result{}, nil // No active pods on failed node
	}

	// 3. Group pods by owning GPUWorkload
	affectedWorkloads := make(map[client.ObjectKey][]corev1.Pod)
	for _, p := range podList.Items {
		wlName := p.Labels[workload.LabelWorkload]
		if wlName == "" {
			continue // Not a Meridian workload pod
		}
		key := client.ObjectKey{Namespace: p.Namespace, Name: wlName}
		affectedWorkloads[key] = append(affectedWorkloads[key], p)
	}

	// 4. Recover affected workloads
	now := time.Now().UTC()
	for key, podsOnNode := range affectedWorkloads {
		var w meridianv1alpha1.GPUWorkload
		if err := r.Get(ctx, key, &w); err != nil {
			if errors.IsNotFound(err) {
				continue
			}
			reqLog.Error(err, "Failed to get affected workload", "workload", key)
			continue
		}

		if w.Status.Phase != meridianv1alpha1.WorkloadPhaseRunning &&
			w.Status.Phase != meridianv1alpha1.WorkloadPhaseScheduling {
			continue // Already in Recovering, Failed, or terminal
		}

		orig := w.DeepCopy()

		// Bump generation for recovery cycle
		w.Status.Generation++

		reasonMsg := fmt.Sprintf("Node %s hardware fault (%s); initiating gang recovery at generation %d",
			node.Name, faultReason, w.Status.Generation)

		if err := fsm.ApplyTransition(&w.Status, fsm.EventNodeFailure, "NodeHardwareFailure", reasonMsg, now); err != nil {
			reqLog.Error(err, "Failed to apply node failure transition", "workload", key)
			continue
		}

		if err := r.Status().Patch(ctx, &w, client.MergeFrom(orig)); err != nil {
			return ctrl.Result{}, fmt.Errorf("failed to patch workload status to Recovering: %w", err)
		}

		if r.Recorder != nil {
			r.Recorder.Event(&w, corev1.EventTypeWarning, "NodeHardwareFailure", reasonMsg)
		}

		// Delete pods residing on the failed node so new pods can be placed
		for i := range podsOnNode {
			pod := &podsOnNode[i]
			if pod.DeletionTimestamp.IsZero() {
				if err := r.Delete(ctx, pod); err != nil && !errors.IsNotFound(err) {
					reqLog.Error(err, "Failed to evict pod from failed node", "pod", pod.Name)
				}
			}
		}

		reqLog.Info("Workload transitioned to Recovering and evicted pods from failed node",
			"workload", key, "generation", w.Status.Generation)
	}

	return ctrl.Result{}, nil
}

// evaluateNodeHealth returns true if the node is tainted or flagged unhealthy.
func (r *NodeHealthReconciler) evaluateNodeHealth(node *corev1.Node) (unhealthy bool, fatal bool, reason string) {
	// Check taints
	for _, t := range node.Spec.Taints {
		if t.Key == agenthealth.TaintGPUUnhealthy {
			unhealthy = true
			if t.Effect == corev1.TaintEffectNoExecute {
				fatal = true
			}
			reason = fmt.Sprintf("Taint(%s=%s)", t.Value, t.Effect)
			return
		}
	}

	// Check conditions
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeConditionType(agenthealth.ConditionGPUHealthy) && c.Status == corev1.ConditionFalse {
			unhealthy = true
			reason = c.Reason
			if reason == "" {
				reason = "ConditionGPUHealthyFalse"
			}
			return
		}
	}

	return false, false, ""
}

// SetupWithManager registers the NodeHealthReconciler with the manager.
func (r *NodeHealthReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&corev1.Node{}).
		Watches(
			&corev1.Pod{},
			handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
				pod, ok := obj.(*corev1.Pod)
				if !ok || pod.Spec.NodeName == "" || pod.Labels[workload.LabelWorkload] == "" {
					return nil
				}
				return []reconcile.Request{{NamespacedName: client.ObjectKey{Name: pod.Spec.NodeName}}}
			}),
		).
		Complete(r)
}
