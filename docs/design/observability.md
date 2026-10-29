# Meridian Observability Design — Phase 11

## Overview

Meridian exposes two tiers of observability:

1. **Operational metrics**: Prometheus counters/histograms/gauges instrumented inside controllers, the router, and the node health agent.
2. **Distributed traces**: OpenTelemetry spans (Phase 07) exported to Jaeger/Tempo.

This document covers the metrics tier.

---

## Metrics Catalog

All metrics use the `meridian_` prefix. Labels use `snake_case`.

### Workload Admission

| Metric | Type | Labels | Description |
|---|---|---|---|
| `meridian_workload_admission_latency_seconds` | Histogram | queue, priority | Time from submission to gate release |
| `meridian_workload_admission_total` | Counter | queue, priority, result | Admission decisions |
| `meridian_workload_preemptions_total` | Counter | queue, reason | Preemption events |
| `meridian_workload_phase_transitions_total` | Counter | from_phase, to_phase | FSM transitions |
| `meridian_queue_depth` | Gauge | queue, priority | Waiting workloads |
| `meridian_queue_gpu_allocated` | Gauge | queue, gpu_type | Allocated GPUs per queue |

### Gang Scheduling

| Metric | Type | Labels | Description |
|---|---|---|---|
| `meridian_gang_start_latency_seconds` | Histogram | queue, gang_size | Gate release to all pods Running |
| `meridian_gang_partial_starts_total` | Counter | queue | Partial gangs (invariant: must be 0) |
| `meridian_gang_permit_wait_seconds` | Histogram | queue | Time in Permit hold |

### Inference Router

| Metric | Type | Labels | Description |
|---|---|---|---|
| `meridian_router_request_total` | Counter | strategy, priority, result | Request outcomes |
| `meridian_router_ttft_seconds` | Histogram | strategy, replica | Time-to-first-token |
| `meridian_router_tokens_generated_total` | Counter | strategy, replica | Tokens streamed |
| `meridian_router_active_requests` | Gauge | strategy | In-flight requests |
| `meridian_router_kv_cache_usage` | Gauge | replica | Scraped KV-cache utilization |
| `meridian_router_prefix_cache_hits_total` | Counter | strategy, replica | Prefix-matched routing hits |

### Node Health & GPU

| Metric | Type | Labels | Description |
|---|---|---|---|
| `meridian_gpu_xid_events_total` | Counter | node, gpu, xid, severity | XID error events |
| `meridian_gpu_ecc_dbe_total` | Counter | node, gpu | Double-bit ECC errors |
| `meridian_gpu_temperature_celsius` | Gauge | node, gpu | GPU die temperature |
| `meridian_gpu_healthy` | Gauge | node, gpu | 1=healthy, 0=tainted |
| `meridian_node_recovery_duration_seconds` | Histogram | node | XID detection to workload rescheduled |

### Checkpoint & Preemption

| Metric | Type | Labels | Description |
|---|---|---|---|
| `meridian_checkpoint_request_total` | Counter | queue | Checkpoint requests dispatched |
| `meridian_checkpoint_ack_latency_seconds` | Histogram | queue | Request to full gang ack |
| `meridian_checkpoint_timeout_total` | Counter | queue | Ack timeouts (force-eviction path) |

---

## SLO Definitions

### SLO-01: Router TTFT p99 < 150ms (30-day)
- Target: 99% requests TTFT < 150ms
- Error budget: 432 min/month
- Alerts: 14x burn (1h/5m) critical, 6x (6h/30m) warning

### SLO-02: Admission Latency p99 < 500ms (30-day)
- Target: 99% workloads admitted within 500ms of submission

### SLO-03: Gang Start Latency p99 < 30s (30-day)
- Target: 99% gangs fully Running within 30s of gate release

### SLO-04: Router Availability > 99.9% (30-day)
- Target: <0.1% error rate on router requests

---

## Grafana Dashboard Layout (3 dashboards)

### Dashboard 1: GPU Fleet Overview
- Total GPUs available/allocated/free (stat)
- GPU utilization heatmap by node
- XID events by severity (time series)
- GPU temperature by node
- Unhealthy node count

### Dashboard 2: Workload & Scheduling
- Admission latency p50/p99 by queue
- Queue depth by priority (stacked bars)
- Gang start latency p50/p99
- Preemptions per hour by reason
- Partial gang starts (invariant gauge — alert if > 0)

### Dashboard 3: Inference Router
- TTFT p50/p99 by strategy (SLO line at 150ms)
- Request throughput by strategy
- Active in-flight requests
- KV-cache utilization per replica
- Prefix cache hit rate by strategy
- Load-shed rate
- Router error rate vs SLO-04 budget

---

## Implementation Plan

1. `internal/metrics/registry.go` — singleton registry, all metric declarations
2. `internal/metrics/workload.go` — admission/preemption/phase-transition helpers
3. `internal/metrics/router.go` — TTFT, throughput, KV-cache helpers
4. `internal/metrics/gpu.go` — XID, ECC, temperature, health gauge helpers
5. `internal/metrics/checkpoint.go` — checkpoint request/ack/timeout helpers
6. `internal/metrics/metrics_test.go` — unit tests: no label collisions, histogram bounds, counter monotonicity
7. Instrument `internal/controller/workload/`, `queue/`, `health/`, `inference/`
8. Instrument `internal/router/proxy.go` and `scraper.go`
9. `deploy/monitoring/prometheus-rule.yaml` — SLO burn-rate + alert PrometheusRules
10. `deploy/monitoring/service-monitor.yaml` — ServiceMonitor CRs
11. `deploy/monitoring/grafana-dashboards.yaml` — ConfigMap with 3 Grafana dashboard JSONs
