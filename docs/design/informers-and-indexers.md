# Design: Informers and Indexers in Meridian

## Why informers instead of direct API calls

Every reconciliation loop needs to read cluster state — pods belonging to a workload,
nodes with a specific GPU label, queue objects for a given tenant. If each reconcile did
a live `kubectl get` (API call), the controller would add latency to every cycle and
hammer the apiserver at scale.

**Informers** solve this: they maintain a local in-memory cache of the apiserver's state,
kept up-to-date via a long-running **List-Watch** connection. Reconcilers read from this
cache (zero network round-trips) and are notified via a queue when objects change.

controller-runtime wraps the informer machinery transparently: `client.Get` and
`client.List` in a reconciler always read from the cache, never from the apiserver
directly (unless `client.Reader` is used explicitly).

## Indexers in Meridian

An **indexer** registers a secondary index on the informer cache — analogous to a
database index on a non-primary-key column. Without indexers, finding all GPUWorkloads
in a given queue requires a full linear scan of every workload object. With an indexer,
the lookup is O(1) (hash map).

Meridian registers three indexers at manager startup:

```go
// 1. GPUWorkload → QueueName
// Lets the queue controller quickly enumerate all workloads for a given queue
// during admission and requeue cycles.
mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.GPUWorkload{},
    ".spec.queueName",
    func(obj client.Object) []string {
        return []string{obj.(*v1alpha1.GPUWorkload).Spec.QueueName}
    },
)

// 2. Pod → Workload owner (via ownerReference)
// Lets the workload controller reconcile when any owned pod changes phase,
// without scanning all pods in the namespace.
mgr.GetFieldIndexer().IndexField(ctx, &corev1.Pod{},
    ".metadata.ownerReferences[].name",
    func(obj client.Object) []string {
        pod := obj.(*corev1.Pod)
        for _, ref := range pod.OwnerReferences {
            if ref.Kind == "GPUWorkload" {
                return []string{ref.Name}
            }
        }
        return nil
    },
)

// 3. InferenceDeployment → ModelName
// Lets the inference controller look up active deployments for a model
// without scanning all InferenceDeployments cluster-wide.
mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.InferenceDeployment{},
    ".spec.modelName",
    func(obj client.Object) []string {
        return []string{obj.(*v1alpha1.InferenceDeployment).Spec.ModelName}
    },
)
```

Usage in a reconciler:

```go
workloads := &v1alpha1.GPUWorkloadList{}
if err := r.List(ctx, workloads,
    client.InNamespace(req.Namespace),
    client.MatchingFields{".spec.queueName": queueName},
); err != nil {
    return ctrl.Result{}, fmt.Errorf("list workloads for queue %s: %w", queueName, err)
}
```

## Owner references and enqueuing

Meridian uses `ctrl.SetControllerReference` on every Pod created for a GPUWorkload.
When a pod transitions phase (Pending → Running, or Running → Failed), controller-runtime
detects the owner reference and enqueues the owning GPUWorkload for reconciliation.

This is the "child watches parent" pattern: the parent controller does not need to
poll pod state; it is woken up reactively whenever an owned pod changes.

## Watch predicates

To avoid unnecessary reconcile cycles, Meridian applies **predicates** on watches:

```go
ctrl.NewControllerManagedBy(mgr).
    For(&v1alpha1.GPUWorkload{}).
    Owns(&corev1.Pod{}, builder.WithPredicates(
        predicate.Or(
            predicate.GenerationChangedPredicate{},
            predicate.NewPredicateFuncs(func(obj client.Object) bool {
                pod := obj.(*corev1.Pod)
                // Only re-enqueue on phase transitions, not on metadata-only updates.
                return pod.Status.Phase != ""
            }),
        ),
    )).
    Complete(r)
```

This prevents a thundering-herd of reconcile events when annotations are updated
(e.g., checkpoint-ack) without a pod phase change.

## Cache sync at startup

At manager startup, all informer caches must synchronize (complete the initial list
from the apiserver) before any reconciler begins processing. controller-runtime handles
this via `mgr.Start()` blocking until `cache.WaitForCacheSync` returns true. Reconcilers
that read from the cache before sync return `Requeue: true` rather than proceeding with
stale data.
