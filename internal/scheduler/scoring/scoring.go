/*
Package scoring implements the Meridian GPU-aware composite scoring algorithm for
the Kubernetes scheduler framework Score and NormalizeScore extension points.

Scoring model (see docs/design/scoring.md for full formulation):

  Composite(P, N) = (wFit·Fit(P,N) + wTopo·Topo(P,N) + wHealth·Health(N)) / (wFit+wTopo+wHealth)

  Default weights: Fit=40, Topo=45, Health=15.

All weights are configurable via KubeSchedulerConfiguration plugin args.
*/
package scoring

import (
	"strconv"
)

const (
	// WeightFit is the default weight for the anti-fragmentation best-fit sub-score.
	WeightFit = int64(40)
	// WeightTopo is the default weight for the topology affinity sub-score.
	WeightTopo = int64(45)
	// WeightHealth is the default weight for the node health penalty sub-score.
	WeightHealth = int64(15)
)

// Config holds tunable weights for the composite scoring function.
// All weights must be non-negative; at least one must be > 0.
type Config struct {
	// WeightFit controls anti-fragmentation priority (default 40).
	WeightFit int64
	// WeightTopo controls topology affinity priority (default 45).
	WeightTopo int64
	// WeightHealth controls node health penalty priority (default 15).
	WeightHealth int64
}

// DefaultConfig returns the recommended production scoring weights from docs/design/scoring.md.
func DefaultConfig() Config {
	return Config{
		WeightFit:    WeightFit,
		WeightTopo:   WeightTopo,
		WeightHealth: WeightHealth,
	}
}

// FitScore computes the anti-fragmentation best-fit sub-score in [0, 100].
//
// The goal is to maximize node utilization and minimize GPU stranding.
// Placements that perfectly fill a node (leftover == 0) score 100.
// Placements that leave common usable leftover capacities (4, 8 GPUs) score 90.
// Placements that leave stranded capacity score 50.
//
// Parameters:
//   - capacity: total GPU count on the node
//   - used: currently allocated GPUs on the node
//   - requested: GPUs requested by the pod being placed
//
// Returns 0 if the pod does not fit (requested > capacity-used).
func FitScore(capacity, used, requested int64) int64 {
	available := capacity - used
	if available < requested || capacity == 0 {
		return 0
	}
	leftover := available - requested
	// Utilization after placement: normalized to [0,100]
	utilization := (used + requested) * 100 / capacity
	// Alignment penalty factor (×100 for integer arithmetic)
	alignFactor := int64(50) // default: stranded capacity
	switch leftover {
	case 0:
		alignFactor = 100
	case 4, 8:
		alignFactor = 90
	case 2:
		alignFactor = 75
	}
	return utilization * alignFactor / 100
}

// HealthScore parses the meridian.dev/health-score label value and returns
// an int64 in [0, 100]. If the label is absent or unparseable, 100 is returned
// (assumed healthy — the node health agent is responsible for maintaining correctness).
func HealthScore(nodeLabels map[string]string) int64 {
	v, ok := nodeLabels["meridian.dev/health-score"]
	if !ok {
		return 100
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 100
	}
	if n > 100 {
		return 100
	}
	return n
}

// Composite computes the weighted composite score given sub-scores and a Config.
// The result is clamped to [0, 100].
func Composite(fitScore, topoScore, healthScore int64, cfg Config) int64 {
	totalWeight := cfg.WeightFit + cfg.WeightTopo + cfg.WeightHealth
	if totalWeight == 0 {
		return 0
	}
	raw := (cfg.WeightFit*fitScore + cfg.WeightTopo*topoScore + cfg.WeightHealth*healthScore) / totalWeight
	if raw < 0 {
		return 0
	}
	if raw > 100 {
		return 100
	}
	return raw
}
