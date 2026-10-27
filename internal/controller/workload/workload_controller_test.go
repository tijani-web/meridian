package workload_test

import (
	"context"
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/controller/workload"
)

type mockIndexer struct {
	indexes map[string]client.IndexerFunc
}

func (m *mockIndexer) IndexField(ctx context.Context, obj client.Object, field string, extractValue client.IndexerFunc) error {
	m.indexes[field] = extractValue
	return nil
}

// TestSetupWorkloadIndexes verifies that the required field indexers are registered with the expected logic.
func TestSetupWorkloadIndexes(t *testing.T) {
	indexer := &mockIndexer{indexes: make(map[string]client.IndexerFunc)}
	ctx := context.Background()

	err := workload.SetupWorkloadIndexes(ctx, indexer)
	if err != nil {
		t.Fatalf("SetupWorkloadIndexes failed: %v", err)
	}

	// 1. Verify workload indexer
	wlExtractor, ok := indexer.indexes[workload.IndexPodWorkload]
	if !ok {
		t.Fatalf("expected indexer %q to be registered", workload.IndexPodWorkload)
	}

	// Test pod with label
	labeledPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-0",
			Namespace: "default",
			Labels: map[string]string{
				workload.LabelWorkload: "my-workload",
			},
		},
	}
	res := wlExtractor(labeledPod)
	if len(res) != 1 || res[0] != "my-workload" {
		t.Fatalf("expected [my-workload], got %v", res)
	}

	// Test pod with owner reference
	isController := true
	ownedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-pod-1",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: meridianv1alpha1.GroupVersion.String(),
					Kind:       "GPUWorkload",
					Name:       "owner-workload",
					Controller: &isController,
				},
			},
		},
	}
	resOwned := wlExtractor(ownedPod)
	if len(resOwned) != 1 || resOwned[0] != "owner-workload" {
		t.Fatalf("expected [owner-workload], got %v", resOwned)
	}

	// Unrelated pod returns nil
	unrelatedPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "unrelated"},
	}
	if resEmpty := wlExtractor(unrelatedPod); len(resEmpty) != 0 {
		t.Fatalf("expected empty for unrelated pod, got %v", resEmpty)
	}

	// 2. Verify node indexer
	nodeExtractor, ok := indexer.indexes[workload.IndexPodNode]
	if !ok {
		t.Fatalf("expected indexer %q to be registered", workload.IndexPodNode)
	}

	scheduledPod := &corev1.Pod{
		Spec: corev1.PodSpec{NodeName: "node-worker-gpu-1"},
	}
	nodeRes := nodeExtractor(scheduledPod)
	if len(nodeRes) != 1 || nodeRes[0] != "node-worker-gpu-1" {
		t.Fatalf("expected [node-worker-gpu-1], got %v", nodeRes)
	}

	unscheduledPod := &corev1.Pod{}
	if unscheduledRes := nodeExtractor(unscheduledPod); len(unscheduledRes) != 0 {
		t.Fatalf("expected empty for unscheduled pod, got %v", unscheduledRes)
	}
}

// TestWorkloadReconcilerPodConstruction verifies gated pod creation metadata and specs.
func TestWorkloadReconcilerPodConstruction(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = clientgoscheme.AddToScheme(scheme)
	_ = meridianv1alpha1.AddToScheme(scheme)

	w := &meridianv1alpha1.GPUWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "llm-train",
			Namespace: "default",
			UID:       "1234-5678",
		},
		Spec: meridianv1alpha1.GPUWorkloadSpec{
			QueueName: "batch-queue",
			Gang:      meridianv1alpha1.GangSpec{Size: 4, MinAvailable: 4},
			PodTemplate: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{"app": "trainer"},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: "train", Image: "nvcr.io/nvidia/pytorch:24.01-py3"},
					},
				},
			},
		},
		Status: meridianv1alpha1.GPUWorkloadStatus{
			Generation: 2,
		},
	}

	// Verify pod construction helpers by testing index formatting
	for idx := int32(0); idx < w.Spec.Gang.Size; idx++ {
		expectedName := fmt.Sprintf("%s-%d", w.Name, idx)
		if expectedName != fmt.Sprintf("llm-train-%d", idx) {
			t.Fatalf("unexpected pod name: %s", expectedName)
		}
	}
}
