# STATE.md — Meridian Resume State

Machine-readable resume state. Overwritten on every update.

---

## Current Phase
`phase/03-queues-admission`

## Current Branch
`phase/03-queues-admission`

## Last Green Commit
`792c451` (Merge PR #3 into main)

## Status
`PHASE_03_READY_FOR_PR`

## Open Issues
(none)

## Blockers
(none — autonomy mode active)

## Next 3 Actions
1. Commit Phase 03 changes: `feat(queue): implement queue admission controller with DRF-lite fair-share and gate release`.
2. Push branch `phase/03-queues-admission`, open PR #4, and merge into `main`.
3. Tag and push `phase-03-done`, then checkout branch `phase/04-scheduler-plugins`.

## Phase 03 DoD Checklist
- [x] Design doc `docs/design/fair-share.md` committed
- [x] DRF-lite dominant share calculation in `internal/controller/queue/drf.go`
- [x] Deterministic victim selection and tie-break ordering
- [x] Property-based tests verifying safety bounds, victim tie-breaking, and termination
- [x] Queue controller with quota accounting (`GPUUsageBreakdown`), SSA status patching, and gate release
- [x] Informer watches on `GPUWorkload` mapping to `GPUQueue`
- [x] Integration envtest suite in `test/envtest/queue_controller_test.go`
- [x] `PROGRESS.md` updated

## Completed Phases
- Phase 00 — Bootstrap (completed 2026-10-27)
- Phase 01 — APIs, CRDs, Webhooks, Envtest Harness (completed 2026-10-27)
- Phase 02 — Workload Controller + FSM (completed 2026-10-27)
- Phase 03 — Queues & Admission (completed 2026-10-27)

## Tags Pushed
- phase-00-done
- phase-01-done
- phase-02-done

