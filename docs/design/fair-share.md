# Design: Fair-Share Admission & Quota Management (DRF-Lite)

**Author**: Meridian Team  
**Status**: Accepted  
**Requirement**: Meridian.md §6.2 — "Computes admission per queue: nominal quota, borrowing from idle capacity, lending, and fair-share across queues (DRF-lite over GPU counts; document the exact algorithm in docs/design/fair-share.md with worked examples)."

---

## 1. Overview & Objectives

In modern AI platforms, GPU clusters host multiple teams and organizational units competing for heterogeneous accelerator resources (e.g. NVIDIA H100 SXM5, NVIDIA A100 80GB, L40S). Static partitioning guarantees isolation but wastes expensive capacity when teams are idle. 

Meridian implements **Dynamic Quota Sharing with DRF-Lite (Dominant Resource Fairness)**:
1. **Guaranteed Nominal Quota**: Every tenant queue $Q_i$ has a contractual baseline of GPUs by accelerator type.
2. **Elastic Borrowing**: When queues are idle, other queues can borrow unused GPUs up to an explicit `BorrowingLimit`.
3. **Lending Limits**: Queues can restrict the maximum proportion of their nominal quota that can be lent out via `LendingLimit`.
4. **Preemption & Reclaim**: When a lending queue submits a workload requiring its nominal quota back, borrower workloads are preempted in a deterministic, checkpoint-aware order.
5. **DRF-Lite Fair-Share**: When total demand exceeds available capacity, admission priority is governed by the normalized dominant share across GPU types.

---

## 2. Mathematical Model & Definitions

Let:
- $\mathcal{A} = \{a_1, a_2, \dots, a_M\}$ be the set of accelerator types (e.g., `"nvidia-h100"`, `"nvidia-a100"`).
- $C(a)$ be the total cluster-wide capacity of accelerator $a \in \mathcal{A}$.
- $\mathcal{Q} = \{Q_1, Q_2, \dots, Q_K\}$ be the set of active GPUQueues.
- $N_i(a)$ be the nominal quota of queue $Q_i$ for accelerator $a$.
- $B_i(a)$ be the borrowing limit of queue $Q_i$ for accelerator $a$.
- $L_i(a)$ be the lending limit of queue $Q_i$ for accelerator $a$.
- $U_i(a)$ be the current GPU allocation used by queue $Q_i$ for accelerator $a$.

### 2.1 Quota Partitioning Invariant
For each queue $Q_i$ and accelerator $a$:
$$U_i(a) = \text{NominalUsed}_i(a) + \text{BorrowedUsed}_i(a)$$

Where:
$$\text{NominalUsed}_i(a) \le N_i(a)$$
$$\text{BorrowedUsed}_i(a) \le B_i(a)$$
$$U_i(a) \le N_i(a) + B_i(a)$$

### 2.2 Lendable Capacity
The capacity that queue $Q_i$ can lend to other queues is the unallocated portion of its nominal quota, capped by its configured `LendingLimit`:
$$\text{Lendable}_i(a) = \min\Big(L_i(a), \, \max\big(0, \, N_i(a) - \text{NominalUsed}_i(a)\big)\Big)$$

The cluster-wide pool of idle borrowable GPUs for accelerator $a$ is:
$$\text{ClusterBorrowable}(a) = \sum_{j \in \mathcal{Q}} \text{Lendable}_j(a) + \Big(C(a) - \sum_{j \in \mathcal{Q}} N_j(a)\Big)$$

### 2.3 DRF-Lite Dominant Share
For queue $Q_i$, its dominant share $s_i$ across all accelerator types is defined as:
$$s_i = \max_{a \in \mathcal{A}} \left( \frac{U_i(a)}{C(a)} \right)$$

When normalized against queue nominal weights:
$$w_i = \max_{a \in \mathcal{A}} \left( \frac{N_i(a)}{C(a)} \right)$$
$$s_i^* = \frac{s_i}{w_i}$$

The queue with the lowest normalized dominant share $s_i^*$ has highest priority for next admission.

---

## 3. Admission Algorithm

When evaluating pending workloads in queue $Q_i$ requesting $R(a)$ GPUs of type $a$:

```
function CanAdmit(Queue Q_i, Workload W):
    a = W.Spec.GPUType
    R = W.Spec.Gang.Size * W.Spec.GPUsPerPod
    
    // Check 1: Can satisfy within remaining nominal quota?
    remainingNominal = N_i(a) - NominalUsed_i(a)
    if R <= remainingNominal:
        return (ADMIT_NOMINAL, 0)
        
    // Check 2: Can satisfy via borrowing?
    deficit = R - max(0, remainingNominal)
    remainingBorrow = B_i(a) - BorrowedUsed_i(a)
    availableIdle = ClusterBorrowable(a)
    
    if deficit <= remainingBorrow and deficit <= availableIdle:
        return (ADMIT_BORROWED, deficit)
        
    // Check 3: Can satisfy via preemption/reclaim within queue?
    if Q_i.Spec.Preemption.ReclaimWithinQueue:
        preemptableWithin = FindPreemptableWithin(Q_i, W)
        if preemptableWithin >= deficit:
            return (PREEMPT_WITHIN, deficit)
            
    // Check 4: Can reclaim borrowed capacity from other queues?
    if Q_i.Spec.Preemption.ReclaimBorrowed and remainingNominal < R:
        reclaimableFromOthers = FindReclaimableFromOthers(Q_i, a)
        if (remainingNominal + reclaimableFromOthers) >= R:
            return (RECLAIM_BORROWED, R - remainingNominal)
            
    return (QUEUED, 0)
```

---

## 4. Reclaim & Victim Selection Tie-Break

When queue $Q_i$ needs to reclaim its nominal quota $N_i(a)$, the controller identifies victim workloads using the following strict lexicographical tie-break ordering:

1. **Borrowers First**: Workloads running on borrowed capacity ($\text{BorrowedUsed} > 0$) are evicted before any workload running on nominal capacity.
2. **Fair-Share Ratio**: Among borrowers, workloads belonging to queues with the highest dominant share $s_j^*$ are prioritized for eviction.
3. **Workload Priority**: Lower `priorityClassName` workloads are evicted before higher priority workloads.
4. **LIFO Admission Time**: Among equal priority workloads, the most recently admitted workload is preempted first (minimizing wasted compute).

---

## 5. Worked Examples

### Example 1: Nominal Allocation & Elastic Borrowing
**Cluster**: 64 $\times$ H100 GPUs ($C = 64$).
- **Queue A (Research)**: $N_A = 32$, $B_A = 32$, $L_A = 16$.
- **Queue B (Product)**: $N_B = 32$, $B_B = 16$, $L_B = 16$.

**State 1: Queue B is idle**:
- $U_B = 0$. $\text{Lendable}_B = \min(16, 32 - 0) = 16$.
- Queue A submits Workload 1 requiring 32 GPUs.
  - Remaining nominal $N_A - U_A = 32 - 0 = 32$. Admitted on nominal.
  - $U_A = 32$ ($\text{Nominal}=32, \text{Borrowed}=0$).
- Queue A submits Workload 2 requiring 16 GPUs.
  - Nominal exhausted. Deficit = 16.
  - Remaining borrow $B_A - 0 = 32 \ge 16$.
  - Idle available from B = 16.
  - Admitted on borrowed capacity!
  - $U_A = 48$ ($\text{Nominal}=32, \text{Borrowed}=16$).
  - $s_A = 48 / 64 = 0.75$.

**State 2: Queue B wakes up and submits a 32-GPU job**:
- $N_B = 32$, $U_B = 0$.
- Available nominal in cluster: $64 - 48 = 16$ GPUs.
- Deficit for B's nominal quota = $32 - 16 = 16$ GPUs.
- Reclaim triggers against Queue A's borrowed Workload 2 (16 GPUs).
- Workload 2 receives checkpoint request, saves state, and drains.
- Queue B receives its full 32 nominal GPUs.
- Invariants hold: no queue exceeded $N + B$, and nominal was fully restored.

---

## 6. Formal Invariants Checked by Property Tests

The following properties are verified using rapid/gopter property-based tests in `internal/controller/queue/`:

1. **Safety Bound**: $\forall Q_i, \forall a \in \mathcal{A}: U_i(a) \le N_i(a) + B_i(a)$.
2. **Conservation of Capacity**: $\sum_{i \in \mathcal{Q}} U_i(a) \le C(a)$.
3. **Atomic Gang Admission**: $\forall W \in \text{Admitted}: |W.\text{Pods}| = W.\text{Spec}.\text{Gang}.\text{Size}$. No partial gangs exist.
4. **Reclaim Termination**: Any reclaim cycle terminates in finite steps ($\le |\text{Workloads}|$).
5. **Monotonic Sequence**: Transition history sequence numbers are strictly monotonically increasing.
