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
