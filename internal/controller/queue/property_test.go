package queue_test

import (
	"fmt"
	"math/rand"
	"testing"
	"time"

	"github.com/tijani-web/meridian/internal/controller/queue"
)

// TestPropertySafetyBound verifies that under arbitrary sequences of admissions,
// a queue's allocation never exceeds Nominal + BorrowingLimit.
func TestPropertySafetyBound(t *testing.T) {
	rng := rand.New(rand.NewSource(42))

	for iter := 0; iter < 1000; iter++ {
		nominal := int32(rng.Intn(64) + 8)
		borrowLimit := int32(rng.Intn(32) + 4)
		clusterIdle := int32(rng.Intn(128))

		q := &queue.QueueState{
			Name:           "test-queue",
			NominalQuota:   map[string]int32{"nvidia-h100": nominal},
			BorrowingLimit: map[string]int32{"nvidia-h100": borrowLimit},
			NominalUsed:    make(map[string]int32),
			BorrowedUsed:   make(map[string]int32),
		}

		idleCap := map[string]int32{"nvidia-h100": clusterIdle}

		// Simulate random workload arrival requests
		for req := 0; req < 20; req++ {
			requested := int32(rng.Intn(16) + 1)
			decision, borrowedDeficit := queue.EvaluateAdmission(q, "nvidia-h100", requested, idleCap)

			switch decision {
			case queue.DecisionAdmitNominal:
				q.NominalUsed["nvidia-h100"] += requested
			case queue.DecisionAdmitBorrowed:
				remNom := nominal - q.NominalUsed["nvidia-h100"]
				if remNom > 0 {
					q.NominalUsed["nvidia-h100"] += remNom
				}
				q.BorrowedUsed["nvidia-h100"] += borrowedDeficit
				idleCap["nvidia-h100"] -= borrowedDeficit
			}

			// Invariant Check
			totalUsed := q.NominalUsed["nvidia-h100"] + q.BorrowedUsed["nvidia-h100"]
			maxAllowed := nominal + borrowLimit
			if totalUsed > maxAllowed {
				t.Fatalf("Iteration %d: Safety bound violated! Total used %d > Max allowed %d (Nominal: %d, BorrowLimit: %d)",
					iter, totalUsed, maxAllowed, nominal, borrowLimit)
			}
			if q.NominalUsed["nvidia-h100"] > nominal {
				t.Fatalf("Iteration %d: Nominal used %d > Nominal quota %d",
					iter, q.NominalUsed["nvidia-h100"], nominal)
			}
			if q.BorrowedUsed["nvidia-h100"] > borrowLimit {
				t.Fatalf("Iteration %d: Borrowed used %d > Borrow limit %d",
					iter, q.BorrowedUsed["nvidia-h100"], borrowLimit)
			}
		}
	}
}

// TestPropertyVictimSelectionTieBreak verifies strict ordering:
// 1. Borrowed first
// 2. Highest dominant share
// 3. Lowest priority
// 4. LIFO admission time
func TestPropertyVictimSelectionTieBreak(t *testing.T) {
	t0 := time.Now()
	t1 := t0.Add(1 * time.Minute)
	t2 := t0.Add(2 * time.Minute)

	candidates := []queue.WorkloadCandidate{
		{
			Name:         "nominal-high-prio",
			IsBorrowed:   false,
			DominantRate: 0.5,
			Priority:     100,
			AdmittedAt:   t0,
			TotalGPUs:    8,
		},
		{
			Name:         "borrowed-low-share",
			IsBorrowed:   true,
			DominantRate: 0.2,
			Priority:     50,
			AdmittedAt:   t1,
			TotalGPUs:    8,
		},
		{
			Name:         "borrowed-high-share",
			IsBorrowed:   true,
			DominantRate: 0.8,
			Priority:     50,
			AdmittedAt:   t0,
			TotalGPUs:    8,
		},
		{
			Name:         "borrowed-high-share-recent",
			IsBorrowed:   true,
			DominantRate: 0.8,
			Priority:     50,
			AdmittedAt:   t2, // More recent than t0 -> evicted first
			TotalGPUs:    8,
		},
	}

	// Request 16 GPUs -> should pick the two borrowed high-share workloads
	victims := queue.SelectVictims(candidates, 16)
	if len(victims) != 2 {
		t.Fatalf("expected 2 victims, got %d", len(victims))
	}

	if victims[0].Name != "borrowed-high-share-recent" {
		t.Errorf("expected victim 0 to be 'borrowed-high-share-recent', got %s", victims[0].Name)
	}
	if victims[1].Name != "borrowed-high-share" {
		t.Errorf("expected victim 1 to be 'borrowed-high-share', got %s", victims[1].Name)
	}

	// Request 32 GPUs -> all borrowed must be chosen before nominal
	allVictims := queue.SelectVictims(candidates, 32)
	for i := 0; i < 3; i++ {
		if !allVictims[i].IsBorrowed {
			t.Errorf("expected victim %d to be borrowed, got %s", i, allVictims[i].Name)
		}
	}
	if allVictims[3].Name != "nominal-high-prio" {
		t.Errorf("expected last victim to be nominal, got %s", allVictims[3].Name)
	}
}

// TestPropertyReclaimTermination verifies that reclaim terminates in finite steps <= len(candidates).
func TestPropertyReclaimTermination(t *testing.T) {
	rng := rand.New(rand.NewSource(99))

	for iter := 0; iter < 100; iter++ {
		nCandidates := rng.Intn(50) + 1
		candidates := make([]queue.WorkloadCandidate, nCandidates)

		var totalAvailable int32
		for i := 0; i < nCandidates; i++ {
			gpus := int32(rng.Intn(16) + 1)
			candidates[i] = queue.WorkloadCandidate{
				Name:         fmt.Sprintf("w-%d", i),
				TotalGPUs:    gpus,
				IsBorrowed:   rng.Intn(2) == 1,
				DominantRate: rng.Float64(),
				Priority:     int32(rng.Intn(100)),
				AdmittedAt:   time.Now().Add(time.Duration(i) * time.Second),
			}
			totalAvailable += gpus
		}

		needed := int32(rng.Intn(int(totalAvailable) + 20))
		victims := queue.SelectVictims(candidates, needed)

		if len(victims) > len(candidates) {
			t.Fatalf("victims count %d > candidates count %d", len(victims), len(candidates))
		}

		var reclaimed int32
		for _, v := range victims {
			reclaimed += v.TotalGPUs
		}

		if needed <= totalAvailable && reclaimed < needed {
			t.Fatalf("insufficient reclamation: needed %d, got %d (total available: %d)",
				needed, reclaimed, totalAvailable)
		}
	}
}
