# Runbook: Queue Starvation

## Symptoms
- Workloads submitted to a specific `GPUQueue` are not getting admitted, while other queues continue scheduling jobs.
- The affected queue has non-zero `guaranteedGPUs`, but its `allocatedGPUs` remains below its guaranteed quota.
- Prometheus alert `MeridianAdmissionLatencySLOBreach` fires on the affected queue.

---

## Architecture Context
Meridian implements **DRF (Dominant Resource Fairness) and dynamic borrowing/reclaiming**:
- Queues can borrow unused capacity from peers up to their `maxGPUs`.
- When a lender queue receives new workloads, the queue controller triggers **preemption / reclamation** of borrowed GPUs.
- Reclaiming must adhere to the checkpoint grace period (`ackTimeout`). If borrower workloads fail to checkpoint in time, reclamation can stall until the timeout expires.

---

## Diagnostic Steps

### 1. Inspect Cluster-Wide Queue Quota State
Run:
```bash
meridianctl status cluster
```
Examine:
- Per-queue `guaranteed` vs `allocated` vs `borrowed` GPUs.
- Check which queues are holding borrowed GPUs from the starved queue.

### 2. Check Workload Preemption Status
Query `meridianctl explain` on the pending workload in the starved queue:
```bash
meridianctl explain workload <starved-workload> -n <namespace>
```
Look at section `6. Preemption History`:
- Check if preemption requests have been dispatched to borrowing workloads.
- Check if candidate victims are currently in `Preempting` phase awaiting checkpoint acknowledgments.

### 3. Check Victim Pod Checkpointing Progress
```bash
kubectl get pods -A -l meridian.dev/checkpoint-request \
  -o custom-columns=NS:.metadata.namespace,POD:.metadata.name,REQ:.metadata.annotations.meridian\.dev/checkpoint-request,ACK:.metadata.annotations.meridian\.dev/checkpoint-ack
```
If victims have `checkpoint-request` without `checkpoint-ack`, they are writing checkpoints to storage.

---

## Mitigation & Resolution

1. **Stuck Checkpointing Victim (Straggler)**:
   - If a borrower workload is hung and not responding to checkpoint requests, verify that the controller's `ackTimeout` (default: 60s) will force evict it.
   - If emergency capacity is needed immediately, force delete the stuck pod:
     ```bash
     kubectl delete pod <victim-pod> -n <borrower-namespace> --grace-period=0 --force
     ```
2. **Borrowing Policy Misconfiguration**:
   - Verify that the borrower queue has `spec.borrowing.enabled: true` and that its `priorityTier` is lower than or equal to the starved queue.
   - If a queue should never borrow from a critical queue, isolate it into a dedicated non-shared quota group or set `maxGPUs = guaranteedGPUs`.
3. **Queue Weights**:
   - Check DRF fair-share weights in the queue specs: `spec.weight`. Ensure the starved queue has an appropriate weight relative to the workload demand.
