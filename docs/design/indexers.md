# Design: Informer Cache Field Indexers

**Author**: Meridian Team  
**Status**: Accepted  
**Requirement**: Meridian.md §6.1 — "Shared informer cache with custom field indexers (pod → workload, node → pods) — required, document in a design doc."

---

## 1. Context & Motivation

Meridian manages distributed GPU training and inference workloads spanning thousands of pods across heterogeneous GPU clusters. At scale (e.g. 10,000 nodes, 80,000 GPUs), standard client-side filtering over all cluster pods creates significant memory and CPU bottlenecks during controller reconciliation:

1. **Workload Pod Enumeration**: When reconciling a `GPUWorkload`, the controller needs to inspect all active gang member pods (e.g., to aggregate phase, check pod scheduling gates, or collect node assignments).
2. **Node Health Invalidation**: When an agent detects a GPU hardware fault on a node (e.g. XID error, bus drop, thermal throttling), the health controller must immediately identify all gang member pods residing on that node to initiate checkpointing or failover.

Without field indexers, both lookups require full namespace or cluster-wide `List` scans with client-side label filtering ($O(N)$ over all cluster pods).

## 2. Implemented Field Indexers

Meridian registers two high-performance custom field indexers on the controller-runtime shared informer cache during manager setup:

### 2.1 Pod → Workload Indexer (`IndexPodWorkload`)

- **Field Name**: `metadata.workloadName`
- **Target Object**: `corev1.Pod`
- **Index Extraction Logic**:
  1. Inspects `pod.Labels["meridian.dev/workload"]`.
  2. If present, returns `[]string{workloadName}`.
  3. If absent, inspects `pod.OwnerReferences` for a controlling owner of kind `GPUWorkload` (`apiGroup: meridian.dev`).
  4. Returns empty slice for unmanaged pods.
- **Access Pattern**:
  ```go
  var pods corev1.PodList
  err := r.List(ctx, &pods,
      client.InNamespace(workload.Namespace),
      client.MatchingFields{"metadata.workloadName": workload.Name},
  )
  ```
- **Complexity**: $O(K)$ where $K$ is the gang size ($K \ll N$), reducing reconcile latency from $O(N)$ to constant-bounded time.

### 2.2 Node → Pods Indexer (`IndexPodNode`)

- **Field Name**: `spec.nodeName`
- **Target Object**: `corev1.Pod`
- **Index Extraction Logic**:
  1. Inspects `pod.Spec.NodeName`.
  2. If non-empty, returns `[]string{pod.Spec.NodeName}`.
  3. If empty (unscheduled or pending bind), returns `nil`.
- **Access Pattern**:
  ```go
  var nodePods corev1.PodList
  err := r.List(ctx, &nodePods,
      client.MatchingFields{"spec.nodeName": failedNodeName},
  )
  ```
- **Complexity**: $O(P_{\text{node}})$ where $P_{\text{node}}$ is the number of pods on the specific node (typically $\le 8$ for multi-GPU servers), enabling sub-millisecond node-fault reaction time.

## 3. Informer Setup & Safety

Both indexers are registered before any informers start:

```go
func SetupWorkloadIndexes(ctx context.Context, indexer client.FieldIndexer) error {
    if err := indexer.IndexField(ctx, &corev1.Pod{}, IndexPodWorkload, extractPodWorkload); err != nil {
        return fmt.Errorf("failed to register pod-workload indexer: %w", err)
    }
    if err := indexer.IndexField(ctx, &corev1.Pod{}, IndexPodNode, extractPodNode); err != nil {
        return fmt.Errorf("failed to register pod-node indexer: %w", err)
    }
    return nil
}
```

Attempting to register field indexers after the informer cache has started will trigger a panic in controller-runtime; therefore, indexers are registered unconditionally in `SetupWithManager` before manager start.
