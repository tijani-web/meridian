/*
Package health defines the core abstractions, contracts, and data structures
for the Meridian GPU node health monitoring agent.
*/
package health

import (
	"context"
	"time"
)

const (
	// ConditionGPUHealthy represents the aggregate health observation of all on-node GPUs.
	ConditionGPUHealthy = "meridian.dev/GPUHealthy"

	// TaintGPUUnhealthy is the node taint applied when hardware faults are detected.
	TaintGPUUnhealthy = "meridian.dev/gpu-unhealthy"

	// AnnotationQuarantined flags that a flapping node has been placed in extended quarantine.
	AnnotationQuarantined = "meridian.dev/quarantined-until"
)

// Severity indicates the critical impact of a detected GPU fault.
type Severity string

const (
	// SeverityHealthy indicates normal operating parameters.
	SeverityHealthy Severity = "Healthy"

	// SeverityRecoverable indicates degraded performance (e.g., thermal throttling)
	// that warrants a NoSchedule taint without abruptly evicting running gangs.
	SeverityRecoverable Severity = "Recoverable"

	// SeverityFatal indicates critical hardware failure (e.g., bus drop, uncorrectable ECC)
	// requiring an immediate NoExecute taint and gang preemption/eviction.
	SeverityFatal Severity = "Fatal"
)

// GPUStatus captures the diagnostic observations for a single physical GPU device.
type GPUStatus struct {
	// Index is the zero-based device index (e.g. 0 to 7).
	Index int `json:"index"`

	// UUID is the globally unique hardware identifier (e.g. "GPU-12345678-...").
	UUID string `json:"uuid"`

	// Healthy indicates whether this individual device is functioning correctly.
	Healthy bool `json:"healthy"`

	// Severity classifies the fault impact if Healthy is false.
	Severity Severity `json:"severity"`

	// ErrorReason is a CamelCase reason explaining the fault.
	ErrorReason string `json:"errorReason,omitempty"`

	// ErrorMessage is a human-readable explanation of the fault.
	ErrorMessage string `json:"errorMessage,omitempty"`

	// XID contains the most recently reported NVIDIA driver error code (0 if none).
	XID int `json:"xid,omitempty"`

	// TemperatureCelsius is the current silicon core temperature.
	TemperatureCelsius float64 `json:"temperatureCelsius,omitempty"`

	// ECCUncorrectableErrors is the count of double-bit uncorrectable memory errors.
	ECCUncorrectableErrors int64 `json:"eccUncorrectableErrors,omitempty"`

	// ObservedAt is the timestamp when the measurement was taken.
	ObservedAt time.Time `json:"observedAt"`
}

// HealthSource abstracts telemetry providers (e.g., NVML, DCGM Prometheus scraper).
type HealthSource interface {
	// Name returns the identifier of the telemetry source.
	Name() string

	// CheckHealth polls the underlying hardware or metrics endpoint and returns
	// current health observations for all discovered devices.
	CheckHealth(ctx context.Context) ([]GPUStatus, error)
}

// ClassifyXID determines the operational severity of an NVIDIA driver XID error code.
func ClassifyXID(xid int) Severity {
	switch xid {
	case 31, 43, 45, 61, 62, 63, 64, 74, 79, 92:
		// Page fault, engine hang, internal MCU halt, ECC DBE, NVLink fatal, PCIe bus drop, thermal shutdown
		return SeverityFatal
	case 13, 44, 54, 68:
		// Graphics engine recovery, thermal throttling, NVLink bandwidth degradation
		return SeverityRecoverable
	default:
		// Any unrecognized non-zero XID is treated as recoverable until diagnosed
		if xid > 0 {
			return SeverityRecoverable
		}
		return SeverityHealthy
	}
}
