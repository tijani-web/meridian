/*
Package decisions implements the Meridian scheduler decision recorder — a per-pod ring
buffer that stores per-node filter and score results so that `meridianctl explain workload`
can show a human-readable breakdown of why each node was accepted or rejected.

Memory bound: at most MaxPodsRetained pods retained; each pod at most MaxNodesPerPod
node decisions. Each decision record is ~256 bytes → worst-case heap =
MaxPodsRetained × MaxNodesPerPod × 256 = 1024 × 256 × 256 ≈ 64 MiB. This is
documented and tested in decisions_test.go.

The recorder is safe for concurrent use from multiple scheduler goroutines.
*/
package decisions

import (
	"sync"
	"time"
)

const (
	// MaxPodsRetained is the maximum number of distinct pods whose decisions are stored.
	// Oldest pods are evicted when this limit is reached (LRU-approximate FIFO eviction).
	MaxPodsRetained = 1024
	// MaxNodesPerPod is the maximum number of node decision records retained per pod.
	MaxNodesPerPod = 256
)

// Verdict classifies the outcome of a scheduling cycle decision for a single node.
type Verdict string

const (
	// VerdictFilter means the node was rejected by a Filter plugin.
	VerdictFilter Verdict = "Filter"
	// VerdictScore means the node was accepted after scoring (not yet bound).
	VerdictScore Verdict = "Score"
	// VerdictBound means the pod was successfully bound to this node.
	VerdictBound Verdict = "Bound"
)

// NodeDecision captures a single scheduler decision for one (pod, node) pair.
type NodeDecision struct {
	// Node is the name of the Kubernetes node.
	Node string
	// Verdict classifies the decision (filter/score/bound).
	Verdict Verdict
	// Reason is a human-readable explanation (e.g., "InsufficientGPU", score breakdown).
	Reason string
	// FitScore is the anti-fragmentation sub-score [0,100].
	FitScore int64
	// TopoScore is the topology affinity sub-score [0,100].
	TopoScore int64
	// HealthScore is the node health sub-score [0,100].
	HealthScore int64
	// CompositeScore is the final weighted composite score [0,100].
	CompositeScore int64
	// RecordedAt is the wall-clock timestamp of this decision.
	RecordedAt time.Time
}

// PodKey uniquely identifies a pod.
type PodKey struct {
	Namespace string
	Name      string
	UID       string
}

// Recorder is the thread-safe decision ring buffer.
type Recorder struct {
	mu      sync.RWMutex
	records map[PodKey][]NodeDecision
	order   []PodKey // insertion order for FIFO eviction
}

// New allocates and returns a fresh Recorder.
func New() *Recorder {
	return &Recorder{
		records: make(map[PodKey][]NodeDecision, MaxPodsRetained),
		order:   make([]PodKey, 0, MaxPodsRetained),
	}
}

// Record appends a NodeDecision for the given pod. If the pod exceeds MaxNodesPerPod
// decisions, the oldest decision for that pod is dropped. If the total number of
// tracked pods exceeds MaxPodsRetained, the oldest pod's records are evicted entirely.
func (r *Recorder) Record(key PodKey, d NodeDecision) {
	d.RecordedAt = time.Now()

	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.records[key]
	if !ok {
		// New pod: check eviction budget
		if len(r.order) >= MaxPodsRetained {
			oldest := r.order[0]
			r.order = r.order[1:]
			delete(r.records, oldest)
		}
		r.order = append(r.order, key)
	}

	if len(existing) >= MaxNodesPerPod {
		// Ring: drop oldest node decision
		existing = existing[1:]
	}
	r.records[key] = append(existing, d)
}

// Query returns a snapshot of all NodeDecision records for the given pod.
// Returns nil if no records exist for the pod.
func (r *Recorder) Query(key PodKey) []NodeDecision {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if ds, ok := r.records[key]; ok {
		out := make([]NodeDecision, len(ds))
		copy(out, ds)
		return out
	}
	return nil
}

// Evict removes all decisions for the given pod (called on pod completion or deletion).
func (r *Recorder) Evict(key PodKey) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.records[key]; !ok {
		return
	}
	delete(r.records, key)
	for i, k := range r.order {
		if k == key {
			r.order = append(r.order[:i], r.order[i+1:]...)
			break
		}
	}
}

// Len returns the number of pods currently tracked.
func (r *Recorder) Len() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.records)
}
