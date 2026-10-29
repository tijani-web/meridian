# Runbook: Node Flapping (GPU Health & Taints)

## Symptoms
- A GPU node repeatedly toggles between `Ready/Healthy` and `Unhealthy`.
- Pods are evicted, rescheduled to the same node, and evicted again.
- Prometheus alerts: `MeridianGPUFatalXIDDetected` or `MeridianGPUECCDoubleBitError`.
- High frequency of node condition updates for `meridian.dev/GPUHealthy`.

---

## Architecture Context
The Meridian node agent runs on each GPU node, monitoring hardware health via NVML or DCGM:
- **Flap Damping with Hysteresis**:
  - `unhealthyThreshold`: 3 consecutive failed health evaluations required to apply taint `meridian.dev/gpu-unhealthy:NoSchedule`.
  - `recoveryThreshold`: 5 consecutive successful health evaluations required to remove the taint.
- **Fatal vs Recoverable XID Errors**:
  - Fatal XIDs (e.g. XID 31 "GPU memory page fault", XID 45 "Preemptive cleanup", XID 79 "GPU fell off the bus"): cause immediate cordon and drain.
  - Thermal throttling: mild penalty during scoring without immediate pod eviction.

---

## Diagnostic Steps

### 1. Inspect Node Conditions & Taints
Check the node status and taints:
```bash
kubectl describe node <node-name> | grep -E "Taints:|meridian.dev" -A 10
```
Check conditions:
- `meridian.dev/GPUHealthy`: Status `False`, Reason e.g. `XIDFatalError` or `ECCDoubleBitError`.

### 2. Inspect Agent Logs on the Flapping Node
Find the agent pod running on the affected node:
```bash
AGENT_POD=$(kubectl get pods -n meridian-system -l app.kubernetes.io/component=agent \
  --field-selector spec.nodeName=<node-name> -o jsonpath='{.items[0].metadata.name}')

kubectl logs -n meridian-system $AGENT_POD -c agent | tail -n 50
```
Look for:
- Specific XID numbers (`XID: 31`, `XID: 79`, `XID: 48`).
- Double-bit ECC memory error counts.
- DCGM scrape errors or timeouts.

### 3. Check DCGM Exporter Endpoint
If the agent uses the DCGM source, verify the local exporter:
```bash
kubectl exec -n meridian-system $AGENT_POD -c agent -- curl -s http://localhost:9400/metrics | grep -E "DCGM_FI_DEV_XID_ERRORS|DCGM_FI_DEV_ECC_DBE_VOL_TOTAL"
```

---

## Mitigation & Resolution

1. **Hardware Defect (XID 79, ECC Double-Bit Error)**:
   - Double-bit ECC errors and PCIe bus drops are permanent hardware faults.
   - Manually cordon the node permanently to prevent any further scheduling:
     ```bash
     kubectl cordon <node-name>
     kubectl drain <node-name> --ignore-daemonsets --delete-emptydir-data --force
     ```
   - Page the hardware infrastructure / data center team for GPU replacement.
2. **Transient Flapping (Thermal or Driver Glitch)**:
   - If health probes are flapping due to brief intermittent latency, increase the hysteresis threshold in the Helm chart:
     ```yaml
     agent:
       flapDamping:
         unhealthyThreshold: 5
         recoveryThreshold: 10
     ```
3. **Resetting a Cleaned Node**:
   - Once drivers are reloaded or GPU is reseated:
     ```bash
     kubectl taint node <node-name> meridian.dev/gpu-unhealthy:NoSchedule-
     kubectl uncordon <node-name>
     ```
