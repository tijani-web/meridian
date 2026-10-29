# Runbook: Gang Workload Never Starts

## Symptoms
- A gang `GPUWorkload` (`spec.gang.size > 1`) cycles between `Admitted` and `Pending`, or never transitions to `Running`.
- Scheduler logs report: `Permit timeout expired for gang <gang-id>, rejecting all members`.
- Prometheus alert `MeridianGangStartLatencySLOBreach` is firing.

---

## Architecture Context
Meridian enforces strict **all-or-nothing atomicity** for gang scheduling.
If `spec.gang.size = 8` and `minAvailable = 8`:
1. All 8 pods must pass Filter, Score, and Reserve plugins.
2. The first 7 pods enter `Wait` status at the `Permit` plugin.
3. If the 8th pod cannot bind within `permitTimeoutSeconds` (default: 120s), the permit plugin cancels all 7 tentative reservations, emits a gang timeout event, and returns all pods to unschedulable.
**Zero partially-started gangs are ever bound to nodes.**

---

## Diagnostic Steps

### 1. Check Gang Reservation Progress
Query `meridianctl explain`:
```bash
meridianctl explain workload <workload-name> -n <namespace>
```
Look at section `5. Permit Status`:
- `Reserved members`: e.g. `6 / 8`.
- `Missing members`: 2 pods.
- `Time until timeout`: e.g. `42s remaining`.

### 2. Identify Why the Straggler Pod Failed Filter
Inspect the pods that failed to reserve:
```bash
kubectl get pods -n <namespace> -l meridian.dev/workload-name=<workload-name> \
  -o custom-columns=NAME:.metadata.name,NODE:.spec.nodeName,STATUS:.status.phase
```
For any unassigned pod, fetch its decision records from the scheduler:
```bash
curl "http://<scheduler-pod>:10259/decisions?namespace=<namespace>&pod=<pod-name>" | jq .
```
Verify if:
- `topology`: The gang is locked to `meridian.dev/nvlink-domain=nvlink-0`, but only 6 slots were free in that domain.
- `affinity`: The remaining 2 pods cannot co-locate with the first 6 within the same topology constraint.

### 3. Check for Competing Workloads
Check if another concurrent gang reservation is locking out the remaining nodes:
```bash
kubectl get pods -A -o wide | grep -E "meridian-scheduler" | grep "ContainerCreating\|Pending"
```

---

## Mitigation & Resolution

1. **Topology Constraint Too Strict**:
   - If `spec.topology.required = true`, the scheduler cannot place pods across multiple NVLink domains or racks.
   - If acceptable for the training framework (e.g. pipeline parallelism across racks, tensor parallelism within node), set `spec.topology.required = false` and `preference = SameRack`.
2. **Increase Permit Timeout**:
   - For very large gangs (e.g. 64–128 GPUs), scheduling cycles across hundreds of nodes can take 30–60s.
   - If permit timeouts occur frequently under high churn, increase `permitTimeoutSeconds` in the Helm values (`scheduler.permitTimeoutSeconds: 180`).
3. **Queue Prioritization**:
   - Move critical large gang jobs to a higher-priority queue (`high-priority` or `critical`) so that preemption can clear contiguous blocks of capacity.
