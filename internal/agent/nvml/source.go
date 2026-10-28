/*
Package nvml implements the NVML hardware telemetry health source for the
Meridian GPU node health agent.

It interfaces with NVIDIA Management Library (NVML) on GPU worker nodes to inspect
physical GPU device temperatures, ECC memory error counters, and PCIe bus status.
*/
package nvml

import (
	"context"
	"fmt"
	"time"

	"github.com/tijani-web/meridian/internal/agent/health"
)

// NVMLDevice abstracts device-level NVML calls for direct hardware access.
type NVMLDevice interface {
	GetUUID() (string, error)
	GetTemperature() (uint32, error)
	GetTotalEccErrors() (uint64, error)
	GetLastXID() (uint64, error)
	IsFellOffBus() bool
}

// NVMLClient abstracts the subsystem lifecycle and device enumeration.
type NVMLClient interface {
	Init() error
	Shutdown() error
	DeviceGetCount() (int, error)
	DeviceGetHandleByIndex(index int) (NVMLDevice, error)
}

// SourceConfig configures thresholds for the NVML health source.
type SourceConfig struct {
	MaxTempCelsius      float64
	CriticalTempCelsius float64
}

// Source implements health.HealthSource by querying local GPU hardware via NVML.
type Source struct {
	config SourceConfig
	client NVMLClient
}

// NewSource creates an NVML health source with the specified NVML client.
func NewSource(client NVMLClient, cfg SourceConfig) *Source {
	if cfg.MaxTempCelsius <= 0 {
		cfg.MaxTempCelsius = 88.0
	}
	if cfg.CriticalTempCelsius <= 0 {
		cfg.CriticalTempCelsius = 95.0
	}
	return &Source{
		config: cfg,
		client: client,
	}
}

// Name returns the identifier of this health source.
func (s *Source) Name() string {
	return "nvml"
}

// CheckHealth queries all NVML devices and classifies hardware health status.
func (s *Source) CheckHealth(ctx context.Context) ([]health.GPUStatus, error) {
	if s.client == nil {
		return nil, fmt.Errorf("nvml: client uninitialized or library not found on host")
	}

	count, err := s.client.DeviceGetCount()
	if err != nil {
		return nil, fmt.Errorf("nvml: failed to get device count: %w", err)
	}

	if count == 0 {
		return nil, fmt.Errorf("nvml: 0 GPU devices discovered")
	}

	now := time.Now().UTC()
	statuses := make([]health.GPUStatus, 0, count)

	for i := 0; i < count; i++ {
		dev, err := s.client.DeviceGetHandleByIndex(i)
		if err != nil {
			statuses = append(statuses, health.GPUStatus{
				Index:        i,
				Healthy:      false,
				Severity:     health.SeverityFatal,
				ErrorReason:  "DeviceHandleUnavailable",
				ErrorMessage: fmt.Sprintf("Failed to get NVML handle for GPU index %d: %v", i, err),
				ObservedAt:   now,
			})
			continue
		}

		if dev.IsFellOffBus() {
			statuses = append(statuses, health.GPUStatus{
				Index:        i,
				Healthy:      false,
				Severity:     health.SeverityFatal,
				ErrorReason:  "GPUFellOffBus",
				ErrorMessage: fmt.Sprintf("GPU %d fell off the PCIe bus (XID 79)", i),
				XID:          79,
				ObservedAt:   now,
			})
			continue
		}

		uuid, err := dev.GetUUID()
		if err != nil {
			uuid = fmt.Sprintf("GPU-INDEX-%d", i)
		}

		st := health.GPUStatus{
			Index:      i,
			UUID:       uuid,
			Healthy:    true,
			Severity:   health.SeverityHealthy,
			ObservedAt: now,
		}

		// 1. Check Temperature
		temp, err := dev.GetTemperature()
		if err == nil {
			st.TemperatureCelsius = float64(temp)
			if st.TemperatureCelsius >= s.config.CriticalTempCelsius {
				st.Healthy = false
				st.Severity = health.SeverityFatal
				st.ErrorReason = "CriticalThermalShutdown"
				st.ErrorMessage = fmt.Sprintf("GPU temperature %.1fC exceeds critical limit %.1fC", st.TemperatureCelsius, s.config.CriticalTempCelsius)
			} else if st.TemperatureCelsius >= s.config.MaxTempCelsius {
				st.Healthy = false
				st.Severity = health.SeverityRecoverable
				st.ErrorReason = "ThermalThrottling"
				st.ErrorMessage = fmt.Sprintf("GPU temperature %.1fC exceeds throttling limit %.1fC", st.TemperatureCelsius, s.config.MaxTempCelsius)
			}
		}

		// 2. Check ECC Memory DBEs
		ecc, err := dev.GetTotalEccErrors()
		if err == nil {
			st.ECCUncorrectableErrors = int64(ecc)
			if ecc > 0 {
				st.Healthy = false
				st.Severity = health.SeverityFatal
				st.ErrorReason = "ECCUncorrectableError"
				st.ErrorMessage = fmt.Sprintf("Detected %d uncorrectable double-bit ECC errors", ecc)
			}
		}

		// 3. Check XID Errors
		xid, err := dev.GetLastXID()
		if err == nil && xid > 0 {
			st.XID = int(xid)
			sev := health.ClassifyXID(int(xid))
			if sev != health.SeverityHealthy {
				st.Healthy = false
				st.Severity = sev
				st.ErrorReason = fmt.Sprintf("XID%dError", xid)
				st.ErrorMessage = fmt.Sprintf("NVIDIA driver reported XID %d", xid)
			}
		}

		statuses = append(statuses, st)
	}

	return statuses, nil
}
