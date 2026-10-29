/*
Package inference implements the Kubernetes controller for Meridian InferenceDeployment CRDs (§6.6).

It reconciles InferenceDeployment resources into per-revision Kubernetes Deployments
and Services, schedules pods using the GPU-aware meridian-scheduler, enforces
warm-up gating, drives progressive canary rollouts, and publishes the active
routing table to the inference router data plane.
*/
package inference

const (
	// FinalizerName ensures cleanup of child Deployments, Services, and routing ConfigMaps.
	FinalizerName = "meridian.dev/inference-finalizer"

	// LabelInferenceDeployment identifies the parent InferenceDeployment name.
	LabelInferenceDeployment = "meridian.dev/inference-deployment"

	// LabelRevision identifies the specific model revision name.
	LabelRevision = "meridian.dev/revision"

	// LabelWarm indicates whether a replica pod has passed warm-up gating.
	LabelWarm = "meridian.dev/warm"

	// AnnotationCanaryStep records the currently active step index in the canary rollout.
	AnnotationCanaryStep = "meridian.dev/canary-step"

	// AnnotationRollbackReason records why an automated canary rollback was triggered.
	AnnotationRollbackReason = "meridian.dev/rollback-reason"

	// FieldManagerName is the Server-Side Apply manager string.
	FieldManagerName = "meridian-inference-controller"

	// RoutingConfigMapSuffix is the naming pattern for the routing table ConfigMap.
	RoutingConfigMapSuffix = "-routing-table"
)
