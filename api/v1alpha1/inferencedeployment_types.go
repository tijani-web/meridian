package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// RolloutStrategy defines how traffic shifts between revisions during model updates.
// +kubebuilder:validation:Enum=Canary;BlueGreen
type RolloutStrategy string

const (
	RolloutStrategyCanary    RolloutStrategy = "Canary"
	RolloutStrategyBlueGreen RolloutStrategy = "BlueGreen"
)

// RoutingStrategy denotes the request distribution algorithm used by the inference router data plane.
// +kubebuilder:validation:Enum=LeastQueue;KVAware;PrefixAffinity;RoundRobin
type RoutingStrategy string

const (
	RoutingStrategyLeastQueue     RoutingStrategy = "LeastQueue"
	RoutingStrategyKVAware        RoutingStrategy = "KVAware"
	RoutingStrategyPrefixAffinity RoutingStrategy = "PrefixAffinity"
	RoutingStrategyRoundRobin     RoutingStrategy = "RoundRobin"
)

// ModelSpec defines the model artifact weights and source repository.
type ModelSpec struct {
	// Name identifies the human-readable model identifier (e.g. "llama-3-8b-instruct").
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`

	// URI points to the cloud storage bucket, huggingface repo, or PVC containing weights.
	// +kubebuilder:validation:MinLength=1
	URI string `json:"uri"`

	// Revision specifies the git commit, tag, or version pin of the weights.
	// +optional
	// +kubebuilder:default="main"
	Revision string `json:"revision,omitempty"`
}

// RuntimeSpec specifies the container image, entrypoint arguments, and ports for the model server.
type RuntimeSpec struct {
	// Image is the container image for the inference server engine (e.g. vLLM or vllm-emu).
	// +kubebuilder:validation:MinLength=1
	Image string `json:"image"`

	// Args provides CLI flags passed to the model server executable.
	// +optional
	Args []string `json:"args,omitempty"`

	// Port is the HTTP port exposed by the model server for OpenAI-compatible completions.
	// +kubebuilder:default=8000
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65535
	Port int32 `json:"port"`

	// MetricsPath is the HTTP path returning Prometheus format engine metrics (e.g. /metrics).
	// +optional
	// +kubebuilder:default="/metrics"
	MetricsPath string `json:"metricsPath,omitempty"`
}

// InferenceResourceSpec defines hardware accelerator requirements for serving replicas.
type InferenceResourceSpec struct {
	// GPUsPerReplica is the count of physical GPUs needed per replica.
	// +kubebuilder:validation:Minimum=0
	GPUsPerReplica int32 `json:"gpusPerReplica"`

	// GPUType specifies the required accelerator hardware label (e.g., "nvidia-h100").
	// +optional
	GPUType string `json:"gpuType,omitempty"`
}

// RolloutAnalysisSpec configures automated metric verification before promoting canary steps.
type RolloutAnalysisSpec struct {
	// MaxErrorRate is the maximum allowable fractional error rate (e.g. 0.01 for 1%).
	// +optional
	MaxErrorRate *string `json:"maxErrorRate,omitempty"`

	// MaxTTFTMillis is the upper bound on 99th percentile Time-To-First-Token in milliseconds.
	// +optional
	MaxTTFTMillis *int32 `json:"maxTTFTMillis,omitempty"`
}

// RolloutStep defines an individual traffic shift weight and soak duration.
type RolloutStep struct {
	// Weight is the percentage of total traffic allocated to the new canary revision (0-100).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Weight int32 `json:"weight"`

	// PauseSeconds is the duration to soak at this weight before evaluating analysis and advancing.
	// +optional
	// +kubebuilder:default=30
	PauseSeconds int32 `json:"pauseSeconds,omitempty"`

	// Analysis defines the automated promotion criteria evaluated during the pause.
	// +optional
	Analysis *RolloutAnalysisSpec `json:"analysis,omitempty"`
}

// RolloutSpec specifies canary or blue-green progression parameters.
type RolloutSpec struct {
	// Strategy determines how revisions are phased in (Canary or BlueGreen).
	// +kubebuilder:default=Canary
	Strategy RolloutStrategy `json:"strategy"`

	// Steps specifies the progressive weight stages for a Canary rollout.
	// +optional
	Steps []RolloutStep `json:"steps,omitempty"`

	// MaxUnavailable defines the maximum number or percentage of unavailable replicas during updates.
	// +optional
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
}

// WarmupSpec ensures replicas are not routed user traffic until KV cache and weights are warm.
type WarmupSpec struct {
	// ReadinessPromptPath points to an HTTP endpoint or test prompt to exercise the model engine.
	// +optional
	ReadinessPromptPath string `json:"readinessPromptPath,omitempty"`

	// Probe specifies a standard Kubernetes readiness probe for initial container startup.
	// +optional
	Probe *corev1.Probe `json:"probe,omitempty"`

	// MinWarmRequests requires this number of synthetic warmup requests to succeed before marking warm.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=0
	MinWarmRequests int32 `json:"minWarmRequests,omitempty"`
}

// LoadSheddingSpec configures admission shedding when replica queues saturate.
type LoadSheddingSpec struct {
	// MaxQueueDepth is the aggregate queue threshold beyond which new requests are shed with HTTP 429.
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=1
	MaxQueueDepth int32 `json:"maxQueueDepth"`

	// PriorityClasses lists priority tiers eligible to bypass load shedding under pressure.
	// +optional
	PriorityClasses []string `json:"priorityClasses,omitempty"`
}

// RoutingSpec configures data-plane routing algorithms and capacity management.
type RoutingSpec struct {
	// Strategy denotes the algorithm (LeastQueue, KVAware, PrefixAffinity, RoundRobin).
	// +kubebuilder:default=LeastQueue
	Strategy RoutingStrategy `json:"strategy"`

	// LoadShedding configures HTTP 429 backpressure parameters.
	// +optional
	LoadShedding *LoadSheddingSpec `json:"loadShedding,omitempty"`
}

// InferenceDeploymentSpec defines model, engine runtime, replica count, canary rollout, and routing.
type InferenceDeploymentSpec struct {
	// Model defines the weights location and revision.
	Model ModelSpec `json:"model"`

	// Runtime defines the container image and serving arguments.
	Runtime RuntimeSpec `json:"runtime"`

	// Replicas is the desired number of serving pods.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:default=1
	Replicas int32 `json:"replicas"`

	// Resources specifies per-replica GPU hardware requirements.
	// +optional
	Resources InferenceResourceSpec `json:"resources,omitempty"`

	// Rollout governs progressive deployment steps and metric verification.
	// +optional
	Rollout RolloutSpec `json:"rollout,omitempty"`

	// Warmup gates routing until replicas pass warm-up queries.
	// +optional
	Warmup WarmupSpec `json:"warmup,omitempty"`

	// Routing specifies algorithm and load shedding thresholds for the Meridian router.
	// +optional
	Routing RoutingSpec `json:"routing,omitempty"`
}

// RevisionStatus tracks the runtime readiness and traffic share of an active model revision.
type RevisionStatus struct {
	// Name is the unique revision name (derived from deployment name and revision hash).
	Name string `json:"name"`

	// Weight is the current percentage of incoming requests routed to this revision.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=100
	Weight int32 `json:"weight"`

	// ReadyReplicas is the count of serving pods currently passing readiness checks.
	// +kubebuilder:validation:Minimum=0
	ReadyReplicas int32 `json:"readyReplicas"`

	// Warm indicates whether the revision has satisfied warmup probe requirements.
	Warm bool `json:"warm"`
}

// InferenceDeploymentStatus defines observed revisions, routing table states, and conditions.
type InferenceDeploymentStatus struct {
	// Revisions lists all active model revisions and their assigned traffic weights.
	// +optional
	Revisions []RevisionStatus `json:"revisions,omitempty"`

	// ObservedGeneration reflects the most recent metadata generation observed by the controller.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions represent observations of the deployment's status (e.g. Available, Progressing).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=id;infdep
// +kubebuilder:printcolumn:name="Model",type="string",JSONPath=".spec.model.name"
// +kubebuilder:printcolumn:name="Replicas",type="integer",JSONPath=".spec.replicas"
// +kubebuilder:printcolumn:name="Strategy",type="string",JSONPath=".spec.routing.strategy"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// InferenceDeployment is the Schema for namespaced GPU model serving rollouts and intelligent routing.
type InferenceDeployment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InferenceDeploymentSpec   `json:"spec,omitempty"`
	Status InferenceDeploymentStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// InferenceDeploymentList contains a list of InferenceDeployment resources.
type InferenceDeploymentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InferenceDeployment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InferenceDeployment{}, &InferenceDeploymentList{})
}
