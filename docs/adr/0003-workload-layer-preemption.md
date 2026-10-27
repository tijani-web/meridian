# docs/adr/0003-workload-layer-preemption.md
# ADR 0003: Preemption at the Workload Layer, Not the Scheduler PostFilter

**Status**: Accepted  
**Date**: 2026-10-27  
**Deciders**: Basit Tijani

---

## Context

Kubernetes has a built-in preemption mechanism (`DefaultPreemption` PostFilter plugin)
that evicts lower-priority pods to make room for a higher-priority pending pod.
This happens inside the scheduler cycle and is purely node-resource-based.

Meridian requires preemption that is:
1. **Quota-aware**: reclaim borrowed quota from a lower-priority queue.
2. **Checkpoint-aware**: signal the victim workload to checkpoint before eviction,
   wait for acks, then evict — avoiding wasted training progress.
3. **Gang-aware**: evict an entire gang atomically, not one pod at a time.
4. **Policy-controlled**: tie-break by queue priority, admission time, and whether
   the workload is borrowing quota.

The default scheduler PostFilter cannot satisfy these requirements — it has no
knowledge of checkpoints, quotas, or gang atomicity.

## Options Considered

### Option A: Override DefaultPreemption (PostFilter plugin)
- Pros: Integrates into the scheduler cycle; gets the scheduler's native preemption
  victim selection and pod-deletion ordering.
- Cons: PostFilter runs in the scheduler goroutine with a short timeout. Waiting for
  checkpoint acks (up to `ackTimeout`, typically 5–30 minutes for large training jobs)
  is impossible here. The PostFilter also has no access to queue quota state.

### Option B: Workload-layer preemption in the queue controller (chosen)
- The queue controller watches for quota pressure, selects victims by policy
  (borrowed first, lowest priority, most recently admitted — documented in
  `docs/design/fair-share.md`), patches the victim workload to `Preempting` phase,
  which signals the checkpoint controller to coordinate acks and evictions.
- `DefaultPreemption` is **disabled** for the `meridian-scheduler` profile so it
  never fires for Meridian pods.

## Decision

Preemption is triggered and orchestrated entirely in the queue controller.
The scheduler's `DefaultPreemption` PostFilter is removed from the
`meridian-scheduler` profile.

Preemption flow:
1. Queue controller detects quota pressure (queue needs nominal back from a borrower).
2. Selects victim workload(s) by: borrowed quota first → lowest priority → most recently admitted.
3. Transitions victim to `Preempting` phase via SSA status patch.
4. Workload controller + checkpoint controller take over: signal pods, wait for acks.
5. On ack or timeout → evict pods → transition to `Recovering`.
6. Queue controller re-evaluates quota, admits next workload.

## Consequences

- **Positive**: Checkpoint-aware preemption is fully controlled. No race between
  the scheduler and the controller.
- **Positive**: Clean audit trail: every preemption is recorded in the workload
  status history with reason, duration, and checkpoint ack status.
- **Negative**: A pending high-priority pod that cannot fit will not be scheduled
  until the queue controller's preemption loop runs (typically within one reconcile
  cycle, ~1–2 seconds). The default scheduler's preemption acts within the same
  scheduling cycle — Meridian accepts this small latency for correctness.
- **Negative**: Must document that `spec.priorityClassName` alone does not trigger
  Kubernetes-native preemption for Meridian pods; only Meridian's own policy applies.
