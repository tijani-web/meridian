package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// gpuWorkloadLog is the logger for the GPUWorkload webhook.
var gpuWorkloadLog = logf.Log.WithName("gpuworkload-webhook")

// SetupWebhookWithManager registers the defaulting and validating webhooks for GPUWorkload
// with the controller-runtime Manager.
func (r *GPUWorkload) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy[*GPUWorkload](mgr, &GPUWorkload{}).
		WithDefaulter(&gpuWorkloadDefaulter{}).
		WithValidator(&gpuWorkloadValidator{}).
		Complete()
}

// ──────────────────────────────────────────────────────────────────────────────
// Defaulter
// ──────────────────────────────────────────────────────────────────────────────

// +kubebuilder:webhook:path=/mutate-meridian-dev-v1alpha1-gpuworkload,mutating=true,failurePolicy=fail,sideEffects=None,groups=meridian.dev,resources=gpuworkloads,verbs=create;update,versions=v1alpha1,name=mgpuworkload.kb.io,admissionReviewVersions=v1

// gpuWorkloadDefaulter implements admission.Defaulter[*GPUWorkload].
type gpuWorkloadDefaulter struct{}

// Ensure gpuWorkloadDefaulter satisfies the Defaulter interface at compile time.
var _ admission.Defaulter[*GPUWorkload] = &gpuWorkloadDefaulter{}

// Default applies sensible defaults to a GPUWorkload before it is persisted.
func (d *gpuWorkloadDefaulter) Default(ctx context.Context, r *GPUWorkload) error {
	gpuWorkloadLog.Info("defaulting GPUWorkload", "name", r.Name, "namespace", r.Namespace)

	// Kind defaults to Training for the most common execution pattern.
	if r.Spec.Kind == "" {
		r.Spec.Kind = WorkloadKindTraining
	}

	// Gang.MinAvailable defaults to Gang.Size for all-or-nothing semantics.
	// Elastic workloads must override this explicitly.
	if r.Spec.Gang.MinAvailable == 0 && r.Spec.Gang.Size > 0 {
		r.Spec.Gang.MinAvailable = r.Spec.Gang.Size
	}

	// Topology.Preference defaults to None (no hard placement requirement).
	if r.Spec.Topology.Preference == "" {
		r.Spec.Topology.Preference = TopologyPreferenceNone
	}

	// RestartPolicy.MaxRestarts defaults to 3 for resilience against transient node failures.
	if r.Spec.RestartPolicy.MaxRestarts == 0 {
		r.Spec.RestartPolicy.MaxRestarts = 3
	}

	// RestartPolicy.Backoff defaults to 10s to avoid thundering-herd on recovery.
	if r.Spec.RestartPolicy.Backoff.Duration == 0 {
		r.Spec.RestartPolicy.Backoff.Duration = 10_000_000_000 // 10s in nanoseconds
	}

	// Checkpoint.AckTimeout defaults to 60s — same as the queue-level preemption grace period.
	if r.Spec.Checkpoint.Enabled && r.Spec.Checkpoint.AckTimeout.Duration == 0 {
		r.Spec.Checkpoint.AckTimeout.Duration = 60_000_000_000 // 60s in nanoseconds
	}

	// Phase is initialized to Pending on creation if unset.
	if r.Status.Phase == "" {
		r.Status.Phase = WorkloadPhasePending
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Validator
// ──────────────────────────────────────────────────────────────────────────────

// +kubebuilder:webhook:path=/validate-meridian-dev-v1alpha1-gpuworkload,mutating=false,failurePolicy=fail,sideEffects=None,groups=meridian.dev,resources=gpuworkloads,verbs=create;update;delete,versions=v1alpha1,name=vgpuworkload.kb.io,admissionReviewVersions=v1

// gpuWorkloadValidator implements admission.Validator[*GPUWorkload].
type gpuWorkloadValidator struct{}

// Ensure gpuWorkloadValidator satisfies the Validator interface at compile time.
var _ admission.Validator[*GPUWorkload] = &gpuWorkloadValidator{}

// ValidateCreate validates a new GPUWorkload on creation.
func (v *gpuWorkloadValidator) ValidateCreate(ctx context.Context, r *GPUWorkload) (admission.Warnings, error) {
	gpuWorkloadLog.Info("validating GPUWorkload create", "name", r.Name, "namespace", r.Namespace)
	return validateGPUWorkload(r, nil)
}

// ValidateUpdate validates a GPUWorkload on update.
// oldR allows detecting immutable field mutations (e.g. queueName change).
func (v *gpuWorkloadValidator) ValidateUpdate(ctx context.Context, oldR, newR *GPUWorkload) (admission.Warnings, error) {
	gpuWorkloadLog.Info("validating GPUWorkload update", "name", newR.Name, "namespace", newR.Namespace)
	return validateGPUWorkload(newR, oldR)
}

// ValidateDelete validates a GPUWorkload on deletion.
// We allow deletion in any phase — the controller handles cleanup of running pods.
func (v *gpuWorkloadValidator) ValidateDelete(ctx context.Context, r *GPUWorkload) (admission.Warnings, error) {
	gpuWorkloadLog.Info("validating GPUWorkload delete", "name", r.Name, "namespace", r.Namespace)
	return nil, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared validation logic
// ──────────────────────────────────────────────────────────────────────────────

// validateGPUWorkload applies all semantic validation rules.
// old is nil on create, non-nil on update (used for immutability checks).
func validateGPUWorkload(r *GPUWorkload, old *GPUWorkload) (admission.Warnings, error) {
	var errs []error

	// Rule 1: QueueName is required and must be non-empty.
	if r.Spec.QueueName == "" {
		errs = append(errs, fmt.Errorf("spec.queueName: must be non-empty"))
	}

	// Rule 2: QueueName is immutable once set — changing the queue mid-flight would corrupt
	// quota accounting across two queues simultaneously.
	if old != nil && old.Spec.QueueName != "" && r.Spec.QueueName != old.Spec.QueueName {
		errs = append(errs, fmt.Errorf(
			"spec.queueName: immutable field cannot be changed from %q to %q",
			old.Spec.QueueName, r.Spec.QueueName,
		))
	}

	// Rule 3: Gang.Size must be at least 1.
	if r.Spec.Gang.Size < 1 {
		errs = append(errs, fmt.Errorf("spec.gang.size: must be >= 1, got %d", r.Spec.Gang.Size))
	}

	// Rule 4: Gang.MinAvailable must be between 1 and Gang.Size.
	if r.Spec.Gang.MinAvailable < 1 {
		errs = append(errs, fmt.Errorf("spec.gang.minAvailable: must be >= 1, got %d", r.Spec.Gang.MinAvailable))
	}
	if r.Spec.Gang.MinAvailable > r.Spec.Gang.Size {
		errs = append(errs, fmt.Errorf(
			"spec.gang.minAvailable: %d cannot exceed spec.gang.size %d",
			r.Spec.Gang.MinAvailable, r.Spec.Gang.Size,
		))
	}

	// Rule 5: GPUsPerPod must be positive.
	if r.Spec.GPUsPerPod < 1 {
		errs = append(errs, fmt.Errorf("spec.gpusPerPod: must be >= 1, got %d", r.Spec.GPUsPerPod))
	}

	// Rule 6: GPUType must be non-empty.
	if r.Spec.GPUType == "" {
		errs = append(errs, fmt.Errorf("spec.gpuType: must be non-empty"))
	}

	// Rule 7: Elastic bounds must be consistent when provided.
	if elastic := r.Spec.RestartPolicy.Elastic; elastic != nil {
		if elastic.MinSize < 1 {
			errs = append(errs, fmt.Errorf("spec.restartPolicy.elastic.minSize: must be >= 1, got %d", elastic.MinSize))
		}
		if elastic.MaxSize < elastic.MinSize {
			errs = append(errs, fmt.Errorf(
				"spec.restartPolicy.elastic.maxSize: %d must be >= minSize %d",
				elastic.MaxSize, elastic.MinSize,
			))
		}
		if elastic.MaxSize > r.Spec.Gang.Size {
			errs = append(errs, fmt.Errorf(
				"spec.restartPolicy.elastic.maxSize: %d cannot exceed spec.gang.size %d",
				elastic.MaxSize, r.Spec.Gang.Size,
			))
		}
	}

	// Rule 8: Topology.Required=true requires a non-None preference — a hard placement filter
	// on "None" would immediately reject all scheduling attempts.
	if r.Spec.Topology.Required && r.Spec.Topology.Preference == TopologyPreferenceNone {
		errs = append(errs, fmt.Errorf(
			"spec.topology: required=true is incompatible with preference=None; set a specific topology preference",
		))
	}

	// Rule 9: If checkpointing is enabled a storage path must be specified.
	// A checkpoint without a path would silently produce unreachable artifacts.
	if r.Spec.Checkpoint.Enabled && r.Spec.Checkpoint.Path == "" {
		errs = append(errs, fmt.Errorf(
			"spec.checkpoint.path: must be non-empty when checkpoint.enabled=true",
		))
	}

	return nil, joinErrors(errs)
}
