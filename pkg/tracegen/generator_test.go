package tracegen

import (
	"bytes"
	"testing"
)

func TestGenerator_Determinism(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JobCount = 50
	cfg.Seed = 1337

	g1 := NewGenerator(cfg)
	jobs1 := g1.Generate()

	g2 := NewGenerator(cfg)
	jobs2 := g2.Generate()

	if len(jobs1) != len(jobs2) {
		t.Fatalf("expected job count %d, got %d", len(jobs1), len(jobs2))
	}

	for i := range jobs1 {
		if jobs1[i].ID != jobs2[i].ID {
			t.Errorf("job %d ID mismatch: %s vs %s", i, jobs1[i].ID, jobs2[i].ID)
		}
		if jobs1[i].GPUsPerReplica != jobs2[i].GPUsPerReplica {
			t.Errorf("job %d GPUs mismatch: %d vs %d", i, jobs1[i].GPUsPerReplica, jobs2[i].GPUsPerReplica)
		}
		if jobs1[i].Replicas != jobs2[i].Replicas {
			t.Errorf("job %d replicas mismatch: %d vs %d", i, jobs1[i].Replicas, jobs2[i].Replicas)
		}
	}
}

func TestGenerator_Distributions(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JobCount = 500
	cfg.Seed = 42

	g := NewGenerator(cfg)
	jobs := g.Generate()

	gpuCounts := make(map[int64]int)
	replicaCounts := make(map[int32]int)
	queueCounts := make(map[string]int)
	priorityCounts := make(map[int32]int)

	for _, j := range jobs {
		gpuCounts[j.GPUsPerReplica]++
		replicaCounts[j.Replicas]++
		queueCounts[j.Queue]++
		priorityCounts[j.Priority]++
	}

	for _, expectedGPU := range []int64{1, 2, 4, 8} {
		if count := gpuCounts[expectedGPU]; count == 0 {
			t.Errorf("expected GPU size %d to be generated, got 0", expectedGPU)
		}
	}

	if replicaCounts[1] == 0 {
		t.Errorf("expected single-pod jobs to be generated")
	}
	if replicaCounts[8] == 0 && replicaCounts[16] == 0 {
		t.Errorf("expected large gang jobs (8 or 16) to be generated")
	}

	for _, q := range cfg.Queues {
		if count := queueCounts[q]; count == 0 {
			t.Errorf("expected queue %s to have jobs, got 0", q)
		}
	}

	for _, prio := range []int32{10, 50, 100} {
		if count := priorityCounts[prio]; count == 0 {
			t.Errorf("expected priority %d to have jobs, got 0", prio)
		}
	}
}

func TestGenerator_ToGPUWorkload(t *testing.T) {
	job := JobSpec{
		ID:             "job-0001",
		Name:           "trace-nlp-g4-r2-0001",
		Namespace:      "research",
		Queue:          "team-nlp",
		Priority:       100,
		Replicas:       2,
		MinAvailable:   2,
		GPUsPerReplica: 4,
		TotalGPUs:      8,
		Topology:       "SameNVLinkDomain",
	}

	wl := job.ToGPUWorkload()
	if wl.Name != "trace-nlp-g4-r2-0001" {
		t.Errorf("expected workload name %s, got %s", job.Name, wl.Name)
	}
	if wl.Spec.QueueName != "team-nlp" {
		t.Errorf("expected queue team-nlp, got %s", wl.Spec.QueueName)
	}
	if wl.Spec.Gang.Size != 2 {
		t.Errorf("expected gang size 2, got %d", wl.Spec.Gang.Size)
	}
	if wl.Spec.Topology.Preference != "SameNVLinkDomain" {
		t.Errorf("expected SameNVLinkDomain topology, got %v", wl.Spec.Topology.Preference)
	}

	gpuReq := wl.Spec.PodTemplate.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"]
	if gpuReq.Value() != 4 {
		t.Errorf("expected 4 GPU request, got %d", gpuReq.Value())
	}
}

func TestGenerator_ReplaySerialization(t *testing.T) {
	cfg := DefaultConfig()
	cfg.JobCount = 10
	cfg.Seed = 999

	g := NewGenerator(cfg)
	originalJobs := g.Generate()

	var buf bytes.Buffer
	if err := SaveTrace(&buf, originalJobs); err != nil {
		t.Fatalf("failed to save trace: %v", err)
	}

	replayedJobs, err := LoadTrace(&buf)
	if err != nil {
		t.Fatalf("failed to load trace: %v", err)
	}

	if len(replayedJobs) != len(originalJobs) {
		t.Fatalf("expected %d replayed jobs, got %d", len(originalJobs), len(replayedJobs))
	}
}
