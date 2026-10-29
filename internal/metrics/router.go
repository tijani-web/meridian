/*
Package metrics provides inference data plane and reverse proxy metric helpers.
*/
package metrics

// RecordRouterRequest records the outcome of an inference dispatch request.
// Strategies include: RoundRobin, LeastQueue, KVAware, PrefixAffinity.
// Results include: success, shed_429, no_replicas_503, bad_gateway_502, error_500.
func (m *Metrics) RecordRouterRequest(strategy, priority, result string) {
	if m == nil || m.RouterRequestTotal == nil {
		return
	}
	m.RouterRequestTotal.WithLabelValues(strategy, priority, result).Inc()
}

// RecordRouterTTFT records Time-To-First-Token in seconds.
// This is the primary driver of SLO-01 (TTFT p99 < 150ms over 30 days).
func (m *Metrics) RecordRouterTTFT(strategy, replica string, seconds float64) {
	if m == nil || m.RouterTTFTSeconds == nil {
		return
	}
	m.RouterTTFTSeconds.WithLabelValues(strategy, replica).Observe(seconds)
}

// RecordRouterTokens records the number of output completion tokens streamed downstream.
func (m *Metrics) RecordRouterTokens(strategy, replica string, count float64) {
	if m == nil || m.RouterTokensGeneratedTotal == nil {
		return
	}
	m.RouterTokensGeneratedTotal.WithLabelValues(strategy, replica).Add(count)
}

// IncRouterActiveRequests increments in-flight active request count for a strategy.
func (m *Metrics) IncRouterActiveRequests(strategy string) {
	if m == nil || m.RouterActiveRequests == nil {
		return
	}
	m.RouterActiveRequests.WithLabelValues(strategy).Inc()
}

// DecRouterActiveRequests decrements in-flight active request count for a strategy.
func (m *Metrics) DecRouterActiveRequests(strategy string) {
	if m == nil || m.RouterActiveRequests == nil {
		return
	}
	m.RouterActiveRequests.WithLabelValues(strategy).Dec()
}

// SetRouterKVCacheUsage updates the scraped KV-cache utilization fraction (0.0 - 1.0) for an upstream replica.
func (m *Metrics) SetRouterKVCacheUsage(replica string, usage float64) {
	if m == nil || m.RouterKVCacheUsage == nil {
		return
	}
	m.RouterKVCacheUsage.WithLabelValues(replica).Set(usage)
}

// RecordPrefixCacheHit increments the hit counter when a request is dispatched to a replica
// already holding a warm prefix KV cache match.
func (m *Metrics) RecordPrefixCacheHit(strategy, replica string) {
	if m == nil || m.RouterPrefixCacheHitsTotal == nil {
		return
	}
	m.RouterPrefixCacheHitsTotal.WithLabelValues(strategy, replica).Inc()
}
