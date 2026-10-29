/*
Package metrics provides GPU hardware, XID faults, and node recovery metric helpers.
*/
package metrics

// RecordXIDEvent records an NVIDIA GPU driver/hardware error event detected by telemetry.
func (m *Metrics) RecordXIDEvent(node, gpu, xid, severity string) {
	if m == nil || m.GPUXIDEventsTotal == nil {
		return
	}
	m.GPUXIDEventsTotal.WithLabelValues(node, gpu, xid, severity).Inc()
}

// RecordECCDBE records an uncorrectable double-bit ECC memory error on a GPU die.
func (m *Metrics) RecordECCDBE(node, gpu string) {
	if m == nil || m.GPUECCDBETotal == nil {
		return
	}
	m.GPUECCDBETotal.WithLabelValues(node, gpu).Inc()
}

// SetGPUTemperature records the current die temperature of a GPU in Celsius.
func (m *Metrics) SetGPUTemperature(node, gpu string, tempCelsius float64) {
	if m == nil || m.GPUTemperatureCelsius == nil {
		return
	}
	m.GPUTemperatureCelsius.WithLabelValues(node, gpu).Set(tempCelsius)
}

// SetGPUHealthy sets the health gauge: 1 for healthy, 0 for tainted or degraded.
func (m *Metrics) SetGPUHealthy(node, gpu string, healthy bool) {
	if m == nil || m.GPUHealthy == nil {
		return
	}
	val := 0.0
	if healthy {
		val = 1.0
	}
	m.GPUHealthy.WithLabelValues(node, gpu).Set(val)
}

// RecordNodeRecoveryDuration records elapsed time in seconds from fault detection to workload recovery.
func (m *Metrics) RecordNodeRecoveryDuration(node string, seconds float64) {
	if m == nil || m.NodeRecoveryDurationSeconds == nil {
		return
	}
	m.NodeRecoveryDurationSeconds.WithLabelValues(node).Observe(seconds)
}
