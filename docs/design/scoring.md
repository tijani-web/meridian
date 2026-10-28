# Meridian Scheduler Scoring & Topology Placement Algorithm

This document defines the mathematical formulation, scoring metrics, normalization curves, and worked numeric examples for the Meridian GPU-aware Kubernetes Scheduler scoring plugins.

---

## 1. Objectives

1. **Anti-Fragmentation / Best-Fit**: Pack GPU workloads to minimize stranding. Avoid placing small allocations (e.g., 1–2 GPUs) on pristine, unallocated 8-GPU nodes when partially utilized nodes can accommodate them.
2. **Topology Affinity**: Maximize intra-gang communication bandwidth by packing distributed gang pods into the tightest possible physical failure and interconnect boundary:
   - Level 0: Same NVLink Domain (`meridian.dev/nvlink-domain`) — direct high-speed NVLink interconnect (up to 900 GB/s per GPU).
   - Level 1: Same Rack (`meridian.dev/rack`) — top-of-rack (ToR) switch boundary, minimal hop count, uniform bisectional bandwidth.
   - Level 2: Same Zone (`topology.kubernetes.io/zone`) — intra-datacenter latency boundary.
3. **Hardware Health Penalty**: Discourage placement on nodes exhibiting recent non-fatal GPU health anomalies (e.g., degraded PCI lanes, transient thermal throttling, single-bit ECC corrections).

---

## 2. Multi-Objective Scoring Formulation

The composite score $S(P, N) \in [0, 100]$ for placing Pod $P$ on Node $N$ is defined as the weighted linear combination:

$$S(P, N) = \min\left(100, \max\left(0, \frac{w_{\text{fit}} \cdot S_{\text{fit}}(P, N) + w_{\text{topo}} \cdot S_{\text{topo}}(P, N) + w_{\text{health}} \cdot S_{\text{health}}(N)}{w_{\text{fit}} + w_{\text{topo}} + w_{\text{health}}}\right)\right)$$

Where the default weights configured via `KubeSchedulerConfiguration` plugin arguments are:
- $w_{\text{fit}} = 40$ (Anti-Fragmentation weight)
- $w_{\text{topo}} = 45$ (Topology Affinity weight)
- $w_{\text{health}} = 15$ (Node Health Penalty weight)

---

## 3. Sub-Scoring Functions

### 3.1 Anti-Fragmentation / Best-Fit ($S_{\text{fit}}$)

Let:
- $C(N) \in \mathbb{N}$ be the total GPU capacity of node $N$ (e.g., 8 GPUs).
- $U(N) \in \mathbb{N}$ be the currently allocated GPUs on node $N$.
- $R(P) \in \mathbb{N}$ be the GPUs requested by pod $P$ ($1 \le R(P) \le C(N) - U(N)$).
- $L(P, N) = C(N) - (U(N) + R(P))$ be the leftover free GPUs on node $N$ after placing pod $P$.

The scoring function rewards placements where the leftover capacity $L(P, N)$ is either:
1. Exactly 0 (perfect fit, full node utilization): score = 100.
2. An integer multiple of common gang powers-of-two (e.g., leftover 2, 4): high score.
3. Leaves stranded fractions (e.g., leftover 1 or 3): lower score.
4. Leaves a large unallocated node partially broken (e.g., taking 1 GPU from an empty 8-GPU node leaving 7 unusable for 8-GPU jobs): heavy penalty.

The mathematical formulation for $S_{\text{fit}}(P, N)$ is:

$$S_{\text{fit}}(P, N) = 100 \cdot \left( \frac{U(N) + R(P)}{C(N)} \right) \cdot \Phi(L(P, N))$$

Where the alignment penalty factor $\Phi(L)$ is:
- $\Phi(0) = 1.0$ (Zero leftover: perfect saturation)
- $\Phi(L) = 0.9$ if $L \in \{4, 8\}$ (Leftover fits standard half/full-node jobs)
- $\Phi(L) = 0.75$ if $L = 2$ (Leftover fits 2-GPU jobs)
- $\Phi(L) = 0.50$ if $L \in \{1, 3, 5, 6, 7\}$ (Leftover strands capacity)

---

### 3.2 Topology Affinity Scoring ($S_{\text{topo}}$)

When gang pod $P$ belongs to a distributed gang $G$:
Let $A(G)$ be the set of nodes already tentatively reserved or bound to preceding members of gang $G$.

If $|A(G)| = 0$ (first pod of the gang being placed):
- $S_{\text{topo}}(P, N) = 50$ (Neutral baseline).

If $|A(G)| > 0$:
Let $N_{\text{ref}} \in A(G)$ be the primary anchor node assigned to the gang leader (Pod 0).
The topology distance score between candidate node $N$ and anchor node $N_{\text{ref}}$ is:

$$S_{\text{topo}}(P, N) = \begin{cases}
100 & \text{if } \text{NVLinkDomain}(N) = \text{NVLinkDomain}(N_{\text{ref}}) \quad \text{(NVLink Domain match)} \\
70 & \text{if } \text{Rack}(N) = \text{Rack}(N_{\text{ref}}) \land \text{NVLinkDomain}(N) \ne \text{NVLinkDomain}(N_{\text{ref}}) \quad \text{(Same Rack)} \\
30 & \text{if } \text{Zone}(N) = \text{Zone}(N_{\text{ref}}) \land \text{Rack}(N) \ne \text{Rack}(N_{\text{ref}}) \quad \text{(Same Zone)} \\
0 & \text{if } \text{Zone}(N) \ne \text{Zone}(N_{\text{ref}}) \quad \text{(Cross-Zone)}
\end{cases}$$

---

### 3.3 Hardware Health & Damping ($S_{\text{health}}$)

Nodes exhibiting recent degraded conditions (tracked via node annotation `meridian.dev/health-score` $\in [0, 100]$ updated by the Meridian node agent):

$$S_{\text{health}}(N) = \text{HealthScore}(N)$$

Where healthy nodes have $\text{HealthScore}(N) = 100$, nodes with recoverable XID events have $\text{HealthScore}(N) = 50$, and untainted nodes recovering from thermal throttling have $\text{HealthScore}(N) = 75$.

---

## 4. Worked Numeric Examples

### Scenario 1: Anti-Fragmentation (Single 1-GPU Pod Placement)

Consider pod $P_1$ requesting $R(P_1) = 1$ GPU on an `nvidia-h100` cluster.
Candidate Nodes:
- **Node A**: Total $C=8$, Used $U=7$. Leftover $L = 8 - (7 + 1) = 0$.
- **Node B**: Total $C=8$, Used $U=4$. Leftover $L = 8 - (4 + 1) = 3$.
- **Node C**: Total $C=8$, Used $U=0$. Leftover $L = 8 - (0 + 1) = 7$.

#### Fit Scores:
- **Node A**:
  $$S_{\text{fit}}(P_1, A) = 100 \cdot \left(\frac{7 + 1}{8}\right) \cdot \Phi(0) = 100 \cdot 1.0 \cdot 1.0 = 100$$
- **Node B**:
  $$S_{\text{fit}}(P_1, B) = 100 \cdot \left(\frac{4 + 1}{8}\right) \cdot \Phi(3) = 100 \cdot 0.625 \cdot 0.50 = 31.25 \to 31$$
- **Node C**:
  $$S_{\text{fit}}(P_1, C) = 100 \cdot \left(\frac{0 + 1}{8}\right) \cdot \Phi(7) = 100 \cdot 0.125 \cdot 0.50 = 6.25 \to 6$$

**Outcome**: Node A scores 100, packing the final GPU and preserving Node C completely intact for upcoming 8-GPU distributed training jobs.

---

### Scenario 2: Topology Affinity (Gang Member Placement)

A 2-pod gang $G$ (Pods $P_0, P_1$) requests 8 GPUs each ($R=8$).
Pod $P_0$ was reserved on `worker-1` (NVLink domain `nvlink-0`, Rack `rack-0`, Zone `us-east-1a`).
Candidate nodes for Pod $P_1$ (all with 8 idle GPUs and 100 health):
- **Node 1** (`worker-2`): Same NVLink domain `nvlink-0`, `rack-0`, `us-east-1a`.
- **Node 2** (`worker-3`): Different NVLink domain `nvlink-1`, same `rack-0`, `us-east-1a`.
- **Node 3** (`worker-4`): Different rack `rack-1`, same `us-east-1a`.

#### Composite Scores ($w_{\text{fit}}=40, w_{\text{topo}}=45, w_{\text{health}}=15$, Total Weight = 100):
Since all candidate nodes have $U=0, R=8, L=0$, their fit score is $S_{\text{fit}} = 100$.
Their health score is $S_{\text{health}} = 100$.

- **Node 1**:
  - $S_{\text{topo}} = 100$
  - Composite = $\frac{40 \cdot 100 + 45 \cdot 100 + 15 \cdot 100}{100} = \mathbf{100}$
- **Node 2**:
  - $S_{\text{topo}} = 70$
  - Composite = $\frac{40 \cdot 100 + 45 \cdot 70 + 15 \cdot 100}{100} = \frac{4000 + 3150 + 1500}{100} = \mathbf{86.5} \to 86$
- **Node 3**:
  - $S_{\text{topo}} = 30$
  - Composite = $\frac{40 \cdot 100 + 45 \cdot 30 + 15 \cdot 100}{100} = \frac{4000 + 1350 + 1500}{100} = \mathbf{68.5} \to 68$

**Outcome**: The scheduler deterministically places Pod $P_1$ on Node 1 within the same NVLink domain.
