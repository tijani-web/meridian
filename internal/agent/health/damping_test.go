package health_test

import (
	"testing"
	"time"

	"github.com/tijani-web/meridian/internal/agent/health"
)

func TestClassifyXID(t *testing.T) {
	fatalCases := []int{31, 43, 45, 61, 62, 63, 64, 74, 79, 92}
	for _, xid := range fatalCases {
		if sev := health.ClassifyXID(xid); sev != health.SeverityFatal {
			t.Errorf("expected XID %d to be Fatal, got %s", xid, sev)
		}
	}

	recoverableCases := []int{13, 44, 54, 68}
	for _, xid := range recoverableCases {
		if sev := health.ClassifyXID(xid); sev != health.SeverityRecoverable {
			t.Errorf("expected XID %d to be Recoverable, got %s", xid, sev)
		}
	}

	if sev := health.ClassifyXID(0); sev != health.SeverityHealthy {
		t.Errorf("expected XID 0 to be Healthy, got %s", sev)
	}
}

func TestDampingTracker_HysteresisRecovery(t *testing.T) {
	cfg := health.FlapDampingConfig{
		ConsecutiveHealthyRequired: 3,
		FlapWindow:                 1 * time.Minute,
		MaxFlaps:                   5,
		QuarantineDuration:         5 * time.Minute,
	}
	tracker := health.NewDampingTracker(cfg)
	now := time.Now()

	// 1. Initially healthy
	healthy, quarantined := tracker.RecordEvaluation(true, now)
	if !healthy || quarantined {
		t.Fatalf("expected healthy initial state")
	}

	// 2. Hardware fault occurs -> immediate unhealthy response
	now = now.Add(1 * time.Second)
	healthy, quarantined = tracker.RecordEvaluation(false, now)
	if healthy || quarantined {
		t.Fatalf("expected immediate unhealthy response on fault")
	}

	// 3. Sensor reports healthy once -> still unhealthy due to hysteresis
	now = now.Add(1 * time.Second)
	healthy, _ = tracker.RecordEvaluation(true, now)
	if healthy {
		t.Fatalf("expected unhealthy after only 1 healthy cycle")
	}

	// 4. Sensor reports healthy second time -> still unhealthy
	now = now.Add(1 * time.Second)
	healthy, _ = tracker.RecordEvaluation(true, now)
	if healthy {
		t.Fatalf("expected unhealthy after 2 healthy cycles")
	}

	// 5. Sensor reports healthy third time -> recovered!
	now = now.Add(1 * time.Second)
	healthy, _ = tracker.RecordEvaluation(true, now)
	if !healthy {
		t.Fatalf("expected healthy after 3 consecutive healthy cycles")
	}
}

func TestDampingTracker_FlapQuarantine(t *testing.T) {
	cfg := health.FlapDampingConfig{
		ConsecutiveHealthyRequired: 1, // Quick transition for flap testing
		FlapWindow:                 1 * time.Minute,
		MaxFlaps:                   3,
		QuarantineDuration:         10 * time.Minute,
	}
	tracker := health.NewDampingTracker(cfg)
	now := time.Now()

	// Flap 1: Healthy -> Unhealthy
	_, quarantined := tracker.RecordEvaluation(false, now)
	if quarantined {
		t.Fatalf("unexpected quarantine on flap 1")
	}

	// Recover
	now = now.Add(2 * time.Second)
	tracker.RecordEvaluation(true, now)

	// Flap 2: Healthy -> Unhealthy
	now = now.Add(2 * time.Second)
	_, quarantined = tracker.RecordEvaluation(false, now)
	if quarantined {
		t.Fatalf("unexpected quarantine on flap 2")
	}

	// Recover
	now = now.Add(2 * time.Second)
	tracker.RecordEvaluation(true, now)

	// Flap 3: Healthy -> Unhealthy -> Exceeds MaxFlaps, must quarantine!
	now = now.Add(2 * time.Second)
	healthy, quarantined := tracker.RecordEvaluation(false, now)
	if healthy || !quarantined {
		t.Fatalf("expected node to be quarantined after 3 flaps, got healthy=%t quarantined=%t", healthy, quarantined)
	}

	// Even if hardware reports healthy next second, quarantine holds it unhealthy
	now = now.Add(1 * time.Second)
	healthy, quarantined = tracker.RecordEvaluation(true, now)
	if healthy || !quarantined {
		t.Fatalf("expected node to remain quarantined, got healthy=%t quarantined=%t", healthy, quarantined)
	}

	// Fast forward beyond QuarantineDuration (10 min) -> recovers
	now = now.Add(11 * time.Minute)
	healthy, quarantined = tracker.RecordEvaluation(true, now)
	if !healthy || quarantined {
		t.Fatalf("expected node to clear quarantine after duration, got healthy=%t quarantined=%t", healthy, quarantined)
	}
}
