# Runbook: Rollback a Canary Deployment

## Symptoms
- A new model revision in an `InferenceDeployment` is rolling out via Canary.
- Error rates spike or p99 TTFT exceeds SLA thresholds during canary soak.
- Automated rollback was triggered or an operator needs to force an immediate manual rollback.

---

## Architecture Context
The Meridian Inference Control Plane manages canary deployments with automated analysis:
- **Canary Steps**: e.g., 10% -> 25% -> 50% -> 100% traffic shift.
- **RolloutAnalysis**: Evaluates router Prometheus metrics during each step's `pauseDuration`:
  - `maxErrorRate`: e.g. "0.01" (1% threshold).
  - `maxTTFTMillis`: e.g. 150 (150ms p99 threshold).
- **Automated Rollback**: If metrics breach thresholds, the controller immediately:
  1. Sets canary revision traffic weight to 0%.
  2. Restores stable revision to 100%.
  3. Updates `InferenceDeployment.status.phase` to `RolledBack`.
  4. Updates routing table ConfigMap `<deployment>-routing-table`.

---

## Diagnostic Steps

### 1. Check InferenceDeployment Status
Inspect the deployment phase and rollout status:
```bash
kubectl get inferencedeployments <name> -n <namespace> -o yaml
```
Look at:
- `.status.phase`: `RollingOut`, `RolledBack`, or `Running`.
- `.status.canary.currentWeight`: Traffic weight percentage.
- `.status.canary.analysisResult`: Metric evaluation details (e.g. `errorRate: 0.045 > 0.010 threshold`).

### 2. Inspect the Live Routing Table ConfigMap
Verify the active endpoint weights read by the router:
```bash
kubectl get configmap <name>-routing-table -n <namespace> -o jsonpath='{.data.routing-table\.json}' | jq .
```
Verify that the canary endpoints have `weight: 0` and stable has `weight: 100`.

### 3. Check Router Metrics During Rollout
```promql
# Compare error rate by revision
sum(rate(meridian_router_request_total{result=~"bad_gateway_502|error_500"}[5m])) by (revision)
/
sum(rate(meridian_router_request_total[5m])) by (revision)
```

---

## Manual Rollback Procedure

If automated rollback is disabled or operator intervention is required immediately:

### Option 1: Abort Canary via `meridianctl`
```bash
meridianctl rollback inference <name> -n <namespace>
```

### Option 2: Direct Spec Reversion via `kubectl`
Revert `spec.model.uri` or `spec.runtime.image` to the previous stable parameters:
```bash
kubectl patch inferencedeployment <name> -n <namespace> --type=merge -p '{
  "spec": {
    "rollout": {
      "strategy": "Recreate"
    }
  }
}'
```
Or directly scale down the canary revision deployment:
```bash
CANARY_DEP=$(kubectl get deployments -l meridian.dev/inference-name=<name>,meridian.dev/inference-role=canary -n <namespace> -o jsonpath='{.items[0].metadata.name}')
kubectl scale deployment $CANARY_DEP -n <namespace> --replicas=0
```

The router immediately detects the zero-weight / unhealthy endpoints via its EndpointSlice watcher and redirects 100% of traffic to the stable revision within < 5 seconds.
