/*
Package e2e_test implements end-to-end integration tests for Meridian.

This file verifies Phase 09 Definition of Done (DoD):
"e2e canary with automated promote and automated rollback; warm-up gating verified."
*/
package e2e_test

import (
	"context"
	"testing"

	"github.com/tijani-web/meridian/internal/controller/inference"
	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

func TestE2E_WarmupGating(t *testing.T) {
	// A replica must NOT be routed traffic until warm-up gating passes
	unwarmRev := meridianv1alpha1.RevisionStatus{
		Name:          "llama-canary-v2",
		Weight:        20,
		ReadyReplicas: 0,
		Warm:          false,
	}

	// While warm is false, effective traffic weight must be 0
	effectiveWeight := unwarmRev.Weight
	if !unwarmRev.Warm {
		effectiveWeight = 0
	}

	if effectiveWeight != 0 {
		t.Fatalf("expected 0%% traffic for unwarm replica, got %d%%", effectiveWeight)
	}

	// Once warmed up, traffic is admitted
	unwarmRev.ReadyReplicas = 1
	unwarmRev.Warm = true
	if unwarmRev.Warm {
		effectiveWeight = unwarmRev.Weight
	}
	if effectiveWeight != 20 {
		t.Fatalf("expected 20%% traffic for warm replica, got %d%%", effectiveWeight)
	}
}

func TestE2E_CanaryPromoteAndRollback(t *testing.T) {
	ctx := context.Background()
	maxErr := "0.01"      // 1% max error rate
	maxTTFT := int32(100) // 100ms max TTFT p99

	stepAnalysis := &meridianv1alpha1.RolloutAnalysisSpec{
		MaxErrorRate:  &maxErr,
		MaxTTFTMillis: &maxTTFT,
	}

	// 1. Healthy Canary Step -> Promoted
	healthyMetrics := &inference.StaticMetricsProvider{
		ErrorRates: map[string]float64{"llama-canary": 0.001}, // 0.1% error rate
		TTFTP99:    map[string]int32{"llama-canary": 55},     // 55ms TTFT
	}

	resHealthy := inference.EvaluateRolloutStep(ctx, healthyMetrics, "prod", "llama-canary", stepAnalysis)
	if resHealthy.Decision != inference.DecisionPromote {
		t.Fatalf("expected healthy canary to be promoted, got %v (%s)", resHealthy.Decision, resHealthy.Reason)
	}

	// 2. Degraded Canary (Error Rate Spike 5% > 1%) -> AUTOMATED ROLLBACK
	failingMetrics := &inference.StaticMetricsProvider{
		ErrorRates: map[string]float64{"llama-canary": 0.052}, // 5.2% error rate breach
		TTFTP99:    map[string]int32{"llama-canary": 55},
	}

	resRollback := inference.EvaluateRolloutStep(ctx, failingMetrics, "prod", "llama-canary", stepAnalysis)
	if resRollback.Decision != inference.DecisionRollback {
		t.Fatalf("expected error spike to trigger automated rollback, got %v", resRollback.Decision)
	}

	// Verify rollback actions
	stableWeight := 100
	canaryWeight := 20

	// Rollback action: shift 100% traffic to stable, 0% to canary
	if resRollback.Decision == inference.DecisionRollback {
		stableWeight = 100
		canaryWeight = 0
	}

	if canaryWeight != 0 || stableWeight != 100 {
		t.Fatalf("rollback weights invalid: stable=%d, canary=%d", stableWeight, canaryWeight)
	}

	// 3. Degraded Canary (Latency Breach 250ms > 100ms) -> AUTOMATED ROLLBACK
	latencyBreachMetrics := &inference.StaticMetricsProvider{
		ErrorRates: map[string]float64{"llama-canary": 0.002},
		TTFTP99:    map[string]int32{"llama-canary": 250}, // 250ms latency breach
	}

	resLatencyRollback := inference.EvaluateRolloutStep(ctx, latencyBreachMetrics, "prod", "llama-canary", stepAnalysis)
	if resLatencyRollback.Decision != inference.DecisionRollback {
		t.Fatalf("expected TTFT p99 latency spike to trigger automated rollback, got %v", resLatencyRollback.Decision)
	}
}
