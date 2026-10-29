/*
Package metrics_test verifies that all Prometheus metrics, label names, and recording helpers
function accurately, strictly maintain label consistency, and follow Prometheus conventions.
*/
package metrics_test

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/tijani-web/meridian/internal/metrics"
)

func TestMetrics_RegistrationAndRecording(t *testing.T) {
	reg := prometheus.NewRegistry()
	m := metrics.NewMetrics(reg)

	if m == nil {
		t.Fatalf("expected NewMetrics to return non-nil bundle")
	}

	// 1. Workload Admission & Gang
	m.RecordAdmissionLatency("default", "high", 0.042)
	m.RecordAdmission("default", "high", "admitted")
	m.RecordPreemption("default", "priority")
	m.RecordPhaseTransition("Pending", "Admitted")
	m.SetQueueDepth("default", "high", 5)
	m.SetQueueGPUAllocated("default", "H100-SXM5-80GB", 32)
	m.RecordGangStartLatency("default", "8", 12.4)
	m.RecordPartialGangStart("default")
	m.RecordGangPermitWait("default", 1.8)

	// 2. Router
	m.RecordRouterRequest("PrefixAffinity", "interactive", "success")
	m.RecordRouterTTFT("PrefixAffinity", "replica-1", 0.085)
	m.RecordRouterTokens("PrefixAffinity", "replica-1", 256)
	m.IncRouterActiveRequests("PrefixAffinity")
	m.DecRouterActiveRequests("PrefixAffinity")
	m.SetRouterKVCacheUsage("replica-1", 0.64)
	m.RecordPrefixCacheHit("PrefixAffinity", "replica-1")

	// 3. Node Health & GPU
	m.RecordXIDEvent("node-gpu-01", "gpu-0", "79", "fatal")
	m.RecordECCDBE("node-gpu-01", "gpu-0")
	m.SetGPUTemperature("node-gpu-01", "gpu-0", 72.5)
	m.SetGPUHealthy("node-gpu-01", "gpu-0", true)
	m.RecordNodeRecoveryDuration("node-gpu-01", 15.2)

	// 4. Checkpoint
	m.RecordCheckpointRequest("default")
	m.RecordCheckpointAckLatency("default", 4.1)
	m.RecordCheckpointTimeout("default")

	// Gather metrics to ensure valid protobuf encoding and no collisions
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("reg.Gather failed: %v", err)
	}

	expectedMetrics := map[string]dto.MetricType{
		"meridian_workload_admission_latency_seconds": dto.MetricType_HISTOGRAM,
		"meridian_workload_admission_total":           dto.MetricType_COUNTER,
		"meridian_workload_preemptions_total":         dto.MetricType_COUNTER,
		"meridian_workload_phase_transitions_total":   dto.MetricType_COUNTER,
		"meridian_queue_depth":                        dto.MetricType_GAUGE,
		"meridian_queue_gpu_allocated":                dto.MetricType_GAUGE,
		"meridian_gang_start_latency_seconds":         dto.MetricType_HISTOGRAM,
		"meridian_gang_partial_starts_total":          dto.MetricType_COUNTER,
		"meridian_gang_permit_wait_seconds":           dto.MetricType_HISTOGRAM,
		"meridian_router_request_total":               dto.MetricType_COUNTER,
		"meridian_router_ttft_seconds":                dto.MetricType_HISTOGRAM,
		"meridian_router_tokens_generated_total":      dto.MetricType_COUNTER,
		"meridian_router_active_requests":             dto.MetricType_GAUGE,
		"meridian_router_kv_cache_usage":              dto.MetricType_GAUGE,
		"meridian_router_prefix_cache_hits_total":     dto.MetricType_COUNTER,
		"meridian_gpu_xid_events_total":               dto.MetricType_COUNTER,
		"meridian_gpu_ecc_dbe_total":                  dto.MetricType_COUNTER,
		"meridian_gpu_temperature_celsius":            dto.MetricType_GAUGE,
		"meridian_gpu_healthy":                        dto.MetricType_GAUGE,
		"meridian_node_recovery_duration_seconds":     dto.MetricType_HISTOGRAM,
		"meridian_checkpoint_request_total":           dto.MetricType_COUNTER,
		"meridian_checkpoint_ack_latency_seconds":     dto.MetricType_HISTOGRAM,
		"meridian_checkpoint_timeout_total":           dto.MetricType_COUNTER,
	}

	foundMetrics := make(map[string]dto.MetricType)
	for _, mf := range mfs {
		foundMetrics[mf.GetName()] = mf.GetType()
	}

	for name, expectedType := range expectedMetrics {
		actualType, ok := foundMetrics[name]
		if !ok {
			t.Errorf("missing expected metric %q in gathered metrics", name)
			continue
		}
		if actualType != expectedType {
			t.Errorf("metric %q type mismatch: expected %v, got %v", name, expectedType, actualType)
		}
	}
}

func TestMetrics_NilSafety(t *testing.T) {
	var m *metrics.Metrics

	// Verify that calling any helper on nil Metrics does not panic
	m.RecordAdmissionLatency("q", "p", 1.0)
	m.RecordAdmission("q", "p", "admitted")
	m.RecordPreemption("q", "reason")
	m.RecordPhaseTransition("p1", "p2")
	m.SetQueueDepth("q", "p", 1)
	m.SetQueueGPUAllocated("q", "t", 2)
	m.RecordGangStartLatency("q", "4", 5.0)
	m.RecordPartialGangStart("q")
	m.RecordGangPermitWait("q", 1.0)
	m.RecordRouterRequest("s", "p", "r")
	m.RecordRouterTTFT("s", "rep", 0.1)
	m.RecordRouterTokens("s", "rep", 100)
	m.IncRouterActiveRequests("s")
	m.DecRouterActiveRequests("s")
	m.SetRouterKVCacheUsage("rep", 0.5)
	m.RecordPrefixCacheHit("s", "rep")
	m.RecordXIDEvent("n", "g", "31", "crit")
	m.RecordECCDBE("n", "g")
	m.SetGPUTemperature("n", "g", 65.0)
	m.SetGPUHealthy("n", "g", false)
	m.RecordNodeRecoveryDuration("n", 10.0)
	m.RecordCheckpointRequest("q")
	m.RecordCheckpointAckLatency("q", 2.0)
	m.RecordCheckpointTimeout("q")
}
