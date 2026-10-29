# STATE.md — Meridian Resume State

Machine-readable resume state. Overwritten on every update.

---

## Current Phase
`phase/11-observability`

## Current Branch
`phase/11-observability`

## Last Green Commit
7125471 (feat(router): Phase 10 — Inference Router, vLLM Emulator, Loadgen, Benchmarks)

## Status
`PHASE_11_IN_PROGRESS`

## Open Issues
(none)

## Blockers
(none — autonomy mode active)

## Next 3 Actions
1. Create `docs/design/observability.md` — metrics catalog, SLO definitions, and Grafana dashboard layout.
2. Implement Prometheus custom metrics in `internal/metrics/` (workload admission latency, router TTFT histogram, KV cache utilization, gang scheduling latency, preemption counts).
3. Create PrometheusRule CRs for SLO burn-rate alerts and Grafana dashboard ConfigMaps, commit and verify.

## Phase 08 DoD Checklist
- [x] `bench/METHODOLOGY.md` updated with host environment, Go version, KWOK simulated node topologies, and statistical parameters
- [x] `pkg/tracegen/` synthetic generator with Poisson arrivals, 1/2/4/8 GPUs per replica, gang size 1-16 pods (total 1-64 GPUs), 3 queues, and 3 priority tiers
- [x] `tools/trace-gen/` standalone CLI supporting `--jobs`, `--seed`, `--format`, and `--replay`
- [x] `pkg/tracegen/generator_test.go` — all unit tests PASS
- [x] `deploy/kwok/` simulated GPU fleet configuration (`kwok-deployment.yaml`, `generate-fleet.sh`, `generate-fleet.ps1`) for 500 to 2,000 nodes
- [x] `bench/runner/` benchmark simulator with topology packing, gang reservation, and fault tolerance
- [x] `bench/runner/simulator_test.go` — all unit tests PASS (proving Meridian's 0 partially-started gang invariant)
- [x] `bench/cmd/run-benchmarks/main.go` CLI runner + `bench/scripts/run-benchmarks.sh` / `.ps1`
- [x] Committed raw CSV/JSON benchmark results under `bench/results/2026-10-28-scale-benchmarks/`:
  - `params.json`
  - `raw/scheduler_500_nodes.csv`
  - `raw/scheduler_2000_nodes.csv`
  - `raw/fault_tolerance.csv`
  - `summary.json`
- [x] Committed SVG plots under `bench/plots/`:
  - `scheduling_throughput_500_vs_2000.svg`
  - `fragmentation_comparison.svg`
  - `gang_start_latency.svg`
  - `fault_recovery_timeline.svg`
- [x] Zero `fake.NewClientBuilder` across all test packages outside tools, zero mocks or stubs in product code
- [x] All unit and integration tests green

## Phase 09 DoD Checklist
- [x] Reconciles `InferenceDeployment` into per-revision Deployments + Services with `meridian-scheduler`
- [x] Warm-up gating implemented: replicas gated until warm, 0% traffic weight until warm-up succeeds
- [x] Stepwise canary rollout with automated hold/promote based on error rate & TTFT p99 metric analysis
- [x] Automated rollback to 100% stable / 0% canary on SLA breach tested and verified
- [x] Published routing table ConfigMap (`<name>-routing-table`) containing JSON endpoints, weights, and strategy
- [x] `internal/controller/inference/inference_controller_test.go` — all unit tests PASS
- [x] `test/e2e/canary_rollback_test.go` — E2E canary promote and rollback tests PASS
- [x] Integration registered in `test/envtest/suite_test.go` and `test/envtest/inference_controller_test.go`
- [x] Zero `fake.NewClientBuilder` across all test packages outside tools, zero mocks or stubs in product code
- [x] `go vet` clean (exit code 0)

## Phase 10 DoD Checklist
- [x] `docs/design/router-strategies.md`: architectural design document for inference data plane, 4 strategies, metrics scraping, load shedding
- [x] `tools/vllm-emu`: standalone OpenAI-compatible streaming emulator exposing pinned v0.9.2 vLLM Prometheus metrics
- [x] `internal/router/`: core reverse proxy, SSE streaming passthrough, 4 routing strategies (`RoundRobin`, `LeastQueue`, `KVAware`, `PrefixAffinity`)
- [x] Load shedding with 429 and `Retry-After` header with priority classification
- [x] `cmd/router/`: standalone router binary
- [x] Streaming correctness tests: SSE token delivery, client cancellation propagation, replica failure handling
- [x] `tools/loadgen/`: synthetic Poisson load generator supporting shared prefix distributions
- [x] §8.3 Four-strategy benchmarks executed with committed raw CSV/JSON results and SVG comparison plots under `bench/results/` and `bench/plots/`
- [x] Zero mocks or stubs in product code, clean `go vet` and tests green

## Completed Phases
- Phase 00 — Bootstrap (completed 2026-10-27)
- Phase 01 — APIs, CRDs, Webhooks, Envtest Harness (completed 2026-10-27)
- Phase 02 — Workload Controller + FSM (completed 2026-10-27)
- Phase 03 — Queues & Admission (completed 2026-10-27)
- Phase 04 — GPU-Aware Scheduler Plugin (completed 2026-10-28)
- Phase 05 — Checkpoint Protocol & Preemption (completed 2026-10-28)
- Phase 06 — Node Health, Telemetry, and Fault Controller (completed 2026-10-28)
- Phase 07 — Explainability & Tracing (completed 2026-10-28)
- Phase 08 — Scale & Scheduler Benchmarks (completed 2026-10-28)
- Phase 09 — Inference Control Plane (completed 2026-10-29)
- Phase 10 — Inference Router + Emulator + Loadgen + Benchmarks (completed 2026-10-29)

## Remaining Phases
- Phase 11 — Observability: Metrics, Grafana Dashboards, SLOs, PrometheusRules (§6.9)
- Phase 12 — Packaging, HA, Chaos: Helm, ArgoCD, Terraform, Leader Failover, Postmortem (§9)
- Phase 13 — Documentation, Resume Bullets, and v0.1.0 Release (§11, §14)

## Tags Pushed
- phase-00-done
- phase-01-done
- phase-02-done
- phase-03-done
- phase-04-done
- phase-05-done
- phase-06-done
- phase-07-done
- phase-08-done
- phase-09-done
- phase-10-done
