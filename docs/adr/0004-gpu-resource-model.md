# docs/adr/0004-gpu-resource-model.md
# ADR 0004: GPU Resource Model — Extended Resources vs DRA

**Status**: Accepted  
**Date**: 2026-10-27  
**Deciders**: Basit Tijani

---

## Context

Meridian must represent GPU resources so the scheduler can filter and score nodes
based on GPU count, type, and topology. Two mechanisms exist in Kubernetes:

1. **Extended Resources** (`nvidia.com/gpu`): Device plugins advertise a count of
   GPUs as an opaque integer resource. Pods request them via `resources.limits`.
2. **Dynamic Resource Allocation (DRA)** (KEP-3063, GA in 1.32): Structured resource
   claims with vendor-defined parameters, ResourceClass, ResourceSlice, etc.

## Options Considered

### Option A: Extended Resources (chosen for v0.1.0)
- The NVIDIA device plugin exposes `nvidia.com/gpu: N` on each node.
- Meridian reads `status.allocatable["nvidia.com/gpu"]` and node labels
  (`meridian.dev/gpu-type`, `meridian.dev/nvlink-domain`, etc.) for topology.
- Pods request GPUs via `resources.limits["nvidia.com/gpu"]`.
- Pros: Universal — works with every existing NVIDIA GPU cluster. No DRA operator
  required. Stable, well-understood in production.
- Cons: Opaque count only. Cannot distinguish MIG slices, GPU UUIDs, NVSwitch
  topology beyond what labels express. Scheduler cannot see individual GPU UUIDs
  at scheduling time.

### Option B: DRA
- Pros: Structured claims; vendor-provided topology in ResourceSlice; future-proof.
  Can express per-GPU properties (UUID, VRAM, compute capability).
- Cons: DRA is GA only in Kubernetes 1.32+ (late 2025/2026). Tooling (NVIDIA DRA
  driver) is still maturing. Adds operator complexity. Cannot assume DRA in a
  standard cluster today.

## Decision

Use extended resources (`nvidia.com/gpu`) plus node labels for topology for v0.1.0.
DRA support is a stretch goal (§15), implemented after Phase 13 DoD is fully green.

Node label contract (documented for scheduler plugins):
- `meridian.dev/gpu-type`: GPU model string (e.g., `nvidia-h100`, `nvidia-a100`)
- `meridian.dev/nvlink-domain`: NVSwitch fabric domain identifier
- `meridian.dev/rack`: Physical rack identifier
- `topology.kubernetes.io/zone`: Availability zone (standard label)

Migration path to DRA: the `Filter` and `Score` plugins are abstracted behind a
`TopologyReader` interface. The DRA implementation of that interface can be added
without changing the plugin dispatch path.

## Consequences

- **Positive**: Works on any NVIDIA GPU cluster with the NVIDIA device plugin.
  No additional operator required.
- **Negative**: Cannot express per-GPU UUID assignment. The `assignment` field in
  `GPUWorkload.status` records which node and how many GPUs, not which specific GPUs.
  Documented in `docs/LIMITATIONS.md`.
- **Future**: DRA implementation will add a `DRATopologyReader` that reads from
  `ResourceSlice` objects for richer placement decisions.

## Reference

- KEP-3063 (DRA): https://github.com/kubernetes/enhancements/issues/3063
- NVIDIA device plugin: https://github.com/NVIDIA/k8s-device-plugin
- NVIDIA DRA driver: https://github.com/NVIDIA/k8s-dra-driver
