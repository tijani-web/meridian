# Runbook: Controller Leader Election Failover

## Symptoms
- The active `meridian-manager` or `meridian-scheduler` pod crashes, is evicted, or enters `CrashLoopBackOff`.
- Log entries state: `leaderelection lost lease`, `leader changed`, or `failed to renew lease`.
- Workload reconciliations or scheduling decisions momentarily pause.

---

## Architecture Context
Meridian runs both the controller-manager and scheduler in High Availability (HA) active-standby pairs:
- **Leader Election Mechanism**:
  - Uses Kubernetes `coordination.k8s.io/v1` `Lease` resources in namespace `meridian-system`.
  - Lease: `meridian-manager` (held by manager pod).
  - Lease: `meridian-scheduler` (held by scheduler pod).
- **Lease Timers**:
  - `leaseDuration`: 15s (time before lease expires if not renewed).
  - `renewDeadline`: 10s (time active leader attempts renewal before giving up leadership).
  - `retryPeriod`: 2s (interval between renewal attempts).
- **Crash Safety Invariant**:
  - Controllers use Server-Side Apply (SSA) and idempotent reconciliation.
  - Failover does not duplicate pods, corrupt quotas, or drop inflight state.

---

## Diagnostic Steps

### 1. Identify the Current Lease Holder
Inspect the manager lease:
```bash
kubectl get lease meridian-manager -n meridian-system -o yaml
```
Look at `.spec.holderIdentity` and `.spec.renewTime`.
Inspect the scheduler lease:
```bash
kubectl get lease meridian-scheduler -n meridian-system -o yaml
```

### 2. Check Controller Pod Logs During Failover
Inspect the standby pod that assumed leadership:
```bash
NEW_LEADER=$(kubectl get lease meridian-manager -n meridian-system -o jsonpath='{.spec.holderIdentity}')
kubectl logs -n meridian-system $NEW_LEADER -c manager | grep -E "leader|promoted|starting reconciler"
```
Look for:
- `successfully acquired lease meridian-system/meridian-manager`
- `starting controllers`
- `starting workers`

### 3. Verify Quota and FSM Reconciliation Integrity
Ensure the new leader re-indexed all queues and workloads without discrepancy:
```bash
meridianctl status cluster
```
Verify that all running workloads remain in phase `Running` and no duplicate pods were created.

---

## Manual Failover Procedure (Drill / Maintenance)

To test leader election failover manually:
1. Locate current leader:
   ```bash
   LEADER=$(kubectl get lease meridian-manager -n meridian-system -o jsonpath='{.spec.holderIdentity}')
   ```
2. Delete the active leader pod:
   ```bash
   kubectl delete pod -n meridian-system $LEADER
   ```
3. Time the promotion of the standby replica:
   ```bash
   # Standby should acquire lease within <= 15 seconds
   kubectl get lease meridian-manager -n meridian-system -w
   ```
4. Verify that the crashed/deleted pod is recreated by its Deployment and enters standby mode.
