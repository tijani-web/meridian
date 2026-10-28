# STATE.md — Meridian Resume State

Machine-readable resume state. Overwritten on every update.

---

## Current Phase
`phase/04-scheduler-plugins`

## Current Branch
`phase/04-scheduler-plugins`

## Last Green Commit
`5e6730f` — feat(scheduler): Phase 04 — GPU-aware out-of-tree scheduler plugin

## Status
`PHASE_04_COMPLETE — READY_FOR_PR`

## Open Issues
(none)

## Blockers
(none — autonomy mode active)

## Next 3 Actions
1. Open PR from `phase/04-scheduler-plugins` into `main`, merge.
2. Tag and push `phase-04-done`.
3. Checkout branch `phase/05-checkpoint-preemption`, start Phase 05.

## Phase 04 DoD Checklist
- [x] Design doc `docs/design/scoring.md` with full formulation
- [x] `cmd/scheduler/go.mod` — isolated sub-module with k8s monorepo replace tree
- [x] `cmd/scheduler/plugin/plugin.go` — MeridianGPU plugin (QueueSort, PreFilter, Filter, Score, Reserve, Permit)
- [x] `cmd/scheduler/plugin/plugin_test.go` — 6 unit tests, all PASS
- [x] `internal/scheduler/topology/` — topology distance helpers + tests
- [x] `internal/scheduler/scoring/` — scoring helpers + tests
- [x] `internal/scheduler/decisions/` — decision ring-buffer recorder + tests (incl. concurrency)
- [x] `deploy/scheduler/deployment.yaml` — HA 2-replica scheduler Deployment + RBAC + ConfigMap
- [x] `tools/patch-node-gpu/main.go` — kind node GPU patcher
- [x] `GOOS=linux GOARCH=amd64` binary built → `bin/meridian-scheduler-linux` (100 MiB)
- [x] All tests green: 18 tests across 4 packages
- [x] Commit `5e6730f` pushed to `phase/04-scheduler-plugins`
- [x] `PROGRESS.md` updated

## Completed Phases
- Phase 00 — Bootstrap (completed 2026-10-27)
- Phase 01 — APIs, CRDs, Webhooks, Envtest Harness (completed 2026-10-27)
- Phase 02 — Workload Controller + FSM (completed 2026-10-27)
- Phase 03 — Queues & Admission (completed 2026-10-27)
- Phase 04 — GPU-Aware Scheduler Plugin (completed 2026-10-28)

## Tags Pushed
- phase-00-done
- phase-01-done
- phase-02-done
- phase-03-done (pending)
- phase-04-done (pending — push after PR merge)
