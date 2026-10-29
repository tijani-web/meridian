/*
Package router provides the 4 core inference routing strategies.
*/
package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash/fnv"
	"math"
	"sync/atomic"
	"time"
)

// StrategyEngine evaluates routing policies over an eligible pool of replicas.
type StrategyEngine struct {
	rrCounter       uint64
	stalenessLimit  time.Duration
	kvSafeThreshold float64 // default 0.85
	boundedLoadMult float64 // default 1.25
}

// NewStrategyEngine creates a strategy engine with default production parameters.
func NewStrategyEngine() *StrategyEngine {
	return &StrategyEngine{
		stalenessLimit:  3 * time.Second,
		kvSafeThreshold: 0.85,
		boundedLoadMult: 1.25,
	}
}

// FilterEligible extracts healthy, warm, and non-stale replicas from the candidate pool.
func (e *StrategyEngine) FilterEligible(pool []*ReplicaState) []*ReplicaState {
	var eligible []*ReplicaState
	for _, r := range pool {
		if r == nil {
			continue
		}
		// Must be marked warm (passes warm-up probe) and healthy
		if !r.Warm || !r.Healthy {
			continue
		}
		eligible = append(eligible, r)
	}
	return eligible
}

// HashPrefix computes a 64-bit numerical hash and hex string from prompt text for consistent hashing.
func HashPrefix(text string) (uint64, string) {
	if len(text) > 256 {
		text = text[:256]
	}
	h := fnv.New64a()
	_, _ = h.Write([]byte(text))
	hashVal := h.Sum64()

	sha := sha256.Sum256([]byte(text))
	return hashVal, hex.EncodeToString(sha[:8])
}

// Route evaluates the requested Strategy over the provided replica pool.
func (e *StrategyEngine) Route(strategy Strategy, pool []*ReplicaState, promptPrefix string) (*RoutingDecision, error) {
	candidates := e.FilterEligible(pool)
	if len(candidates) == 0 {
		return nil, fmt.Errorf("no eligible warm replicas available in pool")
	}

	switch strategy {
	case StrategyRoundRobin:
		return e.routeRoundRobin(candidates)
	case StrategyLeastQueue:
		return e.routeLeastQueue(candidates)
	case StrategyKVAware:
		return e.routeKVAware(candidates)
	case StrategyPrefixAffinity:
		return e.routePrefixAffinity(candidates, promptPrefix)
	default:
		// Default to LeastQueue if unspecified
		return e.routeLeastQueue(candidates)
	}
}

func (e *StrategyEngine) routeRoundRobin(candidates []*ReplicaState) (*RoutingDecision, error) {
	idx := atomic.AddUint64(&e.rrCounter, 1) - 1
	target := candidates[idx%uint64(len(candidates))]

	return &RoutingDecision{
		SelectedReplica: target,
		StrategyUsed:    StrategyRoundRobin,
		Reason:          fmt.Sprintf("RoundRobin index %d selected", idx%uint64(len(candidates))),
	}, nil
}

func (e *StrategyEngine) routeLeastQueue(candidates []*ReplicaState) (*RoutingDecision, error) {
	var best *ReplicaState
	minLoad := int64(math.MaxInt64)
	bestKV := 1.0

	for _, c := range candidates {
		load := c.EffectiveLoad()
		c.mu.RLock()
		kv := c.KVCacheUsage
		isStale := time.Since(c.LastScraped) > e.stalenessLimit
		c.mu.RUnlock()

		// Penalize stale telemetry heavily
		if isStale {
			load += 50
		}

		if load < minLoad || (load == minLoad && kv < bestKV) {
			minLoad = load
			bestKV = kv
			best = c
		}
	}

	return &RoutingDecision{
		SelectedReplica: best,
		StrategyUsed:    StrategyLeastQueue,
		Reason:          fmt.Sprintf("LeastQueue selected replica with load=%d, kv=%.2f", minLoad, bestKV),
	}, nil
}

func (e *StrategyEngine) routeKVAware(candidates []*ReplicaState) (*RoutingDecision, error) {
	var underThreshold []*ReplicaState

	for _, c := range candidates {
		c.mu.RLock()
		kv := c.KVCacheUsage
		c.mu.RUnlock()

		if kv < e.kvSafeThreshold {
			underThreshold = append(underThreshold, c)
		}
	}

	// If replicas with safe KV-cache exist, pick LeastQueue among them
	if len(underThreshold) > 0 {
		dec, err := e.routeLeastQueue(underThreshold)
		if err == nil {
			dec.StrategyUsed = StrategyKVAware
			dec.Reason = fmt.Sprintf("KVAware (safe tier < %.0f%%): %s", e.kvSafeThreshold*100, dec.Reason)
			return dec, nil
		}
	}

	// Cluster is saturated: pick replica with strictly lowest KV-cache usage to minimize eviction penalty
	var best *ReplicaState
	lowestKV := 2.0
	for _, c := range candidates {
		c.mu.RLock()
		kv := c.KVCacheUsage
		c.mu.RUnlock()
		if kv < lowestKV {
			lowestKV = kv
			best = c
		}
	}

	return &RoutingDecision{
		SelectedReplica: best,
		StrategyUsed:    StrategyKVAware,
		Reason:          fmt.Sprintf("KVAware (cluster saturated): picked lowest KV occupancy %.2f%%", lowestKV*100),
	}, nil
}

func (e *StrategyEngine) routePrefixAffinity(candidates []*ReplicaState, prefix string) (*RoutingDecision, error) {
	if prefix == "" {
		// No prefix supplied; fallback to LeastQueue
		dec, err := e.routeLeastQueue(candidates)
		if err == nil {
			dec.StrategyUsed = StrategyPrefixAffinity
			dec.Reason = "PrefixAffinity fallback: empty prefix routed via LeastQueue"
		}
		return dec, err
	}

	hashVal, hashHex := HashPrefix(prefix)

	// Compute average load across candidate pool
	var totalLoad int64
	for _, c := range candidates {
		totalLoad += c.EffectiveLoad()
	}
	avgLoad := float64(totalLoad) / float64(len(candidates))
	maxAllowedLoad := int64(math.Ceil(e.boundedLoadMult * avgLoad))
	if maxAllowedLoad < 2 {
		maxAllowedLoad = 2 // Minimum load allowance buffer
	}

	// Primary target from consistent hash ring
	primaryIdx := hashVal % uint64(len(candidates))
	primary := candidates[primaryIdx]

	if primary.EffectiveLoad() <= maxAllowedLoad {
		// Cache hit! Within bounded capacity limit
		return &RoutingDecision{
			SelectedReplica: primary,
			StrategyUsed:    StrategyPrefixAffinity,
			PrefixHash:      hashHex,
			IsCacheHit:      true,
			Reason:          fmt.Sprintf("PrefixAffinity primary hit on %s (load %d <= bound %d)", primary.ID, primary.EffectiveLoad(), maxAllowedLoad),
		}, nil
	}

	// Overloaded primary: spillover to LeastQueue to prevent hotspot tail latency
	dec, err := e.routeLeastQueue(candidates)
	if err == nil {
		dec.StrategyUsed = StrategyPrefixAffinity
		dec.PrefixHash = hashHex
		dec.IsCacheHit = false
		dec.Reason = fmt.Sprintf("PrefixAffinity spillover: primary %s overloaded (load %d > bound %d) -> routed to %s",
			primary.ID, primary.EffectiveLoad(), maxAllowedLoad, dec.SelectedReplica.ID)
	}
	return dec, err
}
