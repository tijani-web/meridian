package plugin

import (
	"context"
	"testing"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

func newQueuedPodInfo(pod *v1.Pod) *framework.QueuedPodInfo {
	pi, _ := framework.NewPodInfo(pod)
	return &framework.QueuedPodInfo{PodInfo: pi}
}

func TestFitScore(t *testing.T) {
	cases := []struct {
		name                      string
		capacity, used, requested int64
		wantMin, wantMax          int64
	}{
		{"perfect fill", 8, 7, 1, 99, 100},
		{"aligned leftover 4", 8, 0, 4, 40, 50},
		{"empty node all gpus", 8, 0, 8, 99, 100},
		{"does not fit", 8, 7, 2, 0, 0},
		{"zero capacity", 0, 0, 1, 0, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := fitScore(tc.capacity, tc.used, tc.requested)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("fitScore(%d, %d, %d) = %d, want in range [%d, %d]",
					tc.capacity, tc.used, tc.requested, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestHealthScore(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   int64
	}{
		{map[string]string{labelHealthScore: "100"}, 100},
		{map[string]string{labelHealthScore: "80"}, 80},
		{map[string]string{labelHealthScore: "0"}, 0},
		{map[string]string{}, 100},
		{map[string]string{labelHealthScore: "not-a-number"}, 100},
		{map[string]string{labelHealthScore: "150"}, 100},
	}
	for _, tc := range cases {
		got := healthScore(tc.labels)
		if got != tc.want {
			t.Errorf("healthScore(%v) = %d, want %d", tc.labels, got, tc.want)
		}
	}
}

func TestDistanceAndTopoScore(t *testing.T) {
	nodeA := map[string]string{
		labelNVLinkDomain: "nvlink-0",
		labelRack:         "rack-0",
		labelZone:         "zone-a",
	}
	nodeSameNVLink := map[string]string{
		labelNVLinkDomain: "nvlink-0",
		labelRack:         "rack-0",
		labelZone:         "zone-a",
	}
	nodeSameRack := map[string]string{
		labelNVLinkDomain: "nvlink-1",
		labelRack:         "rack-0",
		labelZone:         "zone-a",
	}
	nodeSameZone := map[string]string{
		labelNVLinkDomain: "nvlink-2",
		labelRack:         "rack-1",
		labelZone:         "zone-a",
	}
	nodeOtherZone := map[string]string{
		labelNVLinkDomain: "nvlink-3",
		labelRack:         "rack-2",
		labelZone:         "zone-b",
	}

	if d := distance(nodeA, nodeSameNVLink); d != 0 || topoScore(d) != 100 {
		t.Errorf("expected distance 0, score 100; got %d, %d", d, topoScore(d))
	}
	if d := distance(nodeA, nodeSameRack); d != 1 || topoScore(d) != 70 {
		t.Errorf("expected distance 1, score 70; got %d, %d", d, topoScore(d))
	}
	if d := distance(nodeA, nodeSameZone); d != 2 || topoScore(d) != 30 {
		t.Errorf("expected distance 2, score 30; got %d, %d", d, topoScore(d))
	}
	if d := distance(nodeA, nodeOtherZone); d != 3 || topoScore(d) != 0 {
		t.Errorf("expected distance 3, score 0; got %d, %d", d, topoScore(d))
	}
}

func TestQueueSort_Less(t *testing.T) {
	p := &Plugin{}
	now := time.Now()

	// High priority vs low priority
	podHighPrio := newQueuedPodInfo(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{annotPriority: "100"},
		},
	})
	podLowPrio := newQueuedPodInfo(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{annotPriority: "10"},
		},
	})
	if !p.Less(podHighPrio, podLowPrio) {
		t.Errorf("expected podHighPrio to rank before podLowPrio")
	}

	// Same priority, FIFO by admission time
	earlier := now.Add(-10 * time.Minute).Format(time.RFC3339Nano)
	later := now.Format(time.RFC3339Nano)
	podEarlier := newQueuedPodInfo(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annotPriority:   "50",
				annotAdmittedAt: earlier,
			},
		},
	})
	podLater := newQueuedPodInfo(&v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				annotPriority:   "50",
				annotAdmittedAt: later,
			},
		},
	})
	if !p.Less(podEarlier, podLater) {
		t.Errorf("expected podEarlier to rank before podLater")
	}
}

func TestFilter(t *testing.T) {
	p := &Plugin{}
	ctx := context.Background()

	pod := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "test-pod",
			Labels: map[string]string{
				labelGPUType: "nvidia-h100",
			},
			Annotations: map[string]string{
				annotGPUsPerPod: "4",
			},
		},
	}

	// Case 1: GPU type mismatch
	nodeWrongType := framework.NewNodeInfo()
	nodeWrongType.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-wrong-type",
			Labels: map[string]string{labelGPUType: "nvidia-a100"},
		},
	})
	status := p.Filter(ctx, nil, pod, nodeWrongType)
	if status.Code() != framework.Unschedulable {
		t.Errorf("expected Unschedulable for GPU type mismatch, got %v", status.Code())
	}

	// Case 2: Insufficient GPU
	nodeInsufficient := framework.NewNodeInfo()
	nodeInsufficient.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-insufficient",
			Labels: map[string]string{labelGPUType: "nvidia-h100"},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceName(resourceNvidiaGPU): resource.MustParse("2"), // only 2, pod wants 4
			},
		},
	})
	status = p.Filter(ctx, nil, pod, nodeInsufficient)
	if status.Code() != framework.Unschedulable {
		t.Errorf("expected Unschedulable for InsufficientGPU, got %v", status.Code())
	}

	// Case 3: Node unhealthy taint
	nodeUnhealthy := framework.NewNodeInfo()
	nodeUnhealthy.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-unhealthy",
			Labels: map[string]string{labelGPUType: "nvidia-h100"},
		},
		Spec: v1.NodeSpec{
			Taints: []v1.Taint{
				{Key: taintGPUUnhealthy, Value: "true", Effect: v1.TaintEffectNoSchedule},
			},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceName(resourceNvidiaGPU): resource.MustParse("8"),
			},
		},
	})
	status = p.Filter(ctx, nil, pod, nodeUnhealthy)
	if status.Code() != framework.Unschedulable {
		t.Errorf("expected Unschedulable for unhealthy taint, got %v", status.Code())
	}

	// Case 4: Healthy matching node
	nodeHealthy := framework.NewNodeInfo()
	nodeHealthy.SetNode(&v1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "node-healthy",
			Labels: map[string]string{labelGPUType: "nvidia-h100"},
		},
		Status: v1.NodeStatus{
			Capacity: v1.ResourceList{
				v1.ResourceName(resourceNvidiaGPU): resource.MustParse("8"),
			},
		},
	})
	status = p.Filter(ctx, nil, pod, nodeHealthy)
	if status != nil && !status.IsSuccess() {
		t.Errorf("expected Success for healthy node, got %v", status)
	}
}

func TestPermitAndReserve(t *testing.T) {
	p := &Plugin{timeout: 30 * time.Second}
	ctx := context.Background()

	cs := framework.NewCycleState()
	gs := &gangState{
		reservedNodes: make(map[int]string),
		minAvailable:  2,
		size:          2,
	}
	cs.Write(cycleStateKey, &stateWrapper{gs})

	pod0 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "gang-pod-0",
			Labels: map[string]string{labelGangIndex: "0"},
		},
	}
	pod1 := &v1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:   "gang-pod-1",
			Labels: map[string]string{labelGangIndex: "1"},
		},
	}

	// Before reserving 0: Permit should return Wait
	status, timeout := p.Permit(ctx, cs, pod0, "node-1")
	if status.Code() != framework.Wait {
		t.Errorf("expected Wait before gang is reserved, got %v", status.Code())
	}
	if timeout != 30*time.Second {
		t.Errorf("expected timeout 30s, got %v", timeout)
	}

	// Reserve pod0
	p.Reserve(ctx, cs, pod0, "node-1")
	if len(gs.reservedNodes) != 1 {
		t.Fatalf("expected 1 reserved node, got %d", len(gs.reservedNodes))
	}

	// Still only 1 reserved < minAvailable (2): should still Wait
	status, _ = p.Permit(ctx, cs, pod0, "node-1")
	if status.Code() != framework.Wait {
		t.Errorf("expected Wait when 1/2 reserved, got %v", status.Code())
	}

	// Reserve pod1
	p.Reserve(ctx, cs, pod1, "node-1")
	if len(gs.reservedNodes) != 2 {
		t.Fatalf("expected 2 reserved nodes, got %d", len(gs.reservedNodes))
	}

	// Now minAvailable reached: Permit should return Success
	status, _ = p.Permit(ctx, cs, pod0, "node-1")
	if status.Code() != framework.Success {
		t.Errorf("expected Success when minAvailable reached, got %v", status.Code())
	}

	// Test Unreserve removes pod1
	p.Unreserve(ctx, cs, pod1, "node-1")
	if len(gs.reservedNodes) != 1 {
		t.Errorf("expected 1 reserved node after unreserve, got %d", len(gs.reservedNodes))
	}
}
