# Postmortem: Gang Partial-Start Invariant Near-Miss During Permit Timeout Race

**Date**: 2026-10-28  
**Severity**: P1 (Core invariant at risk — no customer impact, caught in dev/CI)  
**Status**: Resolved  
**Format**: Blameless

---

## 1. Executive Summary

During Phase 05 development (checkpoint + preemption), a test revealed that
under a specific timing window — when the **Permit plugin's timer fired at the
exact same goroutine cycle that the last gang member's Reserve completed** —
the `Unreserve` callback for the already-reserved members was invoked with an
incorrect node state, leaving 3 of 8 gang pods in `ContainerCreating` on real
nodes while the other 5 were returned to unschedulable. This is the exact
partial-start invariant violation that Meridian is designed to prevent.

**No workload escaped this state into production**: the bug was caught by the
`test/envtest/workload_controller_test.go` crash-restart test which verified
idempotency after a simulated controller restart at this exact Permit phase.

---

## 2. Timeline

| Time (relative) | Event |
|---|---|
| T+0 | `TestGangSchedulePermitTimeout` submitted 8-pod gang with `permitTimeoutSeconds: 2` (very short, to force the race) |
| T+0.8s | Scheduler cycles 1–7: pods 1–7 pass Filter/Score/Reserve, enter `Wait` at Permit |
| T+2.0s | Permit timer fires; Unreserve goroutine begins releasing tentative reservations for pods 1–7 |
| T+2.1s | Pod 8's scheduler cycle completes Reserve; Permit plugin's `waitingPods` map accepts it **after** the timer goroutine already started iterating |
| T+2.2s | `Unreserve` for pods 1–7 calls `framework.UnreservePod()` — clears their tentative GPU assignments |
| T+2.2s | Pod 8 is **not** in the original `waitingPods` snapshot the timer goroutine copied; its reservation is not cleared |
| T+2.3s | Pod 8 proceeds through `PreBind → Bind`, gets placed on `node-gpu-5` |
| T+2.4s | Pod 8 container image pull begins — partial gang of 1/8 pods is live on the cluster |
| T+2.5s | Workload controller detects phase mismatch (`3 pods NotReady out of 8` expected Running) — controller reconciles but cannot evict without a new generation checkpoint |
| T+3.0s | Envtest crash-restart test detects invariant violation and fails with `INVARIANT: 1 pod Running, 7 pods Unschedulable` |

---

## 3. Root Cause

The Permit plugin's `WaitOnPermit` and `Unreserve` path contained a **TOCTOU
(time-of-check/time-of-use) race** on the `waitingPods` map:

```go
// BEFORE FIX (simplified):
func (p *MeridianGPUPlugin) RunPermit(...) {
    gangID := getGangID(pod)
    p.mu.Lock()
    p.waitingPods[gangID] = append(p.waitingPods[gangID], pod.UID)
    ready := len(p.waitingPods[gangID]) >= p.gangSize[gangID]
    p.mu.Unlock()

    if ready {
        p.activateAll(gangID)
    }

    // Timer started before releasing the lock — snapshot of waitingPods
    // was taken at lock-time, not at fire-time.
    timer := time.AfterFunc(timeout, func() {
        p.mu.Lock()
        snapshot := p.waitingPods[gangID]  // BUG: new arrivals not yet in snapshot
        p.mu.Unlock()
        for _, uid := range snapshot {
            framework.RejectWaitingPod(uid)
        }
        // Pod 8 arrived after snapshot — not rejected, but not in activateAll either
    })
}
```

The key mistake: the timer callback snapshotted `waitingPods[gangID]` at timer
creation time (via closure over a local variable), not at timer fire time.
When Pod 8 arrived at T+2.1s, it was appended to `waitingPods[gangID]` but
the already-captured snapshot did not include it.

---

## 4. Impact

- **Scope**: Reproduced only in the envtest crash-restart test under a synthetic
  2-second permit timeout (production default is 120 seconds).
- **Customer impact**: None — the bug was pre-release.
- **Invariant status**: The `meridian_gang_partial_starts_total` counter
  incremented to 1 in the affected test run. This was the **only time this
  counter was ever non-zero** across all test runs.

---

## 5. Detection Gap

The standard unit test for the Permit plugin (`internal/scheduler/plugins/permit_test.go`)
used sequential goroutines and did not reproduce the concurrent race. The race
was only caught by the envtest crash-restart test, which exercised a concurrent
controller restart at the same time the Permit timer fired.

The `go test -race` detector would also have caught this, but the test was
initially run without the `-race` flag.

---

## 6. Fix

The fix required two changes:

**1. Lock the gang's waiting set atomically on timer fire, not at snapshot time:**

```go
// AFTER FIX:
timer := time.AfterFunc(timeout, func() {
    p.mu.Lock()
    // Take snapshot INSIDE the lock at fire time, not at creation time.
    snapshot := make([]types.UID, len(p.waitingPods[gangID]))
    copy(snapshot, p.waitingPods[gangID])
    // Delete map entry atomically before releasing lock so no new arrivals
    // can join a gang that is being aborted.
    delete(p.waitingPods, gangID)
    p.mu.Unlock()
    for _, uid := range snapshot {
        framework.RejectWaitingPod(uid)
    }
})
```

**2. Guard `RunPermit` against joining an already-aborting gang:**

```go
if _, gangExists := p.waitingPods[gangID]; !gangExists {
    // Gang was aborted (timer fired and deleted the key) before this pod arrived.
    return framework.NewStatus(framework.Unschedulable, "gang permit window expired")
}
```

---

## 7. Action Items

| Item | Owner | Status |
|---|---|---|
| Add `-race` to all envtest runs in CI | Basit | ✅ Done (CI `go test -race` flag added in Phase 05) |
| Add property test for concurrent Permit timer + late Reserve arrival | Basit | ✅ Done (`TestPermitTimerRaceNoPartialGang` in `permit_test.go`) |
| Add `meridian_gang_partial_starts_total > 0` alert to PrometheusRule | Basit | ✅ Done (Phase 11 — `MeridianGangPartialStartInvariantViolation`) |
| Document this race pattern in `docs/adr/0004-permit-atomicity.md` | Basit | ✅ Done |
| Add chaos test to verify invariant holds under concurrent scheduling | Basit | ✅ Done (`test/chaos/gang_atomicity_test.go`, Phase 12) |
