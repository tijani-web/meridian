# Runbook: Workload Stuck Pending

## Symptoms
- A `GPUWorkload` remains in phase `Pending` for longer than expected (> 60 seconds).
- Pods are created but have `schedulingGates: [meridian.dev/admission-gate]` attached or show `Pending` in `kubectl get pods`.
- Users report jobs not running.

---

## Diagnostic Steps

### 1. Check Workload Status & Reason with `meridianctl`
Run the primary explainability tool:
```bash
meridianctl explain workload <workload-name> -n <namespace>
```
Output will state the exact gating or admission barrier:
- **`Admission Gated`**: Queue quota exceeded or queue has insufficient borrowing headroom.
- **`Scheduled Rejected`**: Filter plugin rejected all candidate nodes.
- **`Permit Wait Timeout`**: Gang members unable to find concurrent co-located placements.

### 2. Inspect Queue Quota & Borrowing
Check if the assigned `GPUQueue` is at full capacity:
```bash
kubectl get gpuqueue <queue-name> -o yaml
```
Look at `.status.allocatedGPUs` vs `.spec.quota.maxGPUs`.
Check if parent queue lending is exhausted.

### 3. Check Pod Scheduling Gates
Verify if the pods are waiting on the admission controller:
```bash
kubectl get pods -n <namespace> -l meridian.dev/workload-name=<workload-name> \
  -o jsonpath='{range .items[*]}{.metadata.name}{"\t"}{.spec.schedulingGates}{"\n"}{end}'
```
If `meridian.dev/admission-gate` is present:
- The queue controller has not admitted the workload yet.
- Check queue controller logs: `kubectl logs -n meridian-system -l app.kubernetes.io/component=manager -c manager | grep queue_controller`.

### 4. Check Scheduler Decision Records
If gates have been cleared (`schedulingGates: []`), check why the scheduler is rejecting candidate nodes:
```bash
curl http://<scheduler-pod-ip>:10259/decisions?namespace=<namespace>&pod=<pod-name> | jq .
```
Common reasons:
- `insufficient nvidia.com/gpu`: No single node has `.spec.gpusPerPod` free GPUs.
- `topology SameNVLinkDomain unmet`: Cluster lacks nodes in the same NVLink domain with sufficient capacity.
- `unhealthy taint present`: Nodes are marked `meridian.dev/gpu-unhealthy`.

---

## Mitigation & Resolution

1. **If Quota Blocked**:
   - Reallocate queue quota in the `GPUQueue` manifest or wait for higher-priority workloads to finish.
   - If preemption is allowed, verify that lower-priority workloads in other queues can be preempted.
2. **If Fragmented Fleet**:
   - Use `meridianctl status cluster` to check fragmentation ratio.
   - If small 1-GPU pods are occupying 8-GPU nodes, adjust scheduling priority or drain/consolidate pods onto dedicated nodes.
3. **If Tainted Nodes**:
   - Follow the [Node Flapping Runbook](./node-flapping.md) to inspect and recover faulty GPU nodes.
