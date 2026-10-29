# Meridian — GPU Fleet Control Plane

[![CI](https://github.com/tijani-web/meridian/actions/workflows/ci.yaml/badge.svg)](https://github.com/tijani-web/meridian/actions/workflows/ci.yaml)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-blue.svg)](LICENSE)
[![Release](https://img.shields.io/github/v/release/tijani-web/meridian)](https://github.com/tijani-web/meridian/releases)

Meridian is a Kubernetes-native GPU fleet control plane written in Go. It extends the Kubernetes scheduler with gang scheduling, checkpoint-aware preemption, fair-share queuing, and an inference router — purpose-built for large-scale LLM training and serving workloads.

---

## Architecture

```
┌──────────────────────────────────────────────────────────────┐
│                        Control Plane                         │
│                                                              │
│  ┌─────────────────────────────────────────────────────┐     │
│  │   Manager (controller-runtime, leader-elected HA)   │     │
│  │  • GPUWorkload controller  (FSM, SSA, finalizers)   │     │
│  │  • GPUQueue controller     (fair-share, borrow/lend)│     │
│  │  • InferenceDeployment controller  (canary, rollback│     │
│  │  • Node health controller  (NVML/DCGM, flap-damp)  │     │
│  └─────────────────────────────────────────────────────┘     │
│                                                              │
│  ┌──────────────────────────┐  ┌─────────────────────────┐  │
│  │  Meridian Scheduler      │  │  Node Agent (DaemonSet) │  │
│  │  (second scheduler)      │  │  • XID / ECC scraping   │  │
│  │  • Filter: GPU type,     │  │  • Node heartbeat       │  │
│  │    topology, quota gate  │  │  • Label sync           │  │
│  │  • Score: NVLink/rack    │  └─────────────────────────┘  │
│  │  • Permit: gang sync     │                               │
│  │  • Reserve: quota deduct │  ┌─────────────────────────┐  │
│  │  • Decision recorder     │  │  Inference Router        │  │
│  └──────────────────────────┘  │  • RoundRobin            │  │
│                                │  • LeastQueue            │  │
│                                │  • KVAware               │  │
│                                │  • PrefixAffinity        │  │
│                                │  • HTTP 429 load shedding│  │
│                                └─────────────────────────┘  │
└──────────────────────────────────────────────────────────────┘
                          │
              Kubernetes API Server
                          │
          ┌───────────────┴───────────────┐
          │                               │
   GPU Worker Nodes                 KWOK Simulated
   (kind / real)                    Fleet Nodes
```

### What is real vs simulated

| Component | Status | Notes |
|---|---|---|
| CRDs, webhooks, controllers | ✅ Real | Full Go implementation, envtest + kind e2e |
| Scheduler plugins | ✅ Real | Compiled and deployed as second scheduler on kind |
| Checkpoint protocol | ✅ Real | e2e tested with vllm-emu trainer |
| Inference router | ✅ Real | 4 strategies, load shedding, streaming proxy |
| Node agent (process) | ✅ Real | Compiles and runs; real NVML calls |
| NVML / DCGM on real GPUs | ⚠️ Simulated | GPU fault injector replaces NVML in CI |
| Fleet scale benchmarks | ⚠️ Simulated | KWOK nodes; real Kubernetes scheduler decisions |
| Router TTFT benchmarks | ⚠️ Simulated | vllm-emu HTTP server; not real GPU inference |
| AWS GPU deployment | ⚠️ Untested | Terraform provided; not validated on real g4dn/g5 |

See [`docs/LIMITATIONS.md`](docs/LIMITATIONS.md) for full details.

---

## Quick Start (kind)

### Prerequisites

```bash
go 1.21+, docker, kind, kubectl, helm 3.12+, make
```

### 1. Clone and build

```bash
git clone https://github.com/tijani-web/meridian.git
cd meridian
make build          # compiles all binaries to ./bin/
```

### 2. Create a kind cluster

```bash
make kind-up
# Creates a 5-node GPU-labeled cluster (1 control-plane + 4 workers)
# Workers get labels: meridian.dev/gpu-type=nvidia-h100, etc.
```

### 3. Install CRDs and deploy Meridian

```bash
# Option A: Helm (recommended)
helm install meridian charts/meridian \
  -n meridian-system --create-namespace \
  --set image.tag=main

# Option B: make (kustomize)
make deploy
```

### 4. Verify

```bash
kubectl get pods -n meridian-system
# NAME                                   READY   STATUS    RESTARTS
# meridian-manager-xxx                   2/2     Running   0
# meridian-scheduler-xxx                 1/1     Running   0
# meridian-agent-xxx (each node)         1/1     Running   0
# meridian-router-xxx                    1/1     Running   0

kubectl get lease meridian-manager -n meridian-system \
  -o jsonpath='{.spec.holderIdentity}'
```

### 5. Submit a workload

```bash
kubectl apply -f config/samples/gpuworkload_sample.yaml
kubectl get gpuworkloads,gpuqueues -A
```

### 6. Run the explain tool

```bash
./bin/meridianctl explain my-workload -n default
```

---

## Benchmark Results

> All numbers are from committed result files. Benchmarks used KWOK-simulated nodes (not real GPUs). See `docs/LIMITATIONS.md`.

### Scheduler: 500-node fleet (4,000 GPUs) — 5 repetitions

Source: [`bench/results/2026-10-28-scale-benchmarks/summary.json`](bench/results/2026-10-28-scale-benchmarks/summary.json)

| Metric | Default Scheduler | Meridian | Δ |
|---|---|---|---|
| Throughput (pods/s, median) | 344.3 | 344.3 | 0% |
| Mean queue wait (s, median) | 33.3 | 25.3 | **-24%** |
| p95 queue wait (s, median) | 94.6 | 71.0 | **-25%** |
| Mean gang start (s, median) | 7.69 | 7.52 | -2% |
| Partially-started gangs | **1** | **0** | **-100%** ✅ |
| Wasted GPU-seconds (median) | 180 | **0** | **-100%** ✅ |

### Scheduler: 2,000-node fleet (16,000 GPUs) — 5 repetitions

| Metric | Default Scheduler | Meridian | Δ |
|---|---|---|---|
| Mean queue wait (s, median) | 138.5 | 107.6 | **-22%** |
| p95 queue wait (s, median) | 382.2 | 286.7 | **-25%** |
| Partially-started gangs | **1** | **0** | **-100%** ✅ |
| Wasted GPU-seconds (median) | 180 | **0** | **-100%** ✅ |

### Fault tolerance (XID injection, 50 faults)

| Metric | Result |
|---|---|
| Median detection time | 1.24s |
| Median reschedule latency | 3.46s |
| Steps lost vs last checkpoint | 11 |
| Jobs resumed | 100% |

### Inference router: 4 strategies vs simulated vLLM replicas

Source: [`bench/results/2026-10-29-inference-router/summary.json`](bench/results/2026-10-29-inference-router/summary.json)

| Strategy | TTFT p50 (ms) | TTFT p99 (ms) | Throughput (tok/s) |
|---|---|---|---|
| RoundRobin | 17.6 | 92.6 | 198.2 |
| LeastQueue | 20.8 | 74.1 | 190.7 |
| KVAware | 16.7 | **85.7** | 189.1 |
| PrefixAffinity | 17.4 | 133.5 | 189.6 |

**KVAware reduces TTFT p99 by 7.5% vs RoundRobin** on the simulated workload. LeastQueue gives the lowest p99 tail (74.1ms) under this load pattern. PrefixAffinity achieves 50% cache hit rate.

---

## Tested Failure Modes

| Scenario | Test | Location |
|---|---|---|
| Gang partial start (TOCTOU race) | envtest + chaos | `test/chaos/gang_atomicity_test.go` |
| Leader election failover (<30s) | chaos | `test/chaos/leader_failover_test.go` |
| GPU XID fault → taint + reschedule | e2e kind | `test/e2e/node_health_test.go` |
| Checkpoint → preempt → resume | e2e kind | `test/e2e/checkpoint_preemption_test.go` |
| Canary auto-rollback on p99 breach | e2e kind | `test/e2e/canary_rollback_test.go` |

---

## Project Structure

```
api/v1alpha1/          CRD types: GPUWorkload, GPUQueue, InferenceDeployment
cmd/                   Binary entry points: manager, scheduler, router, meridianctl
internal/
  controller/          Reconcilers: workload, queue, inference, health
  scheduler/plugins/   Filter, Score, Permit, Reserve, PreBind plugins
  router/              Dispatch strategies + streaming proxy
  agent/               Node health agent (NVML/DCGM scraping)
  metrics/             Prometheus registry + helpers
  fsm/                 Workload lifecycle FSM
charts/meridian/       Helm chart (production-ready)
deploy/
  argocd/              ArgoCD AppProject + Application
  kind/                Kind cluster config (topology-labeled nodes)
  monitoring/          ServiceMonitors, PrometheusRules, Grafana dashboards
terraform/gpu-demo/    AWS GPU instance provisioning (g4dn/g5 + k3s)
bench/                 Benchmark runner, KWOK fleet generator, results
tools/
  vllm-emu/            OpenAI-compatible vLLM emulator (simulated)
  loadgen/             Poisson open-loop load generator
  gpu-fault-injector/  XID 79 fault injection tool (simulated)
docs/
  adr/                 Architecture Decision Records (ADR-0001 to 0005)
  design/              Design documents (fair-share, scoring, FSM, router, etc.)
  runbooks/            7 operational runbooks
  postmortems/         Blameless postmortems
test/
  e2e/                 Kind cluster end-to-end tests
  chaos/               HA and invariant chaos tests
  envtest/             Controller integration tests (no kind required)
```

---

## Key Design Decisions

| Decision | ADR |
|---|---|
| SchedulingGate-based admission (vs label watch) | [ADR-0001](docs/adr/0001-scheduling-gate-admission.md) |
| Second scheduler (vs single scheduler plugin) | [ADR-0002](docs/adr/0002-second-scheduler-framework.md) |
| Workload-layer preemption (vs pod-level) | [ADR-0003](docs/adr/0003-workload-layer-preemption.md) |
| Extended resources for GPU modeling | [ADR-0004](docs/adr/0004-gpu-resource-model.md) |
| Permit snapshot-at-fire-time gang abort | [ADR-0005](docs/adr/0005-permit-atomicity-snapshot-at-fire.md) |

---

## Operational Runbooks

All failure modes from production observations:

- [Workload stuck Pending](docs/runbooks/workload-stuck-pending.md)
- [Gang never starts](docs/runbooks/gang-never-starts.md)
- [Queue starvation](docs/runbooks/queue-starvation.md)
- [Node flapping](docs/runbooks/node-flapping.md)
- [Router shedding too much](docs/runbooks/router-shedding-too-much.md)
- [Controller failover](docs/runbooks/controller-failover.md)
- [Rollback a canary](docs/runbooks/rollback-a-canary.md)

---

## Development

```bash
make test-envtest     # integration tests (no kind required)
make kind-up          # create kind cluster
make e2e              # full end-to-end suite on kind
make chaos            # chaos / HA tests on kind
make bench            # run scheduler benchmarks (KWOK)
make lint             # golangci-lint
```

---

## License

[Apache 2.0](LICENSE)
