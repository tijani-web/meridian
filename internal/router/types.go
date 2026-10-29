/*
Package router implements the Meridian Inference Router data plane (§6.7).

It provides an ultra-low latency, OpenAI-compatible reverse proxy capable of
scraping live vLLM engine metrics, dynamically selecting execution replicas
via four pluggable strategies (RoundRobin, LeastQueue, KVAware, PrefixAffinity),
enforcing priority-aware load shedding, and streaming SSE responses.
*/
package router

import (
	"sync"
	"time"
)

// Strategy represents the algorithm used to select an inference replica.
type Strategy string

const (
	// StrategyRoundRobin distributes requests uniformly in round-robin fashion.
	StrategyRoundRobin Strategy = "RoundRobin"
	// StrategyLeastQueue selects the replica with the lowest combined waiting + running queue.
	StrategyLeastQueue Strategy = "LeastQueue"
	// StrategyKVAware directs traffic away from replicas exceeding safe KV-cache utilization.
	StrategyKVAware Strategy = "KVAware"
	// StrategyPrefixAffinity applies consistent hashing on the prompt prefix with bounded load.
	StrategyPrefixAffinity Strategy = "PrefixAffinity"
)

// ReplicaState holds the live telemetry, health status, and active load for a vLLM replica.
type ReplicaState struct {
	ID              string
	Address         string // e.g. "http://10.244.1.25:8000"
	Model           string
	Warm            bool
	Healthy         bool

	mu              sync.RWMutex
	WaitingRequests int64
	RunningRequests int64
	KVCacheUsage    float64 // 0.0 - 1.0
	InflightRouted  int64   // Tracked in router between dispatch and completion
	LastScraped     time.Time
}

// EffectiveLoad returns total active load including inflight requests tracked by the router.
func (r *ReplicaState) EffectiveLoad() int64 {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.WaitingRequests + r.RunningRequests + r.InflightRouted
}

// IsStale checks if metric telemetry has exceeded the staleness threshold.
func (r *ReplicaState) IsStale(threshold time.Duration) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return time.Since(r.LastScraped) > threshold
}

// RoutingDecision captures the result of a strategy evaluation.
type RoutingDecision struct {
	SelectedReplica *ReplicaState
	StrategyUsed    Strategy
	PrefixHash      string
	IsCacheHit      bool
	Reason          string
}

// PriorityClass represents request urgency for shedding decisions.
type PriorityClass string

const (
	PriorityInteractive PriorityClass = "interactive"
	PriorityBatch       PriorityClass = "batch"
)
