# Runbook: Router Shedding Too Much (HTTP 429)

## Symptoms
- Clients receiving high volume of `HTTP 429 Too Many Requests` with header `Retry-After: N`.
- Prometheus alert `MeridianRouterAvailabilitySLOBreach` or metrics showing high `meridian_router_shed_total`.
- Applications report inference outages or degraded availability.

---

## Architecture Context
The Meridian Inference Router protects backend vLLM engines from memory exhaustion (OOM) and KV-cache thrashing using **priority-aware load shedding**:
- **`maxQueueDepth`**: Maximum sum of queued requests across healthy serving replicas.
- **`batchShedRatio` (default: 0.70)**: When total queue reaches 70% of `maxQueueDepth`, background/batch requests (`X-Priority: batch`) are shed immediately.
- **Full Shedding (100% threshold)**: Interactive and critical requests are shed when total queue exceeds `maxQueueDepth`.
- Replicas with `KV-cache utilization > 90%` are deprioritized by the `KVAware` dispatch strategy.

---

## Diagnostic Steps

### 1. Check Shedding Rate by Priority Tier
Query Prometheus or scrape router `/metrics`:
```promql
sum(rate(meridian_router_shed_total[5m])) by (priority)
```
- If only `priority="batch"` is being shed, the cluster is operating as designed under high load (protecting real-time traffic).
- If `priority="interactive"` or `priority="critical"` is being shed, the backend capacity is truly saturated or replicas are unhealthy.

### 2. Inspect Backend Serving Replica Health & KV Cache Pressure
Query Prometheus:
```promql
meridian_router_kv_cache_usage
```
Or inspect endpoints:
```bash
kubectl get endpointslices -l app.kubernetes.io/component=inference
```
Check if any serving pods have crashed (`OOMKilled`) or are stuck in prefill:
```bash
kubectl get pods -l app.kubernetes.io/component=inference -o wide
```

### 3. Check TTFT Distribution
```promql
histogram_quantile(0.99, sum(rate(meridian_router_ttft_seconds_bucket[5m])) by (le))
```
If TTFT is spiking past 1–2 seconds, the vLLM engines are experiencing heavy scheduling delay.

---

## Mitigation & Resolution

1. **Scale Up Serving Replicas**:
   - Increase replica count on the affected `InferenceDeployment`:
     ```bash
     kubectl scale inferencedeployment <deployment-name> --replicas=<new-count>
     ```
   - Verify that the cluster has available GPU capacity for the new replicas.
2. **Adjust Router Shedding Thresholds**:
   - If backend GPUs have ample headroom and 429s are premature, adjust `maxQueueDepth` in the `InferenceDeployment` routing spec:
     ```yaml
     spec:
       routing:
         loadShedding:
           maxQueueDepth: 500
           batchShedRatio: 0.80
     ```
3. **Switch to PrefixAffinity Routing**:
   - If many queries share identical system prompts or multi-turn chat history, enable `PrefixAffinity` routing strategy to maximize KV-cache reuse and reduce prefill compute by 40–70%:
     ```yaml
     spec:
       routing:
         strategy: PrefixAffinity
     ```
