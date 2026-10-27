package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadKind categorizes the GPU workload archetype for scheduler heuristics and telemetry.
// +kubebuilder:validation:Enum=Training;Eval;RL
type WorkloadKind string

const (
	WorkloadKindTraining WorkloadKind = "Training"
	WorkloadKindEval     WorkloadKind = "Eval"
	WorkloadKindRL       WorkloadKind = "RL"
)

// TopologyPreference specifies the hardware interconnect locality required or preferred for gang members.
// +kubebuilder:validation:Enum=SameNVLinkDomain;SameRack;SameZone;None
type TopologyPreference string

const (
	TopologyPreferenceSameNVLinkDomain TopologyPreference = "SameNVLinkDomain"
	TopologyPreferenceSameRack         TopologyPreference = "SameRack"
	TopologyPreferenceSameZone         TopologyPreference = "SameZone"
	TopologyPreferenceNone             TopologyPreference = "None"
)

// GPUWorkloadPhase represents the current lifecycle stage in the finite state machine.
// +kubebuilder:validation:Enum=Pending;Queued;Admitted;Scheduling;Running;Checkpointing;Preempting;Recovering;Succeeded;Failed
type GPUWorkloadPhase string

const (
	WorkloadPhasePending       GPUWorkloadPhase = "Pending"
	WorkloadPhaseQueued        GPUWorkloadPhase = "Queued"
	WorkloadPhaseAdmitted      GPUWorkloadPhase = "Admitted"
	WorkloadPhaseScheduling    GPUWorkloadPhase = "Scheduling"
	WorkloadPhaseRunning       GPUWorkloadPhase = "Running"
	WorkloadPhaseCheckpointing GPUWorkloadPhase = "Checkpointing"
	WorkloadPhasePreempting    GPUWorkloadPhase = "Preempting"
	WorkloadPhaseRecovering    GPUWorkloadPhase = "Recovering"
	WorkloadPhaseSucceeded     GPUWorkloadPhase = "Succeeded"
	WorkloadPhaseFailed        GPUWorkloadPhase = "Failed"
)

// GangSpec defines all-or-nothing or elastic gang scheduling parameters.
type GangSpec struct {
	// Size is the target total count of pods required for this gang.
	// +kubebuilder:validation:Minimum=1
	Size int32 `json:"size"`

	// MinAvailable is the minimum number of gang pods that must be simultaneously admitted
	// and reserved for the workload to proceed. If 0 or omitted, defaults to Size (all-or-nothing).
	// +optional
	// +kubebuilder:validation:Minimum=1
	MinAvailable int32 `json:"minAvailable,omitempty"`
}

// TopologySpec defines hardware placement constraints (NVLink domain, rack, or AZ).
type TopologySpec struct {
	// Preference denotes the desired locality boundary for gang members.
	// +kubebuilder:default=None
	Preference TopologyPreference `json:"preference,omitempty"`

	// Required enforces a strict hard placement filter when true. If false, treated as a scoring preference.
	// +optional
	// +kubebuilder:default=false
	Required bool `json:"required"`
}

// CheckpointSpec configures checkpoint-aware preemption and periodic saving.
type CheckpointSpec struct {
	// Enabled toggles whether this workload participates in cooperative checkpointing.
	// +optional
	// +kubebuilder:default=false
	Enabled bool `json:"enabled"`

	// Interval is the target cadence between automatic periodic checkpoint requests.
	// +optional
	Interval *metav1.Duration `json:"interval,omitempty"`

	// AckTimeout is the maximum time the controller waits for trainers to acknowledge
	// a checkpoint request before proceeding with forced eviction.
	// +optional
	// +kubebuilder:default="60s"
	AckTimeout metav1.Duration `json:"ackTimeout,omitempty"`

	// Path specifies the shared storage directory where checkpoint artifacts are saved.
	// +optional
	Path string `json:"path,omitempty"`
}

// ElasticSpec configures dynamic gang scaling boundaries upon recovery.
type ElasticSpec struct {
	// MinSize is the lowest acceptable gang pod count under capacity-constrained recovery.
	// +kubebuilder:validation:Minimum=1
	MinSize int32 `json:"minSize"`

	// MaxSize is the upper gang pod limit.
	// +kubebuilder:validation:Minimum=1
	MaxSize int32 `json:"maxSize"`
}

// RestartPolicySpec governs failure recovery and elastic resizing.
type RestartPolicySpec struct {
	// MaxRestarts defines the maximum number of restart generations permitted before failing.
	// +optional
	// +kubebuilder:default=3
	// +kubebuilder:validation:Minimum=0
	MaxRestarts int32 `json:"maxRestarts,omitempty"`

	// Backoff is the delay before re-admitting a failed or preempted workload.
	// +optional
	// +kubebuilder:default="10s"
	Backoff metav1.Duration `json:"backoff,omitempty"`

	// Elastic allows the workload to dynamically resize within [MinSize, MaxSize] on restart.
	// +optional
	Elastic *ElasticSpec `json:"elastic,omitempty"`
}

// GPUWorkloadSpec defines the gang size, GPU requests, pod template, and lifecycle policies.
type GPUWorkloadSpec struct {
	// QueueName references the cluster-scoped GPUQueue to which this workload requests admission.
	// +kubebuilder:validation:MinLength=1
	QueueName string `json:"queueName"`

	// PriorityClassName optionally specifies a Kubernetes PriorityClass for scheduling tie-breaks.
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// Kind categorizes the execution pattern (Training, Eval, or RL).
	// +kubebuilder:default=Training
	Kind WorkloadKind `json:"kind"`

	// Gang defines the gang scheduling group dimensions.
	Gang GangSpec `json:"gang"`

	// GPUsPerPod is the exact number of physical or virtual GPUs requested by each pod.
	// +kubebuilder:validation:Minimum=1
	GPUsPerPod int32 `json:"gpusPerPod"`

	// GPUType specifies the required accelerator hardware label (e.g., "nvidia-h100", "nvidia-a100").
	// +kubebuilder:validation:MinLength=1
	GPUType string `json:"gpuType"`

	// Topology defines hardware interconnect placement constraints.
	// +optional
	Topology TopologySpec `json:"topology,omitempty"`

	// PodTemplate describes the pods that the controller will create for each gang member.
	PodTemplate corev1.PodTemplateSpec `json:"podTemplate"`

	// Checkpoint configures cooperative checkpoint-before-evict behavior.
	// +optional
	Checkpoint CheckpointSpec `json:"checkpoint,omitempty"`

	// RestartPolicy defines max restarts and elastic resize rules.
	// +optional
	RestartPolicy RestartPolicySpec `json:"restartPolicy,omitempty"`
}

// PodAssignment records the placement of an individual gang member pod onto a physical node.
type PodAssignment struct {
	// Pod is the name of the created gang member pod.
	Pod string `json:"pod"`

	// Node is the name of the assigned node where the pod is bound.
	Node string `json:"node"`

	// GPUs lists the allocated GPU device identifiers or bus indices.
	// +optional
	GPUs []string `json:"gpus,omitempty"`
}

// TransitionRecord documents an audited FSM transition with causal reason and monotonic sequence.
type TransitionRecord struct {
	// Seq is a monotonically increasing sequence counter for causal ordering.
	Seq int64 `json:"seq"`

	// Time is the timestamp when the state transition occurred.
	Time metav1.Time `json:"time"`

	// From is the previous workload phase.
	From string `json:"from"`

	// To is the new workload phase.
	To string `json:"to"`

	// Reason is a CamelCase machine-readable classification of the transition.
	Reason string `json:"reason"`

	// Message is a human-readable explanation of the cause.
	Message string `json:"message"`
}

// GPUWorkloadStatus records the observed FSM phase, generation, assignments, history, and conditions.
type GPUWorkloadStatus struct {
	// Phase is the current lifecycle state of the workload FSM.
	// +optional
	// +kubebuilder:default=Pending
	Phase GPUWorkloadPhase `json:"phase,omitempty"`

	// Generation is incremented on every elastic restart or node-fault recovery cycle.
	// +optional
	// +kubebuilder:default=0
	Generation int64 `json:"generation,omitempty"`

	// AdmittedAt is the timestamp when the workload successfully passed queue admission.
	// +optional
	AdmittedAt *metav1.Time `json:"admittedAt,omitempty"`

	// StartedAt is the timestamp when all gang members transitioned to Running.
	// +optional
	StartedAt *metav1.Time `json:"startedAt,omitempty"`

	// LastCheckpointAt is the timestamp of the last acknowledged checkpoint.
	// +optional
	LastCheckpointAt *metav1.Time `json:"lastCheckpointAt,omitempty"`

	// Assignment lists node and device placement for all active gang member pods.
	// +optional
	Assignment []PodAssignment `json:"assignment,omitempty"`

	// History retains a bounded, chronologically ordered sequence of state transitions.
	// +optional
	History []TransitionRecord `json:"history,omitempty"`

	// Conditions represent the current observations of the workload's state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=gw;gpuw
// +kubebuilder:printcolumn:name="Phase",type="string",JSONPath=".status.phase"
// +kubebuilder:printcolumn:name="Queue",type="string",JSONPath=".spec.queueName"
// +kubebuilder:printcolumn:name="Gang",type="integer",JSONPath=".spec.gang.size"
// +kubebuilder:printcolumn:name="GPUs",type="integer",JSONPath=".spec.gpusPerPod"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// GPUWorkload is the Schema for namespaced GPU training, evaluation, and RL gang jobs.
type GPUWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUWorkloadSpec   `json:"spec,omitempty"`
	Status GPUWorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUWorkloadList contains a list of GPUWorkload resources.
type GPUWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUWorkload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUWorkload{}, &GPUWorkloadList{})
}
