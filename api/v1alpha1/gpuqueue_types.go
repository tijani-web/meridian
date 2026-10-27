package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PreemptionPolicy defines preemption and reclaim behavior for workloads in this queue.
// This allows high-priority workloads to reclaim nominal quota from lower-priority jobs.
type PreemptionPolicy struct {
	// ReclaimWithinQueue allows higher-priority workloads to preempt lower-priority workloads
	// within the same queue when nominal capacity is exhausted.
	// +optional
	// +kubebuilder:default=true
	ReclaimWithinQueue bool `json:"reclaimWithinQueue"`

	// ReclaimBorrowed allows this queue to immediately reclaim nominal capacity that was
	// temporarily lent out to other queues as borrowed capacity.
	// +optional
	// +kubebuilder:default=true
	ReclaimBorrowed bool `json:"reclaimBorrowed"`

	// CheckpointGracePeriod is the maximum time given to a victim workload to complete
	// an in-flight checkpoint and acknowledge before being forcefully evicted.
	// +optional
	// +kubebuilder:default="60s"
	CheckpointGracePeriod metav1.Duration `json:"checkpointGracePeriod,omitempty"`
}

// GPUQueueSpec defines the desired quota, limits, and submission rules for a GPU tenant queue.
type GPUQueueSpec struct {
	// NamespaceSelector determines which namespaces are permitted to submit workloads to this queue.
	// An empty selector matches no namespaces; use matchLabels or matchExpressions.
	// +optional
	NamespaceSelector *metav1.LabelSelector `json:"namespaceSelector,omitempty"`

	// NominalQuota defines the guaranteed baseline number of GPUs by accelerator type
	// (e.g., "nvidia-h100": 16, "nvidia-a100": 8).
	// +kubebuilder:validation:MinProperties=1
	NominalQuota map[string]int32 `json:"nominalQuota"`

	// BorrowingLimit defines the maximum additional GPUs by accelerator type that this queue
	// may borrow from cluster-wide idle capacity beyond its nominal quota.
	// +optional
	BorrowingLimit map[string]int32 `json:"borrowingLimit,omitempty"`

	// LendingLimit defines the maximum nominal GPUs by accelerator type that this queue
	// permits other queues to borrow when idle. If omitted, all idle nominal GPUs may be lent.
	// +optional
	LendingLimit map[string]int32 `json:"lendingLimit,omitempty"`

	// Priority provides a deterministic tie-break ordering across queues with equal fair-share ratios.
	// Higher numbers indicate higher queue priority.
	// +optional
	// +kubebuilder:default=0
	Priority int32 `json:"priority,omitempty"`

	// Preemption specifies how and when workloads in this queue may reclaim quota or be preempted.
	// +optional
	Preemption PreemptionPolicy `json:"preemption,omitempty"`
}

// GPUUsageBreakdown tracks active GPU allocations for a specific GPU model.
type GPUUsageBreakdown struct {
	// Used is the total count of GPUs currently allocated to admitted workloads from this queue.
	// +kubebuilder:validation:Minimum=0
	Used int32 `json:"used"`

	// Borrowed is the count of GPUs currently used by this queue that exceed nominal quota.
	// +kubebuilder:validation:Minimum=0
	Borrowed int32 `json:"borrowed"`

	// Lent is the count of this queue's nominal GPUs currently allocated to other queues.
	// +kubebuilder:validation:Minimum=0
	Lent int32 `json:"lent"`
}

// GPUQueueStatus defines the observed quota consumption, admission stats, and conditions.
type GPUQueueStatus struct {
	// Usage provides a per-GPU-type breakdown of currently used, borrowed, and lent accelerators.
	// +optional
	Usage map[string]GPUUsageBreakdown `json:"usage,omitempty"`

	// PendingWorkloads is the number of submitted workloads currently waiting in this queue.
	// +optional
	// +kubebuilder:validation:Minimum=0
	PendingWorkloads int32 `json:"pendingWorkloads"`

	// AdmittedWorkloads is the number of workloads from this queue currently admitted and occupying quota.
	// +optional
	// +kubebuilder:validation:Minimum=0
	AdmittedWorkloads int32 `json:"admittedWorkloads"`

	// Conditions represent the current observations of the queue's state (e.g. QuotaSaturated, UnderFairShare).
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName=gq;gpuq
// +kubebuilder:printcolumn:name="Priority",type="integer",JSONPath=".spec.priority"
// +kubebuilder:printcolumn:name="Admitted",type="integer",JSONPath=".status.admittedWorkloads"
// +kubebuilder:printcolumn:name="Pending",type="integer",JSONPath=".status.pendingWorkloads"
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"

// GPUQueue is the Schema for cluster-scoped GPU tenant quotas, borrowing limits, and fair-share admission.
type GPUQueue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   GPUQueueSpec   `json:"spec,omitempty"`
	Status GPUQueueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// GPUQueueList contains a list of GPUQueue resources.
type GPUQueueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []GPUQueue `json:"items"`
}

func init() {
	SchemeBuilder.Register(&GPUQueue{}, &GPUQueueList{})
}
