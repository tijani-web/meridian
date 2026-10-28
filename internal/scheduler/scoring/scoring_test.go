package scoring_test

import (
	"testing"

	"github.com/tijani-web/meridian/internal/scheduler/scoring"
)

func TestFitScore(t *testing.T) {
	cases := []struct {
		name                         string
		capacity, used, requested    int64
		wantMin, wantMax             int64
	}{
		{"perfect fill node A", 8, 7, 1, 99, 100},   // leftover=0 → utilization=100, align=100 → 100
		{"stranded leftover 3", 8, 4, 1, 20, 50},    // leftover=3 → align=50, util=62% → ~31
		{"empty node large alloc", 8, 0, 8, 99, 100}, // leftover=0 → 100
		{"zero capacity", 0, 0, 1, 0, 0},
		{"does not fit", 8, 6, 4, 0, 0},
		{"leftover 4 alignment", 8, 0, 4, 40, 50},   // leftover=4 → align=90, util=50% → 45
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := scoring.FitScore(tc.capacity, tc.used, tc.requested)
			if got < tc.wantMin || got > tc.wantMax {
				t.Errorf("FitScore(%d,%d,%d) = %d, want [%d,%d]",
					tc.capacity, tc.used, tc.requested, got, tc.wantMin, tc.wantMax)
			}
		})
	}
}

func TestHealthScore(t *testing.T) {
	cases := []struct {
		labels map[string]string
		want   int64
	}{
		{map[string]string{"meridian.dev/health-score": "100"}, 100},
		{map[string]string{"meridian.dev/health-score": "50"}, 50},
		{map[string]string{"meridian.dev/health-score": "0"}, 0},
		{map[string]string{}, 100}, // absent label = assume healthy
		{map[string]string{"meridian.dev/health-score": "invalid"}, 100},
		{map[string]string{"meridian.dev/health-score": "-5"}, 100},
		{map[string]string{"meridian.dev/health-score": "150"}, 100},
	}
	for _, tc := range cases {
		got := scoring.HealthScore(tc.labels)
		if got != tc.want {
			t.Errorf("HealthScore(%v) = %d, want %d", tc.labels, got, tc.want)
		}
	}
}

func TestComposite(t *testing.T) {
	cfg := scoring.DefaultConfig() // 40/45/15

	// Scenario from docs/design/scoring.md §4 Scenario 2: Node 1 (all 100s)
	got := scoring.Composite(100, 100, 100, cfg)
	if got != 100 {
		t.Errorf("all-100s composite = %d, want 100", got)
	}

	// Node 2: topo=70, rest 100
	got = scoring.Composite(100, 70, 100, cfg)
	// (40*100 + 45*70 + 15*100) / 100 = (4000+3150+1500)/100 = 86
	if got < 85 || got > 87 {
		t.Errorf("node-2 composite = %d, want ~86", got)
	}

	// Node 3: topo=30, rest 100
	got = scoring.Composite(100, 30, 100, cfg)
	// (4000+1350+1500)/100 = 68
	if got < 67 || got > 69 {
		t.Errorf("node-3 composite = %d, want ~68", got)
	}

	// Zero weight clamp
	got = scoring.Composite(50, 50, 50, scoring.Config{})
	if got != 0 {
		t.Errorf("zero-weight composite = %d, want 0", got)
	}
}
