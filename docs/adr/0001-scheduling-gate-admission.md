# docs/adr/0001-scheduling-gate-admission.md
# ADR 0001: Scheduling Gate for Admission Control

**Status**: Accepted  
**Date**: 2026-10-27  
**Deciders**: Basit Tijani

---

## Context

Meridian needs to hold pods from being scheduled until their `GPUWorkload`'s queue
has admitted the workload (quota available, gang size satisfiable). Two patterns exist
in the ecosystem:

1. **Suspend-style** (Kueue pattern): Create pods, then set `spec.suspend = true` on
   the owning Job/workload object. The Job controller stops pods. When admitted, unsuspend.
2. **Scheduling Gate** (`spec.schedulingGates`): Create pods with a gate
   (`meridian.dev/admission`). The scheduler skips gated pods entirely until the gate
   is removed by the queue controller.

## Options Considered

### Option A: Suspend via Job.spec.suspend
- Pros: Well-established pattern (used by Kueue, JobSet). Works with any scheduler.
- Cons: Couples Meridian's admission to the Job API. `GPUWorkload` is not a Job wrapper;
  it manages pods directly. Suspend semantics are on the *workload* not the *pod*, creating
  an awkward two-step. Also: suspend removes running pods — inappropriate if we want to
  stage gating at submission, not restart.

### Option B: Scheduling Gate (chosen)
- Pros: Native Kubernetes mechanism (GA since 1.30). The scheduler simply skips the pod
  until the gate is removed — no pod termination, no extra controller reconcile for
  the unsuspend path. Gate removal is a single patch operation with a clear semantic.
  Gate is visible in `kubectl describe pod` for debuggability. Multiple gates composable.
- Cons: Requires Kubernetes ≥ 1.26 (SchedulingGates feature gate; GA in 1.30). Gate
  removal is irreversible per-pod — but that is appropriate: once admitted, a pod should
  not be re-gated (preemption goes through eviction, not re-gating).

## Decision

Use `spec.schedulingGates: [{name: "meridian.dev/admission"}]` on every pod created by
the workload controller. The queue controller removes this gate atomically for all pods
in a gang when the workload is admitted.

Gate name follows the `<domain>/<name>` convention mandated by the KEP.

## Consequences

- **Positive**: Clean separation of concerns. Admission is a gate-removal, not a state
  machine transition in the scheduler. The scheduler's preemption logic is never involved
  in Meridian's quota enforcement.
- **Positive**: Debuggability — `kubectl describe pod` shows the gate directly.
- **Negative**: Requires Kubernetes ≥ 1.30 (GA). Documented in `docs/LIMITATIONS.md`.
- **Negative**: The gate name must not overlap with other controllers (cert-manager,
  Istio, etc.). Namespaced under `meridian.dev/` to prevent collisions.

## Reference

- KEP-3521: https://github.com/kubernetes/enhancements/issues/3521
- Kueue comparison: https://kueue.sigs.k8s.io/docs/concepts/
