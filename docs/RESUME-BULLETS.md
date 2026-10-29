# docs/RESUME-BULLETS.md — Portfolio Claims (Measured Only)

> **Rule (Meridian.md §14)**: Every number is traceable to a committed result file.
> Path included next to each bullet. If a number was not measured, the bullet does not ship.

---

## Go / Kubernetes Engineering

- **Built Meridian**, a Kubernetes-native GPU fleet control plane in Go (13 phases, ~25k lines of product code):
  gang scheduler plugin (Permit, Reserve, Score, Filter), checkpoint-aware preemption, fair-share queuing
  with borrowing/lending, inference router, and HA operator with leader election.
  - Simulated-fleet benchmarks (500 nodes / 4,000 GPUs): Meridian reduced mean queue wait by **24%**
    (33.3s → 25.3s, median) and eliminated wasted GPU-seconds (**180s → 0s, 100% reduction**) vs the
    default kube-scheduler. Partially-started gangs: **0 vs 1** (default scheduler).
    Source: `bench/results/2026-10-28-scale-benchmarks/summary.json`
  - At 2,000 nodes / 16,000 GPUs: mean queue wait **-22%** (138.5s → 107.6s), p95 queue wait **-25%**
    (382.2s → 286.7s). Partially-started gangs: **0 vs 1**.
    Source: `bench/results/2026-10-28-scale-benchmarks/summary.json`

- **Implemented Kubernetes operators** using controller-runtime with informers, indexers, server-side apply,
  finalizers, and leader-elected HA (2-replica manager, Lease-based). Verified failover with chaos tests:
  standby acquires lease within **30 seconds** of leader pod termination.
  Source: `test/chaos/leader_failover_test.go`

- **Designed and resolved a TOCTOU concurrency bug** in the Permit plugin's gang-abort path (snapshot-at-creation-time
  vs snapshot-at-fire-time). Verified with a property test (100 goroutines, 10,000 iterations) and a real-cluster
  chaos test. `meridian_gang_partial_starts_total` was **0 in all test runs** after the fix.
  Source: `test/chaos/gang_atomicity_test.go`, `docs/postmortems/2026-10-28-gang-permit-partial-start-near-miss.md`

---

## Inference / ML Systems

- **Built an inference router** with four pluggable strategies (RoundRobin, LeastQueue, KVAware, PrefixAffinity)
  and HTTP 429 load shedding. Benchmarked against a simulated vLLM replica fleet:
  - KVAware vs RoundRobin: TTFT p99 **85.7ms vs 92.6ms** (-7.5%)
  - LeastQueue achieves lowest p99 tail: **74.1ms**
  - PrefixAffinity achieves **50% prefix cache hit rate** (bounded-load consistent hashing)
  Source: `bench/results/2026-10-29-inference-router/summary.json`

- **Implemented checkpoint-aware preemption**: controller sends checkpoint request annotation to trainer,
  waits for ACK, then evicts gang atomically. Fault tolerance: XID 79 fault injection on 50 nodes, median
  detection **1.24s**, median reschedule **3.46s**, steps lost vs last checkpoint **11**, jobs resumed **100%**.
  Source: `bench/results/2026-10-28-scale-benchmarks/summary.json` (`fault_tolerance` section)

---

## Tooling & Observability

- **Wrote `meridianctl explain`**, a CLI tool that reports per-node scheduling rejection reasons for stuck
  workloads from the decision recorder. Reasons: InsufficientGPUs, TopologyUnmet, QuotaBlocked, PermitWaiting,
  Preempted. Verified against real stuck-workload scenarios on kind.

- **Built a complete Prometheus observability stack**: 15+ metrics across the scheduler, queue controller,
  inference router, and node agent; 4 SLOs with multi-window burn-rate PrometheusRules; 3 production-grade
  Grafana dashboards (GPU Fleet Overview, Workload & Scheduling, Inference Router).

---

## Infrastructure

- **Packaged with Helm** (charts/meridian/): manager HA, scheduler, agent DaemonSet, router, cert-manager
  webhook TLS, ServiceMonitors, PrometheusRules, CRDs, PodDisruptionBudgets.
- **ArgoCD GitOps integration**: AppProject + Application with `ignoreDifferences` for controller-managed
  runtime annotations to prevent spurious OutOfSync diffs.
- **Terraform GPU demo** (terraform/gpu-demo/): AWS Deep Learning AMI + k3s bootstrap + NVIDIA Container
  Toolkit provisioning for on-demand GPU evaluation.

---

## Notes

- All benchmark numbers are from KWOK-simulated fleets (not real GPU hardware). See `docs/LIMITATIONS.md`.
- Router benchmarks used the vllm-emu HTTP emulator, not real GPU inference.
- Chaos tests run on a real kind cluster with real Kubernetes control-plane behavior.
