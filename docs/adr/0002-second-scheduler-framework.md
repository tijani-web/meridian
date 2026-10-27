# docs/adr/0002-second-scheduler-framework.md
# ADR 0002: Out-of-Tree Scheduler via Scheduler Framework

**Status**: Accepted  
**Date**: 2026-10-27  
**Deciders**: Basit Tijani

---

## Context

Meridian requires GPU-aware scheduling with custom extension points:
gang Permit, topology-aware scoring, fragmentation-aware bin-packing,
decision recording, and integration with the custom preemption path.

Two approaches exist in the Kubernetes ecosystem:

1. **Extend the default scheduler** via `KubeSchedulerProfile` and the scheduler
   framework plugin interface, deployed as a separate `meridian-scheduler` binary
   with `schedulerName: meridian-scheduler` on Meridian pods.
2. **Use an external scheduler** (e.g., Volcano, Yunikorn) and configure it for
   GPU scheduling.

## Options Considered

### Option A: Volcano or Yunikorn (external scheduler)
- Pros: Mature gang scheduling support (Volcano has production use). Less code.
- Cons: Opaque internals — cannot demonstrate K8s scheduler framework internals.
  Portfolio goal explicitly requires demonstrating QueueSort, Filter, Score,
  Reserve, Permit, etc. as real code. Volcano's gang model differs from Meridian's
  checkpoint-aware preemption model. Forking Volcano would be worse than writing
  the framework plugins directly.

### Option B: Scheduler Framework Plugins (chosen)
- Pros: Demonstrates K8s internals directly (§10 checklist). Precise control over
  every extension point. Decision recorder is trivially inserted into the framework's
  cycle callbacks. Can reference `sigs.k8s.io/scheduler-plugins` for structural
  patterns without copying code.
- Cons: More code to write. Framework API can break between minor K8s versions —
  mitigated by pinning to the same version as the kind node image.

## Decision

Build `meridian-scheduler` as a Go binary that imports
`k8s.io/kubernetes/pkg/scheduler/framework` and registers custom plugins:
- `MeridianQueueSort` (QueueSort)
- `MeridianPreFilter` (PreFilter)
- `MeridianFilter` (Filter)
- `MeridianScore` + `MeridianNormalizeScore` (Score + NormalizeScore)
- `MeridianReserve` (Reserve + Unreserve)
- `MeridianPermit` (Permit — gang scheduling)
- `MeridianBind` (PreBind + default Bind)

Deployed as a separate Deployment using `KubeSchedulerConfiguration` profile.
Meridian pods set `spec.schedulerName: meridian-scheduler`.
Default scheduler handles all other pods undisturbed.

Disable default `DefaultPreemption` PostFilter for Meridian pods; preemption
lives at the queue controller layer (see ADR 0003).

## Consequences

- **Positive**: Every extension point is real, testable, demonstrable.
- **Positive**: `meridian-scheduler` can be run alongside the default scheduler
  safely; no risk to non-Meridian pods.
- **Negative**: Must track Kubernetes API versions. Pinned to k8s.io/kubernetes
  matching the node image version (see `docs/VERSIONS.md`).
- **Negative**: Using the `k8s.io/kubernetes` module directly (the "staging" import
  approach) requires using `replace` directives or the scheduler-plugins build pattern.
  This is documented in `docs/design/informers-and-indexers.md`.

## Reference

- Scheduler framework: https://kubernetes.io/docs/concepts/scheduling-eviction/scheduling-framework/
- scheduler-plugins repo: https://github.com/kubernetes-sigs/scheduler-plugins
