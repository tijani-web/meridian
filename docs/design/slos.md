# Design: SLOs and Burn-Rate Alerting

## SLO Definitions

Meridian defines four production SLOs. Each SLO is a ratio-based objective measured
over a rolling 30-day window. All SLOs are implemented as PrometheusRules in
`deploy/monitoring/prometheus-rule.yaml`.

### SLO-01: Gang Start Latency (p95 ≤ 120s)

**What it measures**: The fraction of time in a rolling 30-day window that the p95
gang start latency (time from workload admitted to all pods Running) is below 120 seconds.

**Why 120s**: Based on observed mean gang start of ~7.5s at 500 nodes and ~32s at
2,000 nodes (see bench results). 120s provides a 4× headroom for large gangs under
contention.

**Prometheus expression**:
```promql
histogram_quantile(0.95,
  sum(rate(meridian_gang_start_latency_seconds_bucket[5m])) by (le)
) <= 120
```

### SLO-02: Admission Latency (p99 ≤ 5s)

**What it measures**: Time from GPUWorkload creation to SchedulingGate removal
(workload admitted by the queue controller).

**Why 5s**: The admission path is pure memory computation (fair-share accounting,
quota check, borrow/lend). 5s is generous even under 2,000 workload backlog.

### SLO-03: Inference Router Availability (≥ 99.9%)

**What it measures**: Fraction of router requests that receive a non-5xx response.
HTTP 429 (load shedding) counts as **available** — it is intentional backpressure.

**Why 99.9%**: The router is a proxy, not a stateful service. 99.9% allows for
controlled rolling restarts without multi-nines pressure.

### SLO-04: Checkpoint Ack Latency (p95 ≤ 60s)

**What it measures**: Time from checkpoint-requested annotation to checkpoint-ack
annotation being set by the trainer.

**Why 60s**: The checkpoint SLA is configurable per workload (`spec.checkpoint.ackTimeout`).
60s is the default. SLO-04 tracks p95 across all workloads.

---

## Burn-Rate Alerting

Meridian uses **multi-window multi-burn-rate** alerting (Google SRE Workbook chapter 5).
A single threshold alert on error rate has slow response time and high false-positive rate.
Burn-rate alerts detect budget exhaustion faster.

### Budget calculation

For each SLO, the **error budget** = 1 − SLO threshold. Example for SLO-03 (99.9%):

```
Monthly budget = (1 - 0.999) × 30 days × 24 h × 60 min = 43.2 minutes
```

### Burn rates and alert windows

| Severity | Short window | Long window | Burn rate | Budget consumed in |
|---|---|---|---|---|
| Critical (page) | 5m | 1h | 14× | ~2 hours |
| High (ticket) | 30m | 6h | 6× | ~5 hours |
| Low (warning) | 2h | 24h | 1× | 30 days |

**Critical alert fires** when both:
- 5-minute burn rate > 14× baseline error rate
- 1-hour burn rate > 14× baseline error rate

This two-window requirement prevents spurious alerts from short spikes.

### PrometheusRule example (SLO-03)

```yaml
- alert: MeridianRouterHighErrorBurnRate
  expr: |
    (
      rate(meridian_router_requests_total{status=~"5.."}[5m])
      / rate(meridian_router_requests_total[5m])
    ) > (14 * 0.001)
    and
    (
      rate(meridian_router_requests_total{status=~"5.."}[1h])
      / rate(meridian_router_requests_total[1h])
    ) > (14 * 0.001)
  for: 2m
  labels:
    severity: critical
    slo: SLO-03
  annotations:
    summary: "Router error burn rate critical (14×)"
    description: >
      At current burn rate, the 30-day error budget will be exhausted in ~2h.
      Investigate router pod logs and backend health.
```

---

## Gang Partial-Start Invariant Alert

This is not an SLO — it is a **hard invariant**. The counter
`meridian_gang_partial_starts_total` must always be 0. Any non-zero value
indicates a correctness bug, not just a latency or availability issue.

```yaml
- alert: MeridianGangPartialStartInvariantViolation
  expr: meridian_gang_partial_starts_total > 0
  for: 0m  # immediate — no window; any non-zero is critical
  labels:
    severity: critical
  annotations:
    summary: "INVARIANT VIOLATED: partial gang start detected"
    description: >
      meridian_gang_partial_starts_total = {{ $value }}.
      One or more gang pods were placed while other gang members were not.
      This is a correctness bug. Page on-call immediately.
```

---

## SLO Dashboard

The "Workload & Scheduling" Grafana dashboard (in `deploy/monitoring/grafana-dashboards.yaml`)
includes a dedicated SLO panel showing:
- Rolling 30-day error budget remaining (percentage and minutes)
- Current burn rate vs threshold lines
- p95/p99 latency trend for SLO-01 and SLO-04
