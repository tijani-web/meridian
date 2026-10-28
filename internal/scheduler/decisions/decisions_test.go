package decisions_test

import (
	"fmt"
	"sync"
	"testing"

	"github.com/tijani-web/meridian/internal/scheduler/decisions"
)

func key(i int) decisions.PodKey {
	return decisions.PodKey{
		Namespace: "default",
		Name:      fmt.Sprintf("pod-%d", i),
		UID:       fmt.Sprintf("uid-%d", i),
	}
}

func TestRecordAndQuery(t *testing.T) {
	r := decisions.New()

	k := key(0)
	r.Record(k, decisions.NodeDecision{Node: "node-a", Verdict: decisions.VerdictFilter, Reason: "InsufficientGPU"})
	r.Record(k, decisions.NodeDecision{Node: "node-b", Verdict: decisions.VerdictScore, CompositeScore: 86})

	ds := r.Query(k)
	if len(ds) != 2 {
		t.Fatalf("expected 2 decisions, got %d", len(ds))
	}
	if ds[0].Node != "node-a" || ds[0].Verdict != decisions.VerdictFilter {
		t.Errorf("unexpected first decision: %+v", ds[0])
	}
	if ds[1].CompositeScore != 86 {
		t.Errorf("expected composite=86, got %d", ds[1].CompositeScore)
	}
}

func TestQueryMissing(t *testing.T) {
	r := decisions.New()
	if ds := r.Query(key(99)); ds != nil {
		t.Errorf("expected nil for missing pod, got %v", ds)
	}
}

func TestEvict(t *testing.T) {
	r := decisions.New()
	k := key(0)
	r.Record(k, decisions.NodeDecision{Node: "node-a", Verdict: decisions.VerdictBound})
	r.Evict(k)
	if r.Query(k) != nil {
		t.Error("expected nil after eviction")
	}
	if r.Len() != 0 {
		t.Errorf("expected len 0 after evict, got %d", r.Len())
	}
}

func TestMaxPodsRetained_EvictsOldest(t *testing.T) {
	r := decisions.New()
	// Fill to capacity
	for i := 0; i < decisions.MaxPodsRetained; i++ {
		r.Record(key(i), decisions.NodeDecision{Node: "node-x", Verdict: decisions.VerdictFilter})
	}
	if r.Len() != decisions.MaxPodsRetained {
		t.Fatalf("expected %d pods, got %d", decisions.MaxPodsRetained, r.Len())
	}
	// Adding one more should evict pod-0
	r.Record(key(decisions.MaxPodsRetained), decisions.NodeDecision{Node: "node-y", Verdict: decisions.VerdictBound})
	if r.Len() != decisions.MaxPodsRetained {
		t.Errorf("len should remain %d after eviction, got %d", decisions.MaxPodsRetained, r.Len())
	}
	if r.Query(key(0)) != nil {
		t.Error("oldest pod-0 should have been evicted")
	}
	if r.Query(key(decisions.MaxPodsRetained)) == nil {
		t.Error("newest pod should be present")
	}
}

func TestMaxNodesPerPod_DropOldest(t *testing.T) {
	r := decisions.New()
	k := key(0)
	for i := 0; i < decisions.MaxNodesPerPod+10; i++ {
		r.Record(k, decisions.NodeDecision{Node: fmt.Sprintf("node-%d", i), Verdict: decisions.VerdictFilter})
	}
	ds := r.Query(k)
	if len(ds) != decisions.MaxNodesPerPod {
		t.Errorf("expected %d node decisions, got %d", decisions.MaxNodesPerPod, len(ds))
	}
	// The first 10 should have been dropped; first remaining is node-10
	if ds[0].Node != "node-10" {
		t.Errorf("expected first node to be node-10, got %s", ds[0].Node)
	}
}

// TestConcurrentSafety exercises concurrent Record/Query/Evict under the race detector.
func TestConcurrentSafety(t *testing.T) {
	r := decisions.New()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(3)
		i := i
		go func() {
			defer wg.Done()
			r.Record(key(i), decisions.NodeDecision{Node: "node-a", Verdict: decisions.VerdictFilter})
		}()
		go func() {
			defer wg.Done()
			_ = r.Query(key(i))
		}()
		go func() {
			defer wg.Done()
			r.Evict(key(i))
		}()
	}
	wg.Wait()
}

// TestMemoryBound documents the worst-case memory footprint (not a runtime assertion).
func TestMemoryBound(t *testing.T) {
	// MaxPodsRetained=1024, MaxNodesPerPod=256, ~256 bytes per NodeDecision
	// Worst case ≈ 1024 × 256 × 256 B ≈ 64 MiB
	const estimatedBytes = decisions.MaxPodsRetained * decisions.MaxNodesPerPod * 256
	t.Logf("Worst-case decision recorder memory bound: ~%d MiB (%d bytes)",
		estimatedBytes/(1024*1024), estimatedBytes)
}
