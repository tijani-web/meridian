/*
Package health implements flap damping and hysteresis filtering for GPU node health.
*/
package health

import (
	"sync"
	"time"
)

// FlapDampingConfig parameterizes the hysteresis and quarantine state machine.
type FlapDampingConfig struct {
	// ConsecutiveHealthyRequired is the number of successive healthy evaluation
	// cycles needed to transition from unhealthy to healthy.
	ConsecutiveHealthyRequired int

	// FlapWindow is the rolling duration in which transition flaps are tracked.
	FlapWindow time.Duration

	// MaxFlaps is the maximum number of healthy-unhealthy state transitions
	// allowed within FlapWindow before a node is quarantined.
	MaxFlaps int

	// QuarantineDuration is the time a node remains tainted after exceeding MaxFlaps.
	QuarantineDuration time.Duration
}

// DefaultFlapDampingConfig returns production-safe hysteresis defaults.
func DefaultFlapDampingConfig() FlapDampingConfig {
	return FlapDampingConfig{
		ConsecutiveHealthyRequired: 3,
		FlapWindow:                 5 * time.Minute,
		MaxFlaps:                   3,
		QuarantineDuration:         15 * time.Minute,
	}
}

// DampingTracker maintains observation history and applies hysteresis and flap quarantine.
type DampingTracker struct {
	mu     sync.Mutex
	config FlapDampingConfig

	currentlyHealthy   bool
	consecutiveHealthy int
	faults             []time.Time
	quarantinedUntil   time.Time
}

// NewDampingTracker creates a DampingTracker initialized in healthy state.
func NewDampingTracker(cfg FlapDampingConfig) *DampingTracker {
	if cfg.ConsecutiveHealthyRequired <= 0 {
		cfg.ConsecutiveHealthyRequired = 3
	}
	if cfg.FlapWindow <= 0 {
		cfg.FlapWindow = 5 * time.Minute
	}
	if cfg.MaxFlaps <= 0 {
		cfg.MaxFlaps = 3
	}
	if cfg.QuarantineDuration <= 0 {
		cfg.QuarantineDuration = 15 * time.Minute
	}

	return &DampingTracker{
		config:           cfg,
		currentlyHealthy: true,
	}
}

// RecordEvaluation updates the observation record and calculates the effective health state.
func (d *DampingTracker) RecordEvaluation(observedHealthy bool, now time.Time) (effectiveHealthy bool, quarantined bool) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// 1. Check active quarantine
	if now.Before(d.quarantinedUntil) {
		return false, true
	}

	// 2. Prune fault events outside sliding window
	cutoff := now.Add(-d.config.FlapWindow)
	validFaults := make([]time.Time, 0, len(d.faults))
	for _, t := range d.faults {
		if t.After(cutoff) {
			validFaults = append(validFaults, t)
		}
	}
	d.faults = validFaults

	// 3. Evaluate state change
	if !observedHealthy {
		d.consecutiveHealthy = 0

		if d.currentlyHealthy {
			// Transitioned healthy -> unhealthy (fault event)
			d.currentlyHealthy = false
			d.faults = append(d.faults, now)

			if len(d.faults) >= d.config.MaxFlaps {
				d.quarantinedUntil = now.Add(d.config.QuarantineDuration)
				return false, true
			}
		}
		return false, false
	}

	// Observed healthy
	d.consecutiveHealthy++

	if !d.currentlyHealthy {
		// Waiting for consecutive healthy streak before clearing taint
		if d.consecutiveHealthy >= d.config.ConsecutiveHealthyRequired {
			d.currentlyHealthy = true
			return true, false
		}
		return false, false
	}

	return true, false
}
