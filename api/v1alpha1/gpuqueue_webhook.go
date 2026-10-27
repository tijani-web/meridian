package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// gpuQueueLog is the logger for the GPUQueue webhook, scoped to this package.
// Each webhook handler uses a named, scoped logger — never the root logger.
var gpuQueueLog = logf.Log.WithName("gpuqueue-webhook")

// SetupWebhookWithManager registers the defaulting and validating webhooks for GPUQueue
// with the controller-runtime Manager so that the webhook server mux is wired correctly.
func (r *GPUQueue) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy[*GPUQueue](mgr, &GPUQueue{}).
		WithDefaulter(&gpuQueueDefaulter{}).
		WithValidator(&gpuQueueValidator{}).
		Complete()
}

// ──────────────────────────────────────────────────────────────────────────────
// Defaulter
// ──────────────────────────────────────────────────────────────────────────────

// +kubebuilder:webhook:path=/mutate-meridian-dev-v1alpha1-gpuqueue,mutating=true,failurePolicy=fail,sideEffects=None,groups=meridian.dev,resources=gpuqueues,verbs=create;update,versions=v1alpha1,name=mgpuqueue.kb.io,admissionReviewVersions=v1

// gpuQueueDefaulter implements admission.Defaulter[*GPUQueue].
// It is intentionally a separate struct from GPUQueue so that the type's
// own methods remain free for business logic and are not coupled to the
// webhook framework interface set.
type gpuQueueDefaulter struct{}

// Ensure gpuQueueDefaulter satisfies the Defaulter interface at compile time.
var _ admission.Defaulter[*GPUQueue] = &gpuQueueDefaulter{}

// Default applies sensible defaults to a GPUQueue before it is persisted.
// Controller-runtime calls this on CREATE and UPDATE before schema validation.
func (d *gpuQueueDefaulter) Default(ctx context.Context, q *GPUQueue) error {
	gpuQueueLog.Info("defaulting GPUQueue", "name", q.Name)

	// Preemption: enable both reclaim modes by default so queues can reclaim
	// capacity without requiring operators to explicitly opt-in on every queue.
	if !q.Spec.Preemption.ReclaimWithinQueue {
		q.Spec.Preemption.ReclaimWithinQueue = true
	}
	if !q.Spec.Preemption.ReclaimBorrowed {
		q.Spec.Preemption.ReclaimBorrowed = true
	}

	// CheckpointGracePeriod defaults to 60s; Duration zero-value means not set.
	if q.Spec.Preemption.CheckpointGracePeriod.Duration == 0 {
		q.Spec.Preemption.CheckpointGracePeriod.Duration = 60_000_000_000 // 60s in nanoseconds
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Validator
// ──────────────────────────────────────────────────────────────────────────────

// +kubebuilder:webhook:path=/validate-meridian-dev-v1alpha1-gpuqueue,mutating=false,failurePolicy=fail,sideEffects=None,groups=meridian.dev,resources=gpuqueues,verbs=create;update;delete,versions=v1alpha1,name=vgpuqueue.kb.io,admissionReviewVersions=v1

// gpuQueueValidator implements admission.Validator[*GPUQueue].
type gpuQueueValidator struct{}

// Ensure gpuQueueValidator satisfies the Validator interface at compile time.
var _ admission.Validator[*GPUQueue] = &gpuQueueValidator{}

// ValidateCreate validates a new GPUQueue on creation.
func (v *gpuQueueValidator) ValidateCreate(ctx context.Context, q *GPUQueue) (admission.Warnings, error) {
	gpuQueueLog.Info("validating GPUQueue create", "name", q.Name)
	return validateGPUQueue(q)
}

// ValidateUpdate validates a GPUQueue on update.
// oldQ is the previous persisted state; used to guard immutable fields.
func (v *gpuQueueValidator) ValidateUpdate(ctx context.Context, oldQ, newQ *GPUQueue) (admission.Warnings, error) {
	gpuQueueLog.Info("validating GPUQueue update", "name", newQ.Name)
	return validateGPUQueue(newQ)
}

// ValidateDelete validates a GPUQueue on deletion.
// Deletion is rejected while admitted workloads remain to prevent orphaned quota holders.
func (v *gpuQueueValidator) ValidateDelete(ctx context.Context, q *GPUQueue) (admission.Warnings, error) {
	gpuQueueLog.Info("validating GPUQueue delete", "name", q.Name)
	if q.Status.AdmittedWorkloads > 0 {
		return nil, fmt.Errorf(
			"GPUQueue %q cannot be deleted while %d workload(s) are admitted; drain the queue first",
			q.Name, q.Status.AdmittedWorkloads,
		)
	}
	return nil, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared validation logic
// ──────────────────────────────────────────────────────────────────────────────

// validateGPUQueue applies all semantic validation rules shared between create and update.
// Rules are factored here so they are testable in isolation via envtest table-driven tests.
func validateGPUQueue(q *GPUQueue) (admission.Warnings, error) {
	var errs []error

	// Rule 1: NominalQuota must have at least one GPU type with a non-negative count.
	if len(q.Spec.NominalQuota) == 0 {
		errs = append(errs, fmt.Errorf("spec.nominalQuota: must specify at least one GPU type"))
	}
	for gpuType, count := range q.Spec.NominalQuota {
		if count < 0 {
			errs = append(errs, fmt.Errorf("spec.nominalQuota[%q]: count must be >= 0, got %d", gpuType, count))
		}
	}

	// Rule 2: BorrowingLimit must not reference GPU types absent from NominalQuota.
	// Borrowing beyond nominal for unknown types would silently bypass quota enforcement.
	for gpuType := range q.Spec.BorrowingLimit {
		if _, ok := q.Spec.NominalQuota[gpuType]; !ok {
			errs = append(errs, fmt.Errorf(
				"spec.borrowingLimit[%q]: GPU type not present in spec.nominalQuota", gpuType,
			))
		}
	}

	// Rule 3: LendingLimit must not exceed NominalQuota for any GPU type.
	// Lending more than nominal is mathematically impossible and indicates a misconfiguration.
	for gpuType, lendLimit := range q.Spec.LendingLimit {
		if nominal, ok := q.Spec.NominalQuota[gpuType]; ok {
			if lendLimit > nominal {
				errs = append(errs, fmt.Errorf(
					"spec.lendingLimit[%q]: %d exceeds nominalQuota %d", gpuType, lendLimit, nominal,
				))
			}
		} else {
			errs = append(errs, fmt.Errorf(
				"spec.lendingLimit[%q]: GPU type not present in spec.nominalQuota", gpuType,
			))
		}
	}

	return nil, joinErrors(errs)
}
