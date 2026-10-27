# docs/LIMITATIONS.md — Known Limitations

Mandatory and honest. Updated throughout the project lifecycle.
Last updated: 2026-10-27 (Phase 00).

---

## Simulated Components

| Component | What Is Simulated | What Is Real |
|---|---|---|
| GPU Nodes | KWOK simulates node objects with `nvidia.com/gpu` capacity labels; no physical GPUs | Real Kubernetes API objects; all scheduling logic is real |
| vLLM replicas | `tools/vllm-emu` implements the OpenAI-compatible HTTP API and `/metrics` in the format of vLLM v0.9.2 | Real HTTP server; real metric names; real SSE streaming |
| DCGM metrics | `tools/gpu-fault-injector` emits real DCGM-exporter Prometheus text format | Real parsing and decision path in the agent |
| Load | `tools/loadgen` generates real HTTP traffic against the router | Real request flow end-to-end |

## Not Tested on Real GPUs (Phase 00 — will be updated)

- NVML library (`go-nvml`) calls require a physical NVIDIA GPU. The `nvml` health source is implemented against the real NVML interface; in CI and local tests, the DCGM-exporter scraper path is exercised instead via the fault injector.
- Real vLLM inference performance numbers are not available without a GPU. All router benchmarks use `vllm-emu`. If a real GPU session is conducted, results will be in `bench/results/` labeled "real GPU".
- KV-cache hit rates from `PrefixAffinity` routing depend on actual vLLM KV-cache behavior; `vllm-emu` simulates the metric endpoint format but does not replicate actual cache eviction behavior.

## Known Gaps (to be addressed in later phases)

- Dynamic Resource Allocation (DRA) is a stretch goal (§15); the current implementation uses `nvidia.com/gpu` extended resources only.
- Multi-cluster federation is design-doc only (§15).
- No real GPU node autoscaling (Karpenter integration is stretch goal).
- Real Terraform GPU demo (`terraform/gpu-demo`) requires a cloud account and incurs cost; it is optional and documented in runbooks.
- `GPUWorkload` CRD Schema Size: Because `GPUWorkloadSpec` embeds the full Kubernetes `corev1.PodTemplateSpec`, the generated CRD OpenAPI schema exceeds 256 KB. Applying it via standard client-side `kubectl apply` exceeds the annotation size limit (`metadata.annotations: Too long: may not be more than 262144 bytes`). Operators and manifests must use `kubectl apply --server-side` for `gpuworkloads.meridian.dev`.
- Envtest Platform Targets: Upstream `controller-runtime` v0.25 contains an unresolved build issue in `pkg/internal/testing/process/signal_windows.go` on native Windows; additionally upstream Kubernetes provides envtest binaries (`kube-apiserver`, `etcd`) for Linux and Darwin. Envtest execution is targeted to Linux environments (CI, WSL, containers).

## Deliberate Non-Goals

- Not a general-purpose batch scheduler (Volcano, Yunikorn). Meridian is focused on GPU workloads.
- Not a device plugin replacement. It uses the NVIDIA device plugin extended resource model.
- Not a multi-tenant hard isolation system. Queue quotas are advisory; Meridian trusts its own controllers.

---

> This file grows throughout the project. Every simulated result and every tested-only-with-fixtures item must appear here.
