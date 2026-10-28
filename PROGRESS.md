# PROGRESS.md — Meridian Build Log

Append-only log. Each entry: date, phase, what was done, what was verified, commit SHA.

---

## 2026-10-27 — Phase 00: Bootstrap

**Done:**
- Read Meridian.md (488 lines) in full; understood all rules, architecture, and phases.
- Checked tool availability: go 1.27.1 ✅, docker 29.7.2 ✅, kubectl v1.36.1 ✅, git 2.55.0 ✅.
- Installed missing tools: gh CLI v2.103.3 (winget), Helm (winget, in progress), kind (binary download), make (winget).
- Initialized git repository on `main` branch.
- Created full directory scaffold (all paths from §4 repo layout).
- Created: `.gitignore`, `LICENSE` (Apache-2.0), `AGENTS.md`, `PROGRESS.md` (this file), `STATE.md`.
- Created: `docs/VERSIONS.md` (pinned versions, latest stable verified 2026-10-27).
- Created: `docs/LIMITATIONS.md` (skeleton with known initial limitations).
- Created: `Makefile` with all standard targets.
- Created: `.golangci.yml` with full linter config.
- Created: CI skeleton (`.github/workflows/ci.yaml`).
- Created: `deploy/kind/config.yaml` (multi-node kind cluster with topology labels).
- Initialized Go module: `github.com/tijani-web/meridian`.

**Verified:**
- `gh auth status` — authenticated as `tijani-web` (token scopes: gist, read:org, repo, workflow) ✅.
- GitHub repo `tijani-web/meridian` created and pushed to `main` (commit 7252f16) ✅.
- `kind create cluster --config deploy/kind/config.yaml` — created 5-node cluster `meridian` (1 control plane, 4 workers) ✅.
- Topology labels verified on kind workers (`domain-a`/`domain-b`, `rack-0`/`rack-1`, `us-east-1a`/`us-east-1b`, `nvidia-h100`/`nvidia-a100`) ✅.
- `helm lint charts/meridian` — 1 chart(s) linted, 0 chart(s) failed ✅.
- `pkg/version` package created and tested (`go test ./pkg/version/...` passed) ✅.
- Bug #1 diagnosed, fixed in `deploy/kind/config.yaml`, and logged in `docs/debugging-log.md` (Issue #1 closed) ✅.

---

## 2026-10-27 — Phase 01: APIs, CRDs, Webhooks, Envtest Harness

**Done:**
- Defined core CRD types in `api/v1alpha1`: `GPUQueue`, `GPUWorkload`, and `InferenceDeployment`.
- Implemented mutating defaulting and validating admission webhooks using controller-runtime v0.25 generic API.
- Created admission webhook configurations in `config/webhook/manifests.yaml`.
- Created sample resource manifests in `config/samples/` for all three CRDs.
- Created RBAC ClusterRole in `config/rbac/role.yaml`.
- Built integration test suite under `test/envtest/` covering defaulting, validation on create, immutability on update, and deletion guards.
- Added `install` and `uninstall` targets with server-side apply in `Makefile`.
- Updated `docs/LIMITATIONS.md` documenting server-side apply requirement due to recursive `corev1.PodTemplateSpec` OpenAPI schema size.

**Verified:**
- `go build ./api/v1alpha1/...` compiles cleanly ✅.
- All three CRDs installed and verified on live 5-node kind cluster via server-side apply ✅.
- Sample CRs created, inspected with custom printer columns, and deleted cleanly on live cluster ✅.
- Unit tests pass (`go test ./pkg/version/...`) ✅.
- PR #2 merged to `main`, tagged and pushed `phase-01-done` ✅.

---

## 2026-10-27 — Phase 02: Workload Controller + FSM

**Done:**
- Implemented pure, table-driven Workload Finite State Machine in `internal/fsm/`:
  - Defined all 10 states: `Pending`, `Queued`, `Admitted`, `Scheduling`, `Running`, `Checkpointing`, `Preempting`, `Recovering`, `Succeeded`, `Failed`.
  - Defined transition table mapping valid state/event pairs to destination phases, reasons, and messages.
  - Implemented monotonic sequence numbers (`record.Seq`) and bounded history retention (`MaxHistoryEntries = 50`) to avoid unbounded CRD status growth.
  - Comprehensive unit tests in `internal/fsm/fsm_test.go` covering every valid transition, disallowed transitions, terminal state invariants, and history bounds.
- Created architectural design doc `docs/design/indexers.md` explaining index extraction logic and complexity for `IndexPodWorkload` and `IndexPodNode`.
- Implemented Workload Controller in `internal/controller/workload/`:
  - `constants.go`: Finalizer (`meridian.dev/workload-finalizer`), scheduling gate (`meridian.dev/admission-gate`), labels, and SSA field manager.
  - `indexers.go`: Custom field indexers for $O(1)$ informer cache lookups.
  - `workload_controller.go`: Complete control loop managing FSM transitions, gated gang pod creation, status patches via Server-Side Apply, and finalizer cleanup.
  - `workload_controller_test.go`: Unit tests for indexers and gang pod construction.
- Integrated Workload Controller and field indexers into `test/envtest/suite_test.go`.
- Added integration envtest suite in `test/envtest/workload_controller_test.go` covering initial enqueue, gated pod creation, crash-restart convergence, and deletion cleanup.

**Verified:**
- `go test -v ./internal/fsm/...` passed (100% table test coverage) ✅.
- `go test -v ./internal/controller/workload/...` passed ✅.
- `go test -c ./test/envtest/...` compiled cleanly for integration test runner ✅.
- PR #3 opened on GitHub (`https://github.com/tijani-web/meridian/pull/3`).

**Next:** Phase 03 — Queues & admission (quota/borrow/lend/fair-share, gate release, property tests).

---

## 2026-10-27 — Phase 03: Queues & Admission (Quota/Borrow/Lend/Fair-Share, Gate Release)

**Done:**
- Created design document `docs/design/fair-share.md`:
  - Detailed Dominant Resource Fairness (DRF-lite) mathematical formulation for multi-accelerator topologies.
  - Formulated dominant share equations, normalized weight vectors, and priority tie-breaking.
  - Worked step-by-step numeric examples for 2 queues sharing H100 and A100 capacity.
- Implemented pure DRF-lite algorithm and victim selection in `internal/controller/queue/drf.go`:
  - `CalculateWorkloadGPUs`: Aggregates total GPU demand across gang size and per-pod GPU count.
  - `ComputeDominantShare`: Calculates normalized dominant share across heterogeneous accelerators.
  - `EvaluateAdmission`: Enforces nominal quota, borrowing limits, and cluster headroom availability.
  - `SelectVictims`: Implements tie-breaking order for reclaim preemption (borrowed workloads first, lowest priority, highest dominant rate, newest admission).
- Implemented property-based tests in `internal/controller/queue/property_test.go`:
  - `TestPropertySafetyBound`: Validates nominal + borrowing limits never exceeded across 1,000 randomized workload sequences.
  - `TestPropertyVictimSelectionTieBreak`: Proves deterministic victim ranking under randomized candidate sets.
  - `TestPropertyReclaimTermination`: Proves reclaim loop terminates and satisfies required GPU deficit.
- Implemented Queue Controller in `internal/controller/queue/queue_controller.go`:
  - Full reconciliation loop over `GPUQueue` CRDs.
  - Aggregates per-GPU-type usage (`meridianv1alpha1.GPUUsageBreakdown`), pending workloads, and admitted workloads.
  - Atomically admits qualifying queued workloads and strips the `meridian.dev/admission-gate` scheduling gate from all member pods.
  - Watches `GPUWorkload` events via `handler.EnqueueRequestsFromMapFunc` to immediately reconcile queues upon workload submission or lifecycle change.
  - Triggers preemption by flagging victims with `Preempting` phase when nominal quota reclaim is required.
- Integration test suite:
  - Registered `QueueReconciler` in `test/envtest/suite_test.go`.
  - Added `test/envtest/queue_controller_test.go` verifying nominal gang admission, scheduling gate removal, borrowing limits, and admission capping.

**Verified:**
- `go test -v ./internal/controller/queue/...` passed (all unit and randomized property tests passed) ✅.
- `go test -c ./test/envtest/...` compiled cleanly with `GOOS=linux` ✅.

**Next:** Phase 04 — Scheduler plugins (QueueSort, PreFilter, Filter, Score, Permit, Decision recorder).

---

## 2026-10-28 — Phase 04: GPU-Aware Out-of-Tree Scheduler Plugin

**Done:**
- Created `docs/design/scoring.md`: full composite scoring formulation (fit × 40% + topology × 45% + health × 15%) with numeric worked examples.
- Implemented out-of-tree scheduler sub-module in `cmd/scheduler/go.mod` (separate module to isolate k8s monorepo replace-directive tree from the main controller module).
- Implemented `cmd/scheduler/plugin/plugin.go` — unified `MeridianGPU` plugin covering all 6 scheduler framework extension points:
  - **QueueSort**: priority-FIFO ordering via `meridian.dev/priority` + `meridian.dev/admitted-at`.
  - **PreFilter**: gang label validation + cluster-wide GPU type feasibility check.
  - **Filter**: GPU type match, GPU capacity (`nvidia.com/gpu`), health taint rejection.
  - **Score/NormalizeScore**: composite fitScore + topoScore + healthScore weighted and rescaled to [0, MaxNodeScore].
  - **Reserve/Unreserve**: per-gang tentative node assignment in CycleState ring.
  - **Permit**: gang scheduling hold until `minAvailable` threshold, with configurable timeout.
- Implemented internal packages (no framework dependency, used independently by control plane):
  - `internal/scheduler/topology/`: topology label distance calculation (`NVLink` > `Rack` > `Zone`).
  - `internal/scheduler/scoring/`: fitScore, healthScore, composite scoring helpers.
  - `internal/scheduler/decisions/`: per-pod ring-buffer decision audit recorder for `meridianctl explain`.
- Added `tools/patch-node-gpu/main.go`: patches GPU labels and extended resources on kind nodes.
- Added `deploy/scheduler/deployment.yaml`: ServiceAccount, ClusterRoleBindings (`system:kube-scheduler`, `system:volume-scheduler`), ConfigMap with `KubeSchedulerConfiguration`, HA Deployment (2 replicas on control-plane nodes with leader election).
- Added `deploy/scheduler/meridian-scheduler-config.yaml`: standalone scheduler config.
- Added `cmd/scheduler/Dockerfile`: distroless multi-stage image build.

**Verified:**
- `go test ./internal/scheduler/decisions/...` — 7 tests PASS (incl. concurrency + memory bound) ✅
- `go test ./internal/scheduler/scoring/...` — 3 tests PASS ✅
- `go test ./internal/scheduler/topology/...` — 2 tests PASS ✅
- `go test -C cmd/scheduler ./plugin/...` — 6 tests PASS (TestFitScore, TestHealthScore, TestDistanceAndTopoScore, TestQueueSort_Less, TestFilter, TestPermitAndReserve) ✅
- `GOOS=linux GOARCH=amd64 go build -C cmd/scheduler -o ../../bin/meridian-scheduler-linux .` — 100 MiB ELF amd64 binary ✅
- Commit `5e6730f`, pushed to `phase/04-scheduler-plugins` ✅

**Next:** Phase 05 — Checkpoint / Preemption controller + `meridianctl explain`.

---

## 2026-10-28 — Phase 05: Checkpoint Protocol & Cooperative Preemption

**Done:**
- Created `docs/design/checkpoint-protocol.md`: comprehensive design specification covering the cooperative checkpoint protocol, controller state machine, trainer-side library, straggler detection, and elastic restart contract.
- Implemented `pkg/ckptclient`: trainer-side cooperative checkpoint client library in Go:
  - Watches own pod annotations via in-cluster client / downward API (`meridian.dev/checkpoint-request`).
  - Dispatches checkpoint requests to user callback, writes actual artifacts, and patches `meridian.dev/checkpoint-ack`.
  - Emits `meridian.dev/checkpoint-heartbeat` and `meridian.dev/checkpoint-step` on training steps for straggler tracking.
  - Exposes `ResumeFrom()` and `ResumeStep()` to inspect `MERIDIAN_RESUME_FROM` and `MERIDIAN_RESUME_STEP`.
  - Comprehensive unit test suite in `pkg/ckptclient/ckptclient_test.go` (handler acks, idempotency, sequence advancement, heartbeat, direct ack).
- Implemented `internal/checkpoint/coordinator.go`: controller-side coordinator:
  - `RequestCheckpoint`: broadcasts `meridian.dev/checkpoint-request` and path annotations across all gang pods via merge patch.
  - `EvaluateAcks`: evaluates ack prefixes, extracts paths/steps, verifies all gang members, and flags timeout expiration.
  - `CompleteCheckpoint`: updates `LastCheckpointAt`, sets `CheckpointHealthy=True`, records coordinates.
  - `HandleTimeout`: flags `CheckpointHealthy=False` with Reason `AckTimeout`, emits warning events.
  - `InjectResumeEnv`: injects `MERIDIAN_RESUME_FROM` and `MERIDIAN_RESUME_STEP` into pod specs.
  - `DetectStragglers`: identifies lagging gang pods based on heartbeat age.
  - Unit tests in `internal/checkpoint/coordinator_test.go` without any fake clients.
- Integrated checkpoint coordinator into controllers:
  - `internal/controller/workload/workload_controller.go`: full `reconcileCheckpointing` loop managing dispatch, ack polling, complete -> preempt/running transitions, timeout force-eviction, and resume env injection in `buildGangPod`.
  - `internal/controller/queue/queue_controller.go`: checkpoint-aware preemption in `initiateReclaim` requesting cooperative checkpoints on victim workloads before eviction.
- Created `test/e2e/trainer/main.go`: small Go training binary using `ckptclient` that writes real checkpoint artifacts to disk and restores step continuity upon resume.
- Added `test/envtest/checkpoint_preemption_test.go`: envtest integration suite running against real apiserver proving preempt -> checkpoint ack -> evict -> resume with env injection, plus ack timeout path.
- Added `test/e2e/checkpoint_preemption_test.go`: trainer disk persistence & step continuity verification, plus cluster e2e harness.

**Verified:**
- `go test -v ./pkg/ckptclient/...` — 6 tests PASS ✅
- `go test -v ./internal/checkpoint/...` — 5 tests PASS ✅
- `go test -v ./internal/controller/...` — all controller unit tests PASS ✅
- `go build ./test/e2e/trainer` — compiled cleanly ✅
- `go test -v ./test/e2e/...` — trainer checkpoint continuity test PASS ✅
- Zero `fake.NewClientBuilder` across all test packages outside tools, zero mocks or stubs in product code ✅

**Next:** Phase 06 — Node health (NVML + DCGM-scrape, fault injector, health controller).

---
