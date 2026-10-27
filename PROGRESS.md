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
- Topology labels verified on kind workers (`domain-a`/`domain-b`, `rack-0`/`rack-1`, `us-east-1a`/`us-east-1b`, `nvidia-h100`/`nvidia-a100`) ✅:
  ```
  NAME                     STATUS   NVLINK     RACK     ZONE         GPU
  meridian-control-plane   Ready    <none>     <none>   <none>       <none>
  meridian-worker          Ready    domain-a   rack-0   us-east-1a   nvidia-h100
  meridian-worker2         Ready    domain-a   rack-0   us-east-1a   nvidia-h100
  meridian-worker3         Ready    domain-b   rack-1   us-east-1b   nvidia-h100
  meridian-worker4         Ready    domain-b   rack-1   us-east-1b   nvidia-a100
  ```
- `helm lint charts/meridian` — 1 chart(s) linted, 0 chart(s) failed ✅.
- `pkg/version` package created and tested (`go test ./pkg/version/...` passed) ✅.
- Bug #1 diagnosed, fixed in `deploy/kind/config.yaml`, and logged in `docs/debugging-log.md` (Issue #1 closed) ✅.

---

## 2026-10-27 — Phase 01: APIs, CRDs, Webhooks, Envtest Harness

**Done:**
- Defined core CRD types in `api/v1alpha1`:
  - `GPUQueue` (`gpuqueue_types.go`): multi-tenant GPU quotas, borrowing/lending limits, priority, preemption policy, admitted/borrowed status tracking.
  - `GPUWorkload` (`gpuworkload_types.go`): gang size and minAvailable, GPUs per pod, GPU accelerator type, topology affinity preferences, checkpoint configuration, restart/elastic specs, phase conditions, and scheduling gate (`meridian.dev/admission-gate`).
  - `InferenceDeployment` (`inferencedeployment_types.go`): model URI and revision, engine container runtime configuration, canary rollout progression steps with pause intervals, least-queue routing strategy, warmup configuration, and load shedding limits.
  - `zz_generated.deepcopy.go`: deep copy methods generated for all types.
- Implemented mutating defaulting and validating admission webhooks:
  - `gpuqueue_webhook.go`: defaults preemption reclaim and 60s grace period; validates nominal quota types, borrowing limits, and lending bounds <= nominal quota.
  - `gpuworkload_webhook.go`: defaults gang `MinAvailable=Size`, `Kind=Training`, `TopologyPreference=None`; validates queue name immutability, gang bounds, topology consistency, and checkpoint path requirements.
  - `inferencedeployment_webhook.go`: defaults replicas=1, model revision="main", port=8000; validates model name/URI, URI immutability, port bounds, monotonic canary rollout step weights, and load shedding max queue depth.
- Created admission webhook configurations in `config/webhook/manifests.yaml`.
- Created sample resource manifests in `config/samples/` for all three CRDs.
- Created RBAC ClusterRole in `config/rbac/role.yaml`.
- Built integration test suite under `test/envtest/` covering defaulting, validation on create, immutability on update, and deletion guards for all three CRD webhooks.
- Added `install` and `uninstall` targets with server-side apply in `Makefile`.
- Updated `docs/LIMITATIONS.md` documenting server-side apply requirement due to recursive `corev1.PodTemplateSpec` OpenAPI schema size.

**Verified:**
- `go build ./api/v1alpha1/...` compiles cleanly with zero errors ✅.
- `go test -c ./test/envtest/...` compiles cleanly for target integration test runner ✅.
- All three CRDs installed and verified on live 5-node kind cluster via server-side apply:
  - `gpuqueues.meridian.dev` (Cluster, v1alpha1) ✅.
  - `gpuworkloads.meridian.dev` (Namespaced, v1alpha1) ✅.
  - `inferencedeployments.meridian.dev` (Namespaced, v1alpha1) ✅.
- Sample CRs created, inspected with custom printer columns (`kubectl get gpuqueue,gpuworkload,inferencedeployment`), and deleted cleanly on live cluster ✅.
- Unit tests pass (`go test ./pkg/version/...`) ✅.

**Next:** Phase 02 — Workload controller + FSM (finite state machine, scheduling gates, pod lifecycle, crash-restart convergence).

---
