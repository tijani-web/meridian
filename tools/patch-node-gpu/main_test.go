package main

import (
	"testing"

	v1 "k8s.io/api/core/v1"
)

func TestApplyGPUSimulation(t *testing.T) {
	node := &v1.Node{}
	applyGPUSimulation(node, 8)

	// Check label
	if node.Labels["meridian.dev/health-score"] != "100" {
		t.Fatalf("expected health-score label 100, got %s", node.Labels["meridian.dev/health-score"])
	}

	// Check GPU capacity
	gpuResName := v1.ResourceName("nvidia.com/gpu")
	capQty, ok := node.Status.Capacity[gpuResName]
	if !ok || capQty.Value() != 8 {
		t.Fatalf("expected 8 GPUs in capacity, got %v", capQty)
	}

	// Check GPU allocatable
	allocQty, ok := node.Status.Allocatable[gpuResName]
	if !ok || allocQty.Value() != 8 {
		t.Fatalf("expected 8 GPUs in allocatable, got %v", allocQty)
	}
}
