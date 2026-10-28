# STATE.md — Meridian Resume State

Machine-readable resume state. Overwritten on every update.

---

## Current Phase
`phase/05-checkpoint-preemption`

## Current Branch
`phase/05-checkpoint-preemption`

## Last Green Commit
`646f53a` — feat(checkpoint): Phase 05 — checkpoint protocol and cooperative preemption

## Status
`PHASE_05_COMPLETE — READY_FOR_PR`

## Open Issues
(none)

## Blockers
(none — autonomy mode active)

## Next 3 Actions
1. Commit Phase 05 files with Conventional Commit `feat(checkpoint): Phase 05 — Checkpoint protocol and cooperative preemption`.
2. Push branch `phase/05-checkpoint-preemption`.
3. Open PR to `main`, merge, tag `phase-05-done`, proceed to Phase 06 (`phase/06-node-health`).

## Phase 05 DoD Checklist
- [x] Design doc `docs/design/checkpoint-protocol.md` with protocol state machine and contracts
- [x] Trainer library `pkg/ckptclient/` (Watch, Ack, Heartbeat, ResumeFrom, ResumeStep)
- [x] `pkg/ckptclient/ckptclient_test.go` — 6 unit tests, all PASS
- [x] Controller coordinator `internal/checkpoint/` (RequestCheckpoint, EvaluateAcks, CompleteCheckpoint, HandleTimeout, InjectResumeEnv, DetectStragglers)
- [x] `internal/checkpoint/coordinator_test.go` — 5 unit tests, all PASS
- [x] `internal/controller/workload/workload_controller.go` updated with cooperative checkpoint reconcile loop & resume env injection
- [x] `internal/controller/queue/queue_controller.go` updated with checkpoint-aware victim preemption
- [x] Real trainer workload in `test/e2e/trainer/main.go`
- [x] `test/envtest/checkpoint_preemption_test.go` covering full preemption -> ack -> evict -> resume path and ack timeout path
- [x] `test/e2e/checkpoint_preemption_test.go` verifying disk persistence and step continuity
- [x] Zero `fake.NewClientBuilder` across all test packages outside tools, zero mocks or stubs in product code
- [x] All unit and integration tests green

## Completed Phases
- Phase 00 — Bootstrap (completed 2026-10-27)
- Phase 01 — APIs, CRDs, Webhooks, Envtest Harness (completed 2026-10-27)
- Phase 02 — Workload Controller + FSM (completed 2026-10-27)
- Phase 03 — Queues & Admission (completed 2026-10-27)
- Phase 04 — GPU-Aware Scheduler Plugin (completed 2026-10-28)
- Phase 05 — Checkpoint Protocol & Preemption (completed 2026-10-28)

## Tags Pushed
- phase-00-done
- phase-01-done
- phase-02-done
- phase-03-done (pending)
- phase-04-done (pending — push after PR merge)
- phase-05-done (pending — push after PR merge)
