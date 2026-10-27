package workload

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

const (
	// IndexPodWorkload is the field index key for looking up Pods by their owning GPUWorkload name.
	IndexPodWorkload = "metadata.workloadName"

	// IndexPodNode is the field index key for looking up Pods assigned to a specific Node.
	IndexPodNode = "spec.nodeName"
)

// SetupWorkloadIndexes registers custom field indexers with the manager's cache.
// Must be called during manager setup before informers start.
func SetupWorkloadIndexes(ctx context.Context, indexer client.FieldIndexer) error {
	// Index pods by their owning GPUWorkload name
	if err := indexer.IndexField(ctx, &corev1.Pod{}, IndexPodWorkload, func(rawObj client.Object) []string {
		pod, ok := rawObj.(*corev1.Pod)
		if !ok {
			return nil
		}

		// First check explicit label
		if wlName, exists := pod.Labels[LabelWorkload]; exists && wlName != "" {
			return []string{wlName}
		}

		// Fallback to controller owner reference
		for _, ref := range pod.OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.Kind == "GPUWorkload" && ref.APIVersion == meridianv1alpha1.GroupVersion.String() {
				return []string{ref.Name}
			}
		}

		return nil
	}); err != nil {
		return fmt.Errorf("failed to register pod-workload indexer %q: %w", IndexPodWorkload, err)
	}

	// Index pods by their assigned node name
	if err := indexer.IndexField(ctx, &corev1.Pod{}, IndexPodNode, func(rawObj client.Object) []string {
		pod, ok := rawObj.(*corev1.Pod)
		if !ok || pod.Spec.NodeName == "" {
			return nil
		}
		return []string{pod.Spec.NodeName}
	}); err != nil {
		return fmt.Errorf("failed to register pod-node indexer %q: %w", IndexPodNode, err)
	}

	return nil
}
