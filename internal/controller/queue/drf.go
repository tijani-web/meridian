package queue

import (
	"sort"
	"time"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

// AdmissionDecision represents the outcome of evaluating a workload for quota admission.
type AdmissionDecision int

const (
	// DecisionReject indicates the workload cannot be admitted (e.g., exceeds nominal + borrowable capacity).
	DecisionReject AdmissionDecision = iota

	// DecisionAdmitNominal indicates the workload fits within the queue's remaining nominal quota.
	DecisionAdmitNominal

	// DecisionAdmitBorrowed indicates the workload is admitted using idle capacity borrowed from other queues.
	DecisionAdmitBorrowed

	// DecisionReclaimNeeded indicates nominal capacity is needed and victim workloads must be preempted.
	DecisionReclaimNeeded
)

// QueueState encapsulates the current mathematical state of a single queue for admission evaluation.
type QueueState struct {
	Name           string
	Priority       int32
	NominalQuota   map[string]int32
	BorrowingLimit map[string]int32
	LendingLimit   map[string]int32
	NominalUsed    map[string]int32
	BorrowedUsed   map[string]int32
}

// WorkloadCandidate represents a workload requesting admission or a potential preemption victim.
type WorkloadCandidate struct {
	Name         string
	Namespace    string
	QueueName    string
	GPUType      string
	TotalGPUs    int32
	Priority     int32
	IsBorrowed   bool
	AdmittedAt   time.Time
	DominantRate float64
}

// CalculateWorkloadGPUs computes the total GPU requirement for a workload gang.
func CalculateWorkloadGPUs(w *meridianv1alpha1.GPUWorkload) int32 {
	size := w.Spec.Gang.Size
	if size < 1 {
		size = 1
	}
	gpus := w.Spec.GPUsPerPod
	if gpus < 1 {
		gpus = 1
	}
	return size * gpus
}

// ComputeDominantShare calculates the DRF normalized dominant share for a queue.
// s_i = max_{a} (U_i(a) / C(a)) normalized by weight w_i = max_{a} (N_i(a) / C(a)).
func ComputeDominantShare(q *QueueState, clusterCapacity map[string]int32) float64 {
	var maxShare float64
	var maxWeight float64

	for gpuType, totalCap := range clusterCapacity {
		if totalCap <= 0 {
			continue
		}
		used := q.NominalUsed[gpuType] + q.BorrowedUsed[gpuType]
		share := float64(used) / float64(totalCap)
		if share > maxShare {
			maxShare = share
		}

		nominal := q.NominalQuota[gpuType]
		weight := float64(nominal) / float64(totalCap)
		if weight > maxWeight {
			maxWeight = weight
		}
	}

	if maxWeight <= 0 {
		return maxShare
	}
	return maxShare / maxWeight
}

// EvaluateAdmission determines whether a workload can be admitted under nominal or borrowed capacity.
func EvaluateAdmission(
	q *QueueState,
	gpuType string,
	requestedGPUs int32,
	clusterIdleCapacity map[string]int32,
) (AdmissionDecision, int32) {
	nominal := q.NominalQuota[gpuType]
	nomUsed := q.NominalUsed[gpuType]
	remainingNominal := nominal - nomUsed
	if remainingNominal < 0 {
		remainingNominal = 0
	}

	// 1. Fits entirely within nominal quota
	if requestedGPUs <= remainingNominal {
		return DecisionAdmitNominal, 0
	}

	// 2. Requires borrowing
	deficit := requestedGPUs - remainingNominal
	borrowLimit := q.BorrowingLimit[gpuType]
	borrowUsed := q.BorrowedUsed[gpuType]
	remainingBorrow := borrowLimit - borrowUsed
	if remainingBorrow < 0 {
		remainingBorrow = 0
	}

	clusterIdle := clusterIdleCapacity[gpuType]
	if clusterIdle < 0 {
		clusterIdle = 0
	}

	if deficit <= remainingBorrow && deficit <= clusterIdle {
		return DecisionAdmitBorrowed, deficit
	}

	// 3. Check if queue has enough total nominal quota to justify reclaiming
	if requestedGPUs <= nominal {
		return DecisionReclaimNeeded, deficit
	}

	return DecisionReject, 0
}

// SelectVictims orders candidate workloads for preemption according to strict tie-break rules:
// 1. Borrowed capacity workloads first.
// 2. Highest dominant share queues first.
// 3. Lowest workload priority first.
// 4. Most recently admitted first (LIFO among equals).
func SelectVictims(candidates []WorkloadCandidate, neededGPUs int32) []WorkloadCandidate {
	sorted := make([]WorkloadCandidate, len(candidates))
	copy(sorted, candidates)

	sort.Slice(sorted, func(i, j int) bool {
		// Rule 1: Borrowed first
		if sorted[i].IsBorrowed != sorted[j].IsBorrowed {
			return sorted[i].IsBorrowed && !sorted[j].IsBorrowed
		}

		// Rule 2: Higher dominant share queue first
		if sorted[i].DominantRate != sorted[j].DominantRate {
			return sorted[i].DominantRate > sorted[j].DominantRate
		}

		// Rule 3: Lower priority first
		if sorted[i].Priority != sorted[j].Priority {
			return sorted[i].Priority < sorted[j].Priority
		}

		// Rule 4: More recently admitted first (LIFO)
		return sorted[i].AdmittedAt.After(sorted[j].AdmittedAt)
	})

	var victims []WorkloadCandidate
	var reclaimed int32

	for _, c := range sorted {
		if reclaimed >= neededGPUs {
			break
		}
		victims = append(victims, c)
		reclaimed += c.TotalGPUs
	}

	return victims
}
