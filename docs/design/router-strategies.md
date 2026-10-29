# docs/design/router-strategies.md — Inference Router Architecture & Routing Strategies

## 1. Overview & Mission

Meridian's Inference Router (`meridian-router`) serves as the ultra-low-latency data plane reverse proxy for Large Language Model (LLM) inference workloads. Positioned in front of vLLM replica clusters, it proxies OpenAI-compatible `/v1/chat/completions` and `/v1/completions` traffic while optimizing resource utilization, Time-to-First-Token (TTFT), and Time-Per-Output-Token (TPOT).

Key Architectural Pillars:
1. **Real Metrics Telemetry**: Continuously scrapes vLLM Prometheus metrics endpoints with staleness detection.
2. **Pluggable Strategies**: Provides four distinct dispatch strategies (`RoundRobin`, `LeastQueue`, `KVAware`, `PrefixAffinity`).
3. **Prefix-Cache Awareness**: Bounded-load consistent hashing to maximize KV-cache reuse in vLLM RadixAttention.
4. **Resilient Load Shedding**: Prevents tail-latency collapse via priority-aware HTTP 429 shedding with `Retry-After` signaling.
5. **Zero-Overhead Streaming**: Server-Sent Events (SSE) passthrough with prompt flushing and client disconnect cancellation.

---

## 2. Scraping Contract & Telemetry Engine

The router maintains an active in-memory replica state table updated via lightweight asynchronous HTTP scrapers querying each replica's `/metrics` endpoint.

### 2.1 Pinned vLLM v0.9.2 Metrics

| Metric Name | Prometheus Type | Semantics in Router |
|---|---|---|
| `vllm:num_requests_waiting` | Gauge | Number of requests waiting in the engine queue (queue depth). |
| `vllm:num_requests_running` | Gauge | Number of requests currently executing in the engine. |
| `vllm:gpu_cache_usage_perc` | Gauge | Fraction of GPU KV-cache allocated (value in range $[0.0, 1.0]$). |
| `vllm:time_to_first_token_seconds` | Histogram | Upstream engine TTFT distribution. |
| `vllm:time_per_output_token_seconds` | Histogram | Upstream engine inter-token decode latency. |

### 2.2 Staleness and Decay

If a replica fails to answer metric scrapes within the scraping interval (default 500ms) or exceeds the staleness threshold ($T_{\text{stale}} = 3.0\text{s}$), the router marks the telemetry as stale and inflates its effective queue depth by a penalty factor ($+100$), ensuring degraded or non-responsive replicas do not attract new traffic.

---

## 3. Four Dispatch Strategies (§6.7, §8.3)

### 3.1 Strategy 1: `RoundRobin` (Baseline)
- Cycles strictly through the active, healthy replica pool:
  $$\text{Target} = R[(i + 1) \pmod N]$$
- Serves as the un-optimized baseline comparison metric.

### 3.2 Strategy 2: `LeastQueue`
- Minimizes immediate queuing delay by dispatching to the replica with the minimum total active load:
  $$\text{Load}(r) = \text{waiting\_requests}(r) + \text{running\_requests}(r) + \text{inflight\_routed}(r)$$
- Where $\text{inflight\_routed}(r)$ is atomically tracked in the proxy between the moment of dispatch and the receipt of the first byte or upstream disconnect.
- Tie-breaking: lowest current KV-cache usage.

### 3.3 Strategy 3: `KVAware`
- Designed for memory-constrained LLM serving where KV-cache exhaustion leads to prompt preemption and re-computation penalties.
- Defines a safety threshold $K_{\text{safe}} = 0.85$ (85% KV-cache occupancy).
- Algorithm:
  1. Filter candidates where $\text{KVUsage}(r) < K_{\text{safe}}$.
  2. If candidate set is non-empty, select candidate with $\arg\min \text{Load}(r)$.
  3. If all replicas exceed $K_{\text{safe}}$ (cluster-wide memory saturation), select candidate with lowest $\text{KVUsage}(r)$ to minimize the severity of preemption.

### 3.4 Strategy 4: `PrefixAffinity` (Consistent Hashing with Bounded Load)
- Designed to exploit vLLM's RadixTree / PagedAttention prefix caching. Re-sending identical system prompts or multi-turn conversational prefixes to the same replica achieves near-instantaneous prefill ($\text{TTFT} \approx 0$).
- **Prefix Extraction**: Hashes the first $L$ tokens (or the system prompt content) using 64-bit FNV-1a:
  $$h = \text{Hash}(\text{SystemPrompt} \lor \text{Prefix})$$
- **Bounded Load Invariant**: To prevent hotspotting on viral system prompts, the router enforces Mirrokni et al.'s bounded-load consistent hashing:
  $$\text{MaxAllowedLoad} = \lceil 1.25 \times \text{AverageClusterLoad} \rceil$$
  - Primary target: $r_0 = \text{HashRing}(h)$.
  - If $\text{Load}(r_0) \le \text{MaxAllowedLoad}$, dispatch to $r_0$ (cache hit).
  - If $\text{Load}(r_0) > \text{MaxAllowedLoad}$, spill over to `LeastQueue` candidate to preserve latency SLA at the cost of prefill re-computation.

---

## 4. Priority-Aware Load Shedding

When total pending inference requests exceed the configured threshold ($Q_{\text{max}}$), the router protects upstream vLLM workers from thrashing by immediately shedding excess load at the proxy boundary.

1. **HTTP Status**: 429 Too Many Requests.
2. **Header**: `Retry-After: <delta-seconds>` (calculated dynamically based on average token generation rate).
3. **Priority Tiers**:
   - `interactive` (default): shedding triggered only when $Q > Q_{\text{max}}$.
   - `batch`: shedding triggered when $Q > 0.70 \times Q_{\text{max}}$ to preserve capacity for interactive users.

---

## 5. Streaming Pass-Through & Connection Management

- Implements `http.Flusher` wrapping downstream response writer.
- Writes SSE tokens (`data: ...\n\n`) as soon as chunks arrive from upstream without buffering.
- Intercepts `r.Context().Done()`: if a client closes their browser/connection early, the router cancels the upstream HTTP request, freeing vLLM GPU cycles and KV-cache slots immediately.
