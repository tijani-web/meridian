package explain

import (
	"testing"
	"time"
)

func TestFormatWorkload_AllPhases(t *testing.T) {
	// Minimal explanation — verify FormatWorkload produces correct section headers
	w := &WorkloadExplanation{
		Name:          "train-llama",
		Namespace:     "research",
		Phase:         "Queued",
		PhaseDuration: 35 * time.Second,
		Queue: QueueExplanation{
			QueueName:       "team-alpha",
			Position:        "3 workloads pending in queue",
			QuotaAllocated:  24,
			QuotaGuaranteed: 32,
			QuotaCeiling:    48,
			IsBlocked:       false,
		},
		Gated: GatedExplanation{
			IsGated: true,
			Gate:    "meridian.dev/admission",
			Reason:  "Workload waiting for queue quota admission before scheduling",
		},
		Scheduling: SchedulingExplanation{
			IsScheduling: true,
			RejectionsByNode: map[string]int{
				"insufficient nvidia.com/gpu":                     20,
				"unhealthy taint (meridian.dev/gpu-unhealthy)":   4,
			},
			CandidateScores: []CandidateScore{
				{Node: "worker-gpu-0", FitScore: 85, TopoScore: 90, HealthScore: 100, CompositeScore: 91},
				{Node: "worker-gpu-1", FitScore: 80, TopoScore: 80, HealthScore: 95, CompositeScore: 85},
			},
		},
		Permit: PermitExplanation{
			IsPermitWaiting: true,
			Reserved:        3,
			Required:        4,
			RemainingTime:   90 * time.Second,
		},
		History: []HistoryExplanation{
			{Seq: 1, Phase: "Queued", Reason: "Queued", Message: "Submitted to queue", Timestamp: time.Now().Add(-60 * time.Second)},
		},
	}

	out := FormatWorkload(w)

	// Validate all 5 diagnostic sections are present
	sections := []string{
		"1. Queue & Quota:",
		"2. Scheduling Gate:",
		"3. Scheduler Filter & Score:",
		"4. Gang Permit Status:",
		"5. Preemption & Recovery History:",
	}
	for _, section := range sections {
		if !containsStr(out, section) {
			t.Errorf("expected section %q in output", section)
		}
	}

	// Validate key data points
	expectations := []string{
		"train-llama",
		"research",
		"Queued",
		"team-alpha",
		"meridian.dev/admission",
		"20 nodes: insufficient nvidia.com/gpu",
		"worker-gpu-0",
		"3/4",
	}
	for _, exp := range expectations {
		if !containsStr(out, exp) {
			t.Errorf("expected %q in workload explanation output", exp)
		}
	}
}

func TestFormatCluster(t *testing.T) {
	c := &ClusterStatus{
		TotalGPUs:          64,
		AllocatedGPUs:      48,
		AvailableGPUs:      16,
		GPUUtilizationPct:  75.0,
		FragmentationRatio: 0.12,
		HealthyNodes:       8,
		UnhealthyNodes:     1,
		Queues: []QueueStatusEntry{
			{Name: "team-alpha", Priority: 10, Allocated: 24, Guaranteed: 32, Ceiling: 48, Pending: 3},
			{Name: "team-beta", Priority: 5, Allocated: 24, Guaranteed: 16, Ceiling: 32, Pending: 1},
		},
	}

	out := FormatCluster(c)
	expectations := []string{
		"64",
		"75.0%",
		"8 healthy",
		"1 unhealthy",
		"team-alpha",
		"team-beta",
	}
	for _, exp := range expectations {
		if !containsStr(out, exp) {
			t.Errorf("expected %q in cluster status output", exp)
		}
	}
}

func TestFormatInference(t *testing.T) {
	inf := &InferenceExplanation{
		Name:            "llama-serve",
		Namespace:       "production",
		Model:           "llama-3-8b-instruct",
		TotalReplicas:   4,
		HealthyReplicas: 3,
		WarmupGating:    "Passed",
		CanaryActive:    true,
		CanaryWeight:    20,
		Revisions: []InferenceRevision{
			{Revision: "llama-serve-stable", Weight: 80, Replicas: 4, Ready: 4},
			{Revision: "llama-serve-canary", Weight: 20, Replicas: 1, Ready: 1},
		},
		Endpoints: []string{"http://llama-serve.production.svc.cluster.local:8000/v1/chat/completions"},
	}

	out := FormatInference(inf)
	expectations := []string{
		"llama-serve",
		"llama-3-8b-instruct",
		"ACTIVE (weight=20%)",
		"llama-serve-stable",
		"llama-serve-canary",
		"http://llama-serve.production.svc.cluster.local:8000",
	}
	for _, exp := range expectations {
		if !containsStr(out, exp) {
			t.Errorf("expected %q in inference explanation output", exp)
		}
	}
}

func TestFormatWorkload_NoHistory(t *testing.T) {
	w := &WorkloadExplanation{
		Name:      "test-wl",
		Namespace: "default",
		Phase:     "Running",
		Queue:     QueueExplanation{QueueName: "default-queue"},
		Scheduling: SchedulingExplanation{
			RejectionsByNode: make(map[string]int),
		},
		Permit: PermitExplanation{Reserved: 2, Required: 2},
	}

	out := FormatWorkload(w)
	if !containsStr(out, "no preemption or recovery events") {
		t.Errorf("expected no-history message in output")
	}
}

func TestStuckWorkloadScenarios(t *testing.T) {
	// Scenario 1: Quota-blocked
	w1 := &WorkloadExplanation{
		Name:          "training-run-1",
		Namespace:     "research",
		Phase:         "Pending",
		PhaseDuration: 120 * time.Second,
		Queue: QueueExplanation{
			QueueName:        "team-nlp",
			Position:         "4 workloads ahead",
			QuotaAllocated:   32,
			QuotaGuaranteed:  32,
			QuotaCeiling:     32,
			IsBlocked:        true,
			BlockReason:      "queue quota ceiling reached (32/32 GPUs)",
			HoldingWorkloads: []string{"active-job-a", "active-job-b"},
		},
		Gated: GatedExplanation{
			IsGated: true,
			Gate:    "meridian.dev/admission",
			Reason:  "Workload waiting for queue quota admission before scheduling",
		},
	}
	out1 := FormatWorkload(w1)
	if !containsStr(out1, "queue quota ceiling reached (32/32 GPUs)") {
		t.Errorf("Scenario 1 failed to report quota blocked reason")
	}
	if !containsStr(out1, "active-job-a") || !containsStr(out1, "active-job-b") {
		t.Errorf("Scenario 1 failed to list blocking workloads")
	}

	// Scenario 2: Insufficient GPUs
	w2 := &WorkloadExplanation{
		Name:          "large-train-2",
		Namespace:     "ml",
		Phase:         "Scheduling",
		PhaseDuration: 45 * time.Second,
		Queue: QueueExplanation{
			QueueName:       "team-vision",
			QuotaAllocated:  16,
			QuotaGuaranteed: 32,
			QuotaCeiling:    64,
		},
		Scheduling: SchedulingExplanation{
			IsScheduling: true,
			RejectionsByNode: map[string]int{
				"insufficient nvidia.com/gpu": 32,
			},
		},
	}
	out2 := FormatWorkload(w2)
	if !containsStr(out2, "32 nodes: insufficient nvidia.com/gpu") {
		t.Errorf("Scenario 2 failed to report insufficient GPU rejection")
	}

	// Scenario 3: Topology constraint unmet
	w3 := &WorkloadExplanation{
		Name:          "distributed-gpt",
		Namespace:     "research",
		Phase:         "Scheduling",
		PhaseDuration: 80 * time.Second,
		Queue: QueueExplanation{
			QueueName:       "team-nlp",
			QuotaAllocated:  8,
			QuotaGuaranteed: 32,
			QuotaCeiling:    32,
		},
		Scheduling: SchedulingExplanation{
			IsScheduling: true,
			RejectionsByNode: map[string]int{
				"topology SameNVLinkDomain unmet": 16,
				"insufficient nvidia.com/gpu":     8,
			},
			CandidateScores: []CandidateScore{
				{Node: "worker-h100-0", FitScore: 100, TopoScore: 40, HealthScore: 100, CompositeScore: 76},
			},
		},
	}
	out3 := FormatWorkload(w3)
	if !containsStr(out3, "16 nodes: topology SameNVLinkDomain unmet") {
		t.Errorf("Scenario 3 failed to report topology unmet rejection")
	}

	// Scenario 4: Permit-waiting
	w4 := &WorkloadExplanation{
		Name:          "gang-job-4",
		Namespace:     "research",
		Phase:         "Scheduling",
		PhaseDuration: 15 * time.Second,
		Queue: QueueExplanation{
			QueueName: "team-nlp",
		},
		Permit: PermitExplanation{
			IsPermitWaiting: true,
			Reserved:        6,
			Required:        8,
			RemainingTime:   45 * time.Second,
		},
	}
	out4 := FormatWorkload(w4)
	if !containsStr(out4, "Waiting at Permit: 6/8 reserved") {
		t.Errorf("Scenario 4 failed to report permit gang reservation status")
	}
	if !containsStr(out4, "Timeout Remaining: 45s") {
		t.Errorf("Scenario 4 failed to report permit timeout remaining")
	}

	// Scenario 5: Preempted
	w5 := &WorkloadExplanation{
		Name:          "preempted-worker-5",
		Namespace:     "batch",
		Phase:         "Preempting",
		PhaseDuration: 25 * time.Second,
		Queue: QueueExplanation{
			QueueName: "batch-low-pri",
		},
		History: []HistoryExplanation{
			{Seq: 1, Phase: "Running", Reason: "AdmittedAndBound", Message: "All pods running on worker-gpu-1", Timestamp: time.Now().Add(-180 * time.Second)},
			{Seq: 2, Phase: "Checkpointing", Reason: "CooperativePreemption", Message: "Victim chosen for higher priority workload team-nlp/distributed-gpt; checkpoint requested", Timestamp: time.Now().Add(-40 * time.Second)},
			{Seq: 3, Phase: "Preempting", Reason: "PreemptionEviction", Message: "Checkpoint acked in 14.8s; terminating gang pods", Timestamp: time.Now().Add(-25 * time.Second)},
		},
	}
	out5 := FormatWorkload(w5)
	if !containsStr(out5, "Victim chosen for higher priority workload team-nlp/distributed-gpt") {
		t.Errorf("Scenario 5 failed to report preemption victim context")
	}
	if !containsStr(out5, "Checkpoint acked in 14.8s; terminating gang pods") {
		t.Errorf("Scenario 5 failed to report checkpoint ack and termination")
	}
}

// containsStr is a helper that checks if substr appears in s (case-sensitive).
func containsStr(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && (s == substr || len(s) >= len(substr) &&
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}
