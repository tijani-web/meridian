package inference

import (
	"context"
	"fmt"
	"strconv"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

// Decision indicates the automated rollout action after metric analysis.
type Decision int

const (
	DecisionHold Decision = iota
	DecisionPromote
	DecisionRollback
)

// AnalysisResult encapsulates the decision and diagnostic reason.
type AnalysisResult struct {
	Decision Decision
	Reason   string
}

// MetricsProvider queries live metrics for an inference revision.
type MetricsProvider interface {
	GetRevisionMetrics(ctx context.Context, namespace, revisionName string) (errorRate float64, ttftP99Millis int32, err error)
}

// StaticMetricsProvider provides fixed metrics (used for deterministic testing and simulations).
type StaticMetricsProvider struct {
	ErrorRates map[string]float64
	TTFTP99    map[string]int32
}

// GetRevisionMetrics returns stored metrics for the revision.
func (s *StaticMetricsProvider) GetRevisionMetrics(ctx context.Context, namespace, revisionName string) (float64, int32, error) {
	er := 0.0
	if s.ErrorRates != nil {
		er = s.ErrorRates[revisionName]
	}
	ttft := int32(50) // Default 50ms
	if s.TTFTP99 != nil && s.TTFTP99[revisionName] > 0 {
		ttft = s.TTFTP99[revisionName]
	}
	return er, ttft, nil
}

// EvaluateRolloutStep evaluates canary health against step promotion criteria.
func EvaluateRolloutStep(
	ctx context.Context,
	metrics MetricsProvider,
	namespace, revisionName string,
	analysis *meridianv1alpha1.RolloutAnalysisSpec,
) AnalysisResult {
	if analysis == nil {
		// No analysis criteria specified: auto-promote when warm
		return AnalysisResult{Decision: DecisionPromote, Reason: "No analysis criteria configured; ready to advance"}
	}

	errRate, ttftP99, err := metrics.GetRevisionMetrics(ctx, namespace, revisionName)
	if err != nil {
		return AnalysisResult{
			Decision: DecisionHold,
			Reason:   fmt.Sprintf("Failed to query metrics for %s: %v", revisionName, err),
		}
	}

	// 1. Evaluate MaxErrorRate breach -> trigger automated rollback
	if analysis.MaxErrorRate != nil && *analysis.MaxErrorRate != "" {
		maxErr, parseErr := strconv.ParseFloat(*analysis.MaxErrorRate, 64)
		if parseErr == nil && errRate > maxErr {
			return AnalysisResult{
				Decision: DecisionRollback,
				Reason: fmt.Sprintf("Error rate %.4f exceeded threshold %.4f for revision %s",
					errRate, maxErr, revisionName),
			}
		}
	}

	// 2. Evaluate MaxTTFTMillis breach -> trigger automated rollback
	if analysis.MaxTTFTMillis != nil && *analysis.MaxTTFTMillis > 0 {
		if ttftP99 > *analysis.MaxTTFTMillis {
			return AnalysisResult{
				Decision: DecisionRollback,
				Reason: fmt.Sprintf("TTFT p99 %d ms exceeded maximum threshold %d ms for revision %s",
					ttftP99, *analysis.MaxTTFTMillis, revisionName),
			}
		}
	}

	// Metrics are within limits: promote to next step
	return AnalysisResult{
		Decision: DecisionPromote,
		Reason:   fmt.Sprintf("Metrics healthy (errorRate=%.4f, ttftP99=%dms); promoting revision %s", errRate, ttftP99, revisionName),
	}
}
