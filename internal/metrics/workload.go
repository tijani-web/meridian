/*
Package metrics provides workload admission and gang scheduling instrumentation helpers.
*/
package metrics

// RecordAdmissionLatency records the duration in seconds taken from workload creation to scheduling gate release.
// This directly feeds the SLO-02 latency monitor (99% admitted within 500ms).
func (m *Metrics) RecordAdmissionLatency(queue, priority string, seconds float64) {
	if m == nil || m.WorkloadAdmissionLatencySeconds == nil {
		return
	}
	m.WorkloadAdmissionLatencySeconds.WithLabelValues(queue, priority).Observe(seconds)
}

// RecordAdmission tracks an admission decision outcome (e.g. admitted, queued, rejected, timeout).
func (m *Metrics) RecordAdmission(queue, priority, result string) {
	if m == nil || m.WorkloadAdmissionTotal == nil {
		return
	}
	m.WorkloadAdmissionTotal.WithLabelValues(queue, priority, result).Inc()
}

// RecordPreemption tracks when a running workload is preempted to make room for higher-priority or reclaim.
func (m *Metrics) RecordPreemption(queue, reason string) {
	if m == nil || m.WorkloadPreemptionsTotal == nil {
		return
	}
	m.WorkloadPreemptionsTotal.WithLabelValues(queue, reason).Inc()
}

// RecordPhaseTransition tracks transitions within the workload FSM (Pending -> Admitted -> Running -> etc.).
func (m *Metrics) RecordPhaseTransition(fromPhase, toPhase string) {
	if m == nil || m.WorkloadPhaseTransitionsTotal == nil {
		return
	}
	m.WorkloadPhaseTransitionsTotal.WithLabelValues(fromPhase, toPhase).Inc()
}

// SetQueueDepth sets the current number of queued workloads awaiting scheduling gates for a queue and priority.
func (m *Metrics) SetQueueDepth(queue, priority string, depth float64) {
	if m == nil || m.QueueDepth == nil {
		return
	}
	m.QueueDepth.WithLabelValues(queue, priority).Set(depth)
}

// SetQueueGPUAllocated sets the number of physical GPUs currently assigned to active workloads in a queue.
func (m *Metrics) SetQueueGPUAllocated(queue, gpuType string, count float64) {
	if m == nil || m.QueueGPUAllocated == nil {
		return
	}
	m.QueueGPUAllocated.WithLabelValues(queue, gpuType).Set(count)
}

// RecordGangStartLatency records the elapsed time from gate release until all pods in the gang are in Running state.
// Feeds SLO-03 (99% gangs running within 30 seconds).
func (m *Metrics) RecordGangStartLatency(queue, gangSize string, seconds float64) {
	if m == nil || m.GangStartLatencySeconds == nil {
		return
	}
	m.GangStartLatencySeconds.WithLabelValues(queue, gangSize).Observe(seconds)
}

// RecordPartialGangStart records an invariant violation: a gang was partially scheduled without all members.
// In Meridian, this metric must always evaluate to 0. Any positive value triggers an immediate critical alert.
func (m *Metrics) RecordPartialGangStart(queue string) {
	if m == nil || m.GangPartialStartsTotal == nil {
		return
	}
	m.GangPartialStartsTotal.WithLabelValues(queue).Inc()
}

// RecordGangPermitWait records time spent by gang pods waiting in the Permit stage of the scheduler framework.
func (m *Metrics) RecordGangPermitWait(queue string, seconds float64) {
	if m == nil || m.GangPermitWaitSeconds == nil {
		return
	}
	m.GangPermitWaitSeconds.WithLabelValues(queue).Observe(seconds)
}
