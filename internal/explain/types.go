/*
Package explain implements the diagnostic explainability engine for Meridian.

It inspects live Kubernetes resources (GPUWorkload, GPUQueue, Nodes, Pods, and
Events) to provide actionable, structured, human-readable breakdowns for:
  - `meridianctl explain workload <name> -n <ns>`
  - `meridianctl explain inference <name> -n <ns>`
  - `meridianctl status cluster`
*/
package explain

import (
	"time"
)

// WorkloadExplanation answers §6.8 explainability questions in strict order.
type WorkloadExplanation struct {
	Name          string        `json:"name"`
	Namespace     string        `json:"namespace"`
	Phase         string        `json:"phase"`
	PhaseDuration time.Duration `json:"phaseDuration"`

	// 1. Queue Status
	Queue QueueExplanation `json:"queue"`

	// 2. Gate Status
	Gated GatedExplanation `json:"gated"`

	// 3. Scheduling Analysis
	Scheduling SchedulingExplanation `json:"scheduling"`

	// 4. Permit Status
	Permit PermitExplanation `json:"permit"`

	// 5. Preempted / Recovering History
	History []HistoryExplanation `json:"history"`

	// Active Pods
	Pods []PodPlacement `json:"pods"`
}

// QueueExplanation describes queue quota and blocking workloads.
type QueueExplanation struct {
	QueueName        string   `json:"queueName"`
	Position         string   `json:"position"`
	QuotaAllocated   int64    `json:"quotaAllocated"`
	QuotaGuaranteed  int64    `json:"quotaGuaranteed"`
	QuotaCeiling     int64    `json:"quotaCeiling"`
	IsBlocked        bool     `json:"isBlocked"`
	BlockReason      string   `json:"blockReason,omitempty"`
	HoldingWorkloads []string `json:"holdingWorkloads,omitempty"`
}

// GatedExplanation details why a workload's pods are held by scheduling gates.
type GatedExplanation struct {
	IsGated bool   `json:"isGated"`
	Gate    string `json:"gate,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// CandidateScore captures the sub-score breakdown for candidate nodes.
type CandidateScore struct {
	Node           string `json:"node"`
	FitScore       int64  `json:"fitScore"`
	TopoScore      int64  `json:"topoScore"`
	HealthScore    int64  `json:"healthScore"`
	CompositeScore int64  `json:"compositeScore"`
}

// SchedulingExplanation summarizes per-node rejection reasons and candidate scores.
type SchedulingExplanation struct {
	IsScheduling     bool                      `json:"isScheduling"`
	RejectionsByNode map[string]int            `json:"rejectionsByNode"` // reason -> node count
	CandidateScores  []CandidateScore          `json:"candidateScores"`
	FailureSummary   string                    `json:"failureSummary,omitempty"`
}

// PermitExplanation describes gang scheduling reservation status.
type PermitExplanation struct {
	IsPermitWaiting bool          `json:"isPermitWaiting"`
	Reserved        int           `json:"reserved"`
	Required        int           `json:"required"`
	RemainingTime   time.Duration `json:"remainingTime"`
}

// HistoryExplanation captures causal ordering of lifecycle events.
type HistoryExplanation struct {
	Seq       int64     `json:"seq"`
	Phase     string    `json:"phase"`
	Reason    string    `json:"reason"`
	Message   string    `json:"message"`
	Timestamp time.Time `json:"timestamp"`
}

// PodPlacement details current pod assignments.
type PodPlacement struct {
	Name      string `json:"name"`
	GangIndex int    `json:"gangIndex"`
	Node      string `json:"node,omitempty"`
	Status    string `json:"status"`
}

// ClusterStatus answers `meridianctl status cluster`.
type ClusterStatus struct {
	TotalGPUs          int64              `json:"totalGPUs"`
	AllocatedGPUs      int64              `json:"allocatedGPUs"`
	AvailableGPUs      int64              `json:"availableGPUs"`
	GPUUtilizationPct  float64            `json:"gpuUtilizationPct"`
	FragmentationRatio float64            `json:"fragmentationRatio"`
	HealthyNodes       int                `json:"healthyNodes"`
	UnhealthyNodes     int                `json:"unhealthyNodes"`
	Queues             []QueueStatusEntry `json:"queues"`
}

// QueueStatusEntry is a row in the cluster queues summary.
type QueueStatusEntry struct {
	Name       string `json:"name"`
	Priority   int32  `json:"priority"`
	Allocated  int64  `json:"allocated"`
	Guaranteed int64  `json:"guaranteed"`
	Ceiling    int64  `json:"ceiling"`
	Pending    int    `json:"pending"`
}

// InferenceExplanation answers `meridianctl explain inference <name>`.
type InferenceExplanation struct {
	Name            string                 `json:"name"`
	Namespace       string                 `json:"namespace"`
	Model           string                 `json:"model"`
	Revisions       []InferenceRevision    `json:"revisions"`
	CanaryActive    bool                   `json:"canaryActive"`
	CanaryWeight    int32                  `json:"canaryWeight"`
	WarmupGating    string                 `json:"warmupGating"`
	TotalReplicas   int32                  `json:"totalReplicas"`
	HealthyReplicas int32                  `json:"healthyReplicas"`
	Endpoints       []string               `json:"endpoints"`
}

// InferenceRevision describes a deployed revision.
type InferenceRevision struct {
	Revision string `json:"revision"`
	Weight   int32  `json:"weight"`
	Replicas int32  `json:"replicas"`
	Ready    int32  `json:"ready"`
}
