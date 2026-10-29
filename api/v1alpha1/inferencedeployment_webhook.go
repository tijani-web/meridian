package v1alpha1

import (
	"context"
	"fmt"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// inferenceDeploymentLog is the logger for the InferenceDeployment webhook.
var inferenceDeploymentLog = logf.Log.WithName("inferencedeployment-webhook")

// SetupWebhookWithManager registers the defaulting and validating webhooks for InferenceDeployment
// with the controller-runtime Manager.
func (r *InferenceDeployment) SetupWebhookWithManager(mgr ctrl.Manager) error {
	return builder.WebhookManagedBy[*InferenceDeployment](mgr, &InferenceDeployment{}).
		WithDefaulter(&inferenceDeploymentDefaulter{}).
		WithValidator(&inferenceDeploymentValidator{}).
		Complete()
}

// ──────────────────────────────────────────────────────────────────────────────
// Defaulter
// ──────────────────────────────────────────────────────────────────────────────

// +kubebuilder:webhook:path=/mutate-meridian-dev-v1alpha1-inferencedeployment,mutating=true,failurePolicy=fail,sideEffects=None,groups=meridian.dev,resources=inferencedeployments,verbs=create;update,versions=v1alpha1,name=minferencedeployment.kb.io,admissionReviewVersions=v1

// inferenceDeploymentDefaulter implements admission.Defaulter[*InferenceDeployment].
type inferenceDeploymentDefaulter struct{}

// Ensure inferenceDeploymentDefaulter satisfies the Defaulter interface at compile time.
var _ admission.Defaulter[*InferenceDeployment] = &inferenceDeploymentDefaulter{}

// Default applies sensible defaults to an InferenceDeployment before it is persisted.
func (d *inferenceDeploymentDefaulter) Default(ctx context.Context, r *InferenceDeployment) error {
	inferenceDeploymentLog.Info("defaulting InferenceDeployment", "name", r.Name, "namespace", r.Namespace)

	// Replicas defaults to 1 (belt-and-suspenders alongside +kubebuilder:default).
	if r.Spec.Replicas == 0 {
		r.Spec.Replicas = 1
	}

	// Model.Revision defaults to "main" if not specified.
	if r.Spec.Model.Revision == "" {
		r.Spec.Model.Revision = "main"
	}

	// Runtime.MetricsPath defaults to "/metrics".
	if r.Spec.Runtime.MetricsPath == "" {
		r.Spec.Runtime.MetricsPath = "/metrics"
	}

	// Routing.Strategy defaults to LeastQueue — best for heterogeneous token-length workloads.
	if r.Spec.Routing.Strategy == "" {
		r.Spec.Routing.Strategy = RoutingStrategyLeastQueue
	}

	// Rollout.Strategy defaults to Canary for safe progressive delivery.
	if r.Spec.Rollout.Strategy == "" {
		r.Spec.Rollout.Strategy = RolloutStrategyCanary
	}

	// Warmup.MinWarmRequests defaults to 1 to ensure at least one synthetic warmup pass.
	if r.Spec.Warmup.MinWarmRequests == 0 {
		r.Spec.Warmup.MinWarmRequests = 1
	}

	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Validator
// ──────────────────────────────────────────────────────────────────────────────

// +kubebuilder:webhook:path=/validate-meridian-dev-v1alpha1-inferencedeployment,mutating=false,failurePolicy=fail,sideEffects=None,groups=meridian.dev,resources=inferencedeployments,verbs=create;update;delete,versions=v1alpha1,name=vinferencedeployment.kb.io,admissionReviewVersions=v1

// inferenceDeploymentValidator implements admission.Validator[*InferenceDeployment].
type inferenceDeploymentValidator struct{}

// Ensure inferenceDeploymentValidator satisfies the Validator interface at compile time.
var _ admission.Validator[*InferenceDeployment] = &inferenceDeploymentValidator{}

// ValidateCreate validates a new InferenceDeployment on creation.
func (v *inferenceDeploymentValidator) ValidateCreate(ctx context.Context, r *InferenceDeployment) (admission.Warnings, error) {
	inferenceDeploymentLog.Info("validating InferenceDeployment create", "name", r.Name, "namespace", r.Namespace)
	return validateInferenceDeployment(r, nil)
}

// ValidateUpdate validates an InferenceDeployment on update.
func (v *inferenceDeploymentValidator) ValidateUpdate(ctx context.Context, oldR, newR *InferenceDeployment) (admission.Warnings, error) {
	inferenceDeploymentLog.Info("validating InferenceDeployment update", "name", newR.Name, "namespace", newR.Namespace)
	return validateInferenceDeployment(newR, oldR)
}

// ValidateDelete validates an InferenceDeployment on deletion.
func (v *inferenceDeploymentValidator) ValidateDelete(ctx context.Context, r *InferenceDeployment) (admission.Warnings, error) {
	inferenceDeploymentLog.Info("validating InferenceDeployment delete", "name", r.Name, "namespace", r.Namespace)
	return nil, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// Shared validation logic
// ──────────────────────────────────────────────────────────────────────────────

// validateInferenceDeployment applies all semantic validation rules.
func validateInferenceDeployment(r *InferenceDeployment, old *InferenceDeployment) (admission.Warnings, error) {
	var errs []error

	// Rule 1: Model.Name must be non-empty.
	if r.Spec.Model.Name == "" {
		errs = append(errs, fmt.Errorf("spec.model.name: must be non-empty"))
	}

	// Rule 2: Model.URI must be non-empty — without a weights location the deployment cannot start.
	if r.Spec.Model.URI == "" {
		errs = append(errs, fmt.Errorf("spec.model.uri: must be non-empty"))
	}

	// Rule 3: Runtime.Image must be non-empty.
	if r.Spec.Runtime.Image == "" {
		errs = append(errs, fmt.Errorf("spec.runtime.image: must be non-empty"))
	}

	// Rule 4: Replicas must be non-negative.
	if r.Spec.Replicas < 0 {
		errs = append(errs, fmt.Errorf("spec.replicas: must be >= 0, got %d", r.Spec.Replicas))
	}

	// Rule 5: Runtime.Port must be in valid TCP range.
	if r.Spec.Runtime.Port < 1 || r.Spec.Runtime.Port > 65535 {
		errs = append(errs, fmt.Errorf(
			"spec.runtime.port: must be in [1, 65535], got %d", r.Spec.Runtime.Port,
		))
	}

	// Rule 6: Canary rollout steps must have monotonically increasing weights ending at ≤ 100.
	// A non-monotonic step sequence would cause traffic routing to flip back — undefined behavior.
	if r.Spec.Rollout.Strategy == RolloutStrategyCanary && len(r.Spec.Rollout.Steps) > 0 {
		prev := int32(0)
		for i, step := range r.Spec.Rollout.Steps {
			if step.Weight < 0 || step.Weight > 100 {
				errs = append(errs, fmt.Errorf(
					"spec.rollout.steps[%d].weight: must be in [0, 100], got %d", i, step.Weight,
				))
			}
			if step.Weight < prev {
				errs = append(errs, fmt.Errorf(
					"spec.rollout.steps[%d].weight: %d must be >= previous step weight %d (steps must be monotonically increasing)",
					i, step.Weight, prev,
				))
			}
			prev = step.Weight
		}
	}

	// Rule 7: LoadShedding.MaxQueueDepth must be positive if load shedding is configured.
	if r.Spec.Routing.LoadShedding != nil {
		if r.Spec.Routing.LoadShedding.MaxQueueDepth < 1 {
			errs = append(errs, fmt.Errorf(
				"spec.routing.loadShedding.maxQueueDepth: must be >= 1, got %d",
				r.Spec.Routing.LoadShedding.MaxQueueDepth,
			))
		}
	}

	// Rule 8: Model.URI is immutable — changing the weights location requires creating a new revision,
	// not mutating the existing deployment's model source.
	if old != nil && old.Spec.Model.URI != "" && r.Spec.Model.URI != old.Spec.Model.URI {
		return nil, fmt.Errorf(
			"spec.model.uri: immutable field cannot be changed from %q to %q; create a new InferenceDeployment instead",
			old.Spec.Model.URI, r.Spec.Model.URI,
		)
	}

	return nil, joinErrors(errs)
}
