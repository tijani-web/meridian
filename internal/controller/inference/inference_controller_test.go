package inference

import (
	"context"
	"encoding/json"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

func TestRevisionHash_Determinism(t *testing.T) {
	inf1 := &meridianv1alpha1.InferenceDeployment{
		Spec: meridianv1alpha1.InferenceDeploymentSpec{
			Model: meridianv1alpha1.ModelSpec{
				Name:     "llama-3-8b",
				URI:      "gs://models/llama-3-8b",
				Revision: "v1.0",
			},
			Runtime: meridianv1alpha1.RuntimeSpec{
				Image: "vllm/vllm-openai:v0.6.0",
				Port:  8000,
			},
			Resources: meridianv1alpha1.InferenceResourceSpec{
				GPUsPerReplica: 2,
			},
		},
	}

	inf2 := &meridianv1alpha1.InferenceDeployment{
		Spec: meridianv1alpha1.InferenceDeploymentSpec{
			Model: meridianv1alpha1.ModelSpec{
				Name:     "llama-3-8b",
				URI:      "gs://models/llama-3-8b",
				Revision: "v1.0",
			},
			Runtime: meridianv1alpha1.RuntimeSpec{
				Image: "vllm/vllm-openai:v0.6.0",
				Port:  8000,
			},
			Resources: meridianv1alpha1.InferenceResourceSpec{
				GPUsPerReplica: 2,
			},
		},
	}

	h1 := computeRevisionHash(inf1)
	h2 := computeRevisionHash(inf2)
	if h1 != h2 {
		t.Fatalf("expected identical hashes, got %s and %s", h1, h2)
	}

	// Change model revision -> must change hash
	inf2.Spec.Model.Revision = "v1.1"
	h3 := computeRevisionHash(inf2)
	if h1 == h3 {
		t.Fatalf("expected different hash after revision update, got %s", h3)
	}
}

func TestEvaluateRolloutStep_Promote(t *testing.T) {
	ctx := context.Background()
	maxErr := "0.01" // 1% max error rate
	maxTTFT := int32(200)

	analysis := &meridianv1alpha1.RolloutAnalysisSpec{
		MaxErrorRate:  &maxErr,
		MaxTTFTMillis: &maxTTFT,
	}

	metrics := &StaticMetricsProvider{
		ErrorRates: map[string]float64{"llama-canary": 0.002}, // 0.2% error rate (healthy)
		TTFTP99:    map[string]int32{"llama-canary": 120},    // 120ms (healthy)
	}

	res := EvaluateRolloutStep(ctx, metrics, "production", "llama-canary", analysis)
	if res.Decision != DecisionPromote {
		t.Errorf("expected DecisionPromote, got %v (reason: %s)", res.Decision, res.Reason)
	}
}

func TestEvaluateRolloutStep_ErrorRateRollback(t *testing.T) {
	ctx := context.Background()
	maxErr := "0.02" // 2% max error rate

	analysis := &meridianv1alpha1.RolloutAnalysisSpec{
		MaxErrorRate: &maxErr,
	}

	// 5% error rate -> should trigger automated rollback
	metrics := &StaticMetricsProvider{
		ErrorRates: map[string]float64{"llama-canary": 0.05},
	}

	res := EvaluateRolloutStep(ctx, metrics, "production", "llama-canary", analysis)
	if res.Decision != DecisionRollback {
		t.Errorf("expected DecisionRollback on error rate breach, got %v", res.Decision)
	}
	if res.Reason == "" {
		t.Errorf("expected diagnostic reason for rollback")
	}
}

func TestEvaluateRolloutStep_TTFTP99Rollback(t *testing.T) {
	ctx := context.Background()
	maxTTFT := int32(150) // 150ms max TTFT p99

	analysis := &meridianv1alpha1.RolloutAnalysisSpec{
		MaxTTFTMillis: &maxTTFT,
	}

	// 350ms TTFT p99 -> should trigger automated rollback
	metrics := &StaticMetricsProvider{
		TTFTP99: map[string]int32{"llama-canary": 350},
	}

	res := EvaluateRolloutStep(ctx, metrics, "production", "llama-canary", analysis)
	if res.Decision != DecisionRollback {
		t.Errorf("expected DecisionRollback on TTFT latency breach, got %v", res.Decision)
	}
}

func TestPublishedRoutingTable_JSON(t *testing.T) {
	table := PublishedRoutingTable{
		Deployment:    "llama-serve",
		Namespace:     "production",
		Strategy:      meridianv1alpha1.RoutingStrategyLeastQueue,
		MaxQueueDepth: 120,
		Revisions: []PublishedRevision{
			{Name: "llama-serve-v1", Weight: 80, Warm: true, Endpoints: []string{"http://llama-serve-v1:8000"}},
			{Name: "llama-serve-v2", Weight: 20, Warm: true, Endpoints: []string{"http://llama-serve-v2:8000"}},
		},
	}

	data, err := json.Marshal(table)
	if err != nil {
		t.Fatalf("failed to marshal routing table: %v", err)
	}

	var decoded PublishedRoutingTable
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("failed to unmarshal routing table: %v", err)
	}

	if decoded.Deployment != "llama-serve" {
		t.Errorf("expected deployment llama-serve, got %s", decoded.Deployment)
	}
	if len(decoded.Revisions) != 2 {
		t.Errorf("expected 2 revisions, got %d", len(decoded.Revisions))
	}
	if decoded.Revisions[0].Weight != 80 || decoded.Revisions[1].Weight != 20 {
		t.Errorf("weights mismatch: %d and %d", decoded.Revisions[0].Weight, decoded.Revisions[1].Weight)
	}
}

func TestAutomatedRollbackCondition(t *testing.T) {
	inf := &meridianv1alpha1.InferenceDeployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-inf",
			Namespace: "default",
		},
	}

	cond := metav1.Condition{
		Type:    "RolloutAborted",
		Status:  metav1.ConditionTrue,
		Reason:  "AutomatedRollback",
		Message: "Error rate exceeded 0.05",
	}
	inf.Status.Conditions = append(inf.Status.Conditions, cond)

	if len(inf.Status.Conditions) != 1 {
		t.Fatalf("expected 1 condition, got %d", len(inf.Status.Conditions))
	}
	if inf.Status.Conditions[0].Reason != "AutomatedRollback" {
		t.Errorf("expected AutomatedRollback reason, got %s", inf.Status.Conditions[0].Reason)
	}
}
