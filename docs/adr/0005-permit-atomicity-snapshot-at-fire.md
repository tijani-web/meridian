# ADR-0005: Permit Plugin Atomicity — Snapshot-at-Fire-Time for Gang Abort

**Date**: 2026-10-28  
**Status**: Accepted  
**Deciders**: Basit (solo)  
**Context**: Phase 05 bug (gang partial-start near-miss); see `docs/postmortems/2026-10-28-gang-permit-partial-start-near-miss.md`

---

## Context

Meridian's strongest scheduling guarantee (Meridian.md §8.1) is:

> _"Number of partially-started gangs = 0 (must be 0 for Meridian)"_

This is enforced by the Permit plugin. When a gang's permit timer fires, the
plugin must atomically reject **all** waiting pod UIDs at that instant — including
any pod that arrived between the timer being created and the timer firing.

The original implementation contained a TOCTOU race: the timer goroutine captured
a snapshot of `waitingPods[gangID]` at timer-creation time via a closure, so late
arrivals (pods whose Reserve completed after the timer was armed) were never rejected
and could complete their Bind while the rest of the gang was unreserved.

---

## Decision

The Permit plugin adopts **snapshot-at-fire-time** semantics:

1. **Timer goroutine holds the mutex when building the rejection snapshot**. It takes
   the snapshot and deletes the gang's map key atomically in a single critical section.
2. **Late arrivals check for gang existence before appending**. If `waitingPods[gangID]`
   has been deleted (i.e., the abort is in progress), `RunPermit` returns
   `Unschedulable` immediately.
3. **No pod can join a gang after its abort has started**. The deleted key acts as a
   tombstone; the timer goroutine does not need a second pass.

```go
// Timer goroutine (inside time.AfterFunc callback):
p.mu.Lock()
snapshot := make([]types.UID, len(p.waitingPods[gangID]))
copy(snapshot, p.waitingPods[gangID])
delete(p.waitingPods, gangID) // tombstone — no new arrivals can join
p.mu.Unlock()
for _, uid := range snapshot {
    framework.RejectWaitingPod(uid)
}

// RunPermit — late arrival guard:
p.mu.Lock()
if _, exists := p.waitingPods[gangID]; !exists {
    p.mu.Unlock()
    return framework.NewStatus(framework.Unschedulable, "gang permit window expired")
}
p.waitingPods[gangID] = append(p.waitingPods[gangID], pod.UID)
p.mu.Unlock()
```

---

## Alternatives Considered

### A: Channel-based rendezvous

Use a per-gang `chan struct{}` closed by the timer to signal abort. Pods arriving
after close immediately see the closed channel and return Unschedulable.

**Rejected**: Requires a cleanup goroutine to garbage-collect per-gang channels and
adds coordination complexity. The mutex+delete approach achieves the same invariant
with simpler code that `go test -race` can verify directly.

### B: Versioned generation field on the gang state

Each gang tracking entry carries a `generation` int. The timer captures the
generation at arm-time; on fire, it only rejects pods whose generation matches.

**Rejected**: Generations still require a lock to increment atomically; the
simpler delete-as-tombstone approach needs no generation bookkeeping.

---

## Consequences

- **`go test -race` verifiable**: The fix was validated with `go test -race ./internal/scheduler/plugins/...`. No data-race detected.
- **Property test**: `TestPermitTimerRaceNoPartialGang` in `permit_test.go` exercises 100 concurrent goroutines all racing to join a gang whose timer fires at random intervals, verifying `meridian_gang_partial_starts_total` stays at 0.
- **Metrics alert**: `MeridianGangPartialStartInvariantViolation` alert fires if this counter ever exceeds 0 in production (Phase 11 PrometheusRule).
- **Chaos test**: `test/chaos/gang_atomicity_test.go` exercises this under real concurrent capacity churn on a kind cluster (Phase 12).
