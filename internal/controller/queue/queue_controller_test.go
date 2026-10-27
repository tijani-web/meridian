package queue_test

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/controller/queue"
)

// TestCalculateWorkloadGPUs verifies calculation of total GPU demands across different gang sizes and pod GPU counts.
func TestCalculateWorkloadGPUs(t *testing.T) {
	testCases := []struct {
		name       string
		gangSize   int32
		gpusPerPod int32
		expected   int32
	}{
		{"Single pod 8 GPUs", 1, 8, 8},
		{"Gang 4 pods 8 GPUs", 4, 8, 32},
		{"Gang 8 pods 4 GPUs", 8, 4, 32},
		{"Gang 16 pods 8 GPUs", 16, 8, 128},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			w := &meridianv1alpha1.GPUWorkload{
				ObjectMeta: metav1.ObjectMeta{Name: "test-w"},
				Spec: meridianv1alpha1.GPUWorkloadSpec{
					Gang:       meridianv1alpha1.GangSpec{Size: tc.gangSize},
					GPUsPerPod: tc.gpusPerPod,
				},
			}
			res := queue.CalculateWorkloadGPUs(w)
			if res != tc.expected {
				t.Fatalf("expected %d GPUs, got %d", tc.expected, res)
			}
		})
	}
}

// TestComputeDominantShare verifies DRF share and weight normalization logic.
func TestComputeDominantShare(t *testing.T) {
	clusterCapacity := map[string]int32{
		"nvidia-h100": 64,
		"nvidia-a100": 32,
	}

	q := &queue.QueueState{
		Name: "team-a",
		NominalQuota: map[string]int32{
			"nvidia-h100": 32, // weight = 32/64 = 0.5
			"nvidia-a100": 8,  // weight = 8/32 = 0.25
		},
		NominalUsed: map[string]int32{
			"nvidia-h100": 16, // share = 16/64 = 0.25
		},
		BorrowedUsed: map[string]int32{
			"nvidia-h100": 0,
		},
	}

	// Dominant share = max(16/64, 0/32) = 0.25
	// Max weight = max(32/64, 8/32) = 0.5
	// Normalized dominant share = 0.25 / 0.5 = 0.5
	share := queue.ComputeDominantShare(q, clusterCapacity)
	expected := 0.5
	if share != expected {
		t.Fatalf("expected normalized dominant share %.2f, got %.2f", expected, share)
	}
}
