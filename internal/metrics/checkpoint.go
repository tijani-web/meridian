/*
Package metrics provides checkpoint coordination and preemption metric helpers.
*/
package metrics

// RecordCheckpointRequest increments the counter when a checkpoint snapshot request is sent to a gang.
func (m *Metrics) RecordCheckpointRequest(queue string) {
	if m == nil || m.CheckpointRequestTotal == nil {
		return
	}
	m.CheckpointRequestTotal.WithLabelValues(queue).Inc()
}

// RecordCheckpointAckLatency records the time taken for all gang ranks to acknowledge checkpoint completion.
func (m *Metrics) RecordCheckpointAckLatency(queue string, seconds float64) {
	if m == nil || m.CheckpointAckLatencySeconds == nil {
		return
	}
	m.CheckpointAckLatencySeconds.WithLabelValues(queue).Observe(seconds)
}

// RecordCheckpointTimeout increments when a gang fails to ack checkpoint within the grace window,
// leading to involuntary eviction.
func (m *Metrics) RecordCheckpointTimeout(queue string) {
	if m == nil || m.CheckpointTimeoutTotal == nil {
		return
	}
	m.CheckpointTimeoutTotal.WithLabelValues(queue).Inc()
}
