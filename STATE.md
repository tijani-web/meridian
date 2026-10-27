# STATE.md — Meridian Resume State

Machine-readable resume state. Overwritten on every update.

---

## Current Phase
`phase/03-queues-admission`

## Current Branch
`phase/02-workload-controller-fsm`

## Last Green Commit
`1d0cce7` (feat(fsm): implement table-driven GPU workload finite state machine with unit tests)

## Status
`PHASE_02_COMPLETE`

## Open Issues
(none)

## Blockers
(none — autonomy mode active)

## Next 3 Actions
1. Commit Workload Controller implementation and push to `phase/02-workload-controller-fsm`.
2. Merge PR #3 into `main` and push tag `phase-02-done`.
3. Create and switch to branch `phase/03-queues-admission` to implement the Queue Admission Controller and Fair-Share DRF-lite algorithm.

## Phase 02 DoD Checklist
- [x] Pure table-driven FSM in `internal/fsm`
- [x] FSM unit table tests covering all transitions and invalid attempts
- [x] Bounded transition history in status with monotonic sequence counter
- [x] Gated pod creation with scheduling gate (`meridian.dev/admission-gate`)
- [x] Custom field indexers registered on manager informer cache
- [x] Design doc `docs/design/indexers.md` committed
- [x] Workload reconciler implemented with finalizers and SSA status patching
- [x] Envtest integration test suite covering transitions and crash-restart convergence
- [x] `PROGRESS.md` updated

## Completed Phases
- Phase 00 — Bootstrap (completed 2026-10-27)
- Phase 01 — APIs, CRDs, Webhooks, Envtest Harness (completed 2026-10-27)
- Phase 02 — Workload Controller + FSM (completed 2026-10-27)

## Tags Pushed
- phase-00-done
- phase-01-done
- phase-02-done (pending push)
