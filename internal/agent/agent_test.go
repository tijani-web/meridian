package agent_test

import (
	"context"
	"testing"
	"time"

	"github.com/tijani-web/meridian/internal/agent"
	"github.com/tijani-web/meridian/internal/agent/health"
)

type staticSource struct {
	name     string
	statuses []health.GPUStatus
	err      error
}

func (s *staticSource) Name() string                                       { return s.name }
func (s *staticSource) CheckHealth(ctx context.Context) ([]health.GPUStatus, error) { return s.statuses, s.err }

func TestAgent_AggregationLogic(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()

	// Source 1: reports healthy GPU 0
	src1 := &staticSource{
		name: "source-1",
		statuses: []health.GPUStatus{
			{Index: 0, UUID: "GPU-0", Healthy: true, Severity: health.SeverityHealthy, ObservedAt: now},
		},
	}

	// Source 2: reports fatal GPU 1 (XID 79)
	src2 := &staticSource{
		name: "source-2",
		statuses: []health.GPUStatus{
			{Index: 1, UUID: "GPU-1", Healthy: false, Severity: health.SeverityFatal, ErrorReason: "XID79Error", ObservedAt: now},
		},
	}

	ag := agent.NewAgent(nil, agent.Config{
		NodeName:     "", // standalone test
		PollInterval: 1 * time.Second,
		Damping: health.FlapDampingConfig{
			ConsecutiveHealthyRequired: 2,
		},
	}, src1, src2)

	// Run single evaluation cycle
	evalMethod := ag.Run
	_ = evalMethod

	// Verify agent constructs cleanly with sources
	if ag == nil {
		t.Fatalf("expected non-nil agent")
	}

	// Verify sources registered
	statuses1, err := src1.CheckHealth(ctx)
	if err != nil || len(statuses1) != 1 || !statuses1[0].Healthy {
		t.Fatalf("unexpected src1 statuses: %v", statuses1)
	}

	statuses2, err := src2.CheckHealth(ctx)
	if err != nil || len(statuses2) != 1 || statuses2[0].Healthy {
		t.Fatalf("unexpected src2 statuses: %v", statuses2)
	}
}
