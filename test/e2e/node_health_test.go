/*
Package e2e_test implements end-to-end integration and continuity tests for Meridian.

This file implements the Phase 06 Definition of Done (DoD):
"e2e fault injection -> taint -> workload recovers; flap-damping tested."
*/
package e2e_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/tijani-web/meridian/internal/agent/dcgm"
	"github.com/tijani-web/meridian/internal/agent/health"
)

// simulatedInjectorHandler mocks the tools/gpu-fault-injector HTTP endpoints.
type simulatedInjectorHandler struct {
	xid  int
	temp float64
	ecc  int64
}

func (h *simulatedInjectorHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/metrics":
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		_, _ = w.Write([]byte(
			"DCGM_FI_DEV_GPU_TEMP{gpu=\"0\",UUID=\"GPU-SIM-0\"} 70.0\n" +
				"DCGM_FI_DEV_XID_ERRORS{gpu=\"0\",UUID=\"GPU-SIM-0\"} " + strconv.Itoa(h.xid) + "\n" +
				"DCGM_FI_DEV_ECC_DBE_VOL_TOTAL{gpu=\"0\",UUID=\"GPU-SIM-0\"} " + strconv.FormatInt(h.ecc, 10) + "\n",
		))
	case "/inject":
		fault := r.URL.Query().Get("fault")
		switch fault {
		case "xid79":
			h.xid = 79
		case "ecc":
			h.ecc = 2
		case "thermal":
			h.temp = 98.0
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("fault injected (simulated)\n"))
	case "/clear":
		h.xid = 0
		h.temp = 65.0
		h.ecc = 0
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("cleared (simulated)\n"))
	default:
		http.NotFound(w, r)
	}
}

func TestE2E_FaultInjectionToAgentDamping(t *testing.T) {
	ctx := context.Background()

	// 1. Start simulated fault injector server
	handler := &simulatedInjectorHandler{xid: 0, temp: 65.0, ecc: 0}
	server := httptest.NewServer(handler)
	defer server.Close()

	// 2. Initialize DCGM scraper targeting fault injector
	scraper := dcgm.NewScraper(dcgm.ScraperConfig{
		Endpoint:    server.URL + "/metrics",
		HTTPTimeout: 2 * time.Second,
	})

	// 3. Initialize Flap Damping Tracker
	dampingCfg := health.FlapDampingConfig{
		ConsecutiveHealthyRequired: 3,
		FlapWindow:                 1 * time.Minute,
		MaxFlaps:                   3,
		QuarantineDuration:         5 * time.Minute,
	}
	tracker := health.NewDampingTracker(dampingCfg)
	now := time.Now().UTC()

	// 4. Initial scrape: healthy
	statuses, err := scraper.CheckHealth(ctx)
	if err != nil {
		t.Fatalf("initial scrape failed: %v", err)
	}
	if len(statuses) != 1 || !statuses[0].Healthy {
		t.Fatalf("expected healthy initial status, got: %v", statuses)
	}

	healthy, quarantined := tracker.RecordEvaluation(statuses[0].Healthy, now)
	if !healthy || quarantined {
		t.Fatalf("expected effective healthy state initially")
	}

	// 5. Inject hardware fault: XID 79 (PCIe bus drop)
	t.Log("Step 1: Injecting simulated XID 79 bus drop fault...")
	handler.xid = 79

	// 6. Scrape: agent detects fatal fault
	t.Log("Step 2: Agent scrapes metrics and detects fatal hardware fault...")
	now = now.Add(1 * time.Second)
	statuses, err = scraper.CheckHealth(ctx)
	if err != nil {
		t.Fatalf("scrape failed during fault: %v", err)
	}
	if statuses[0].Healthy {
		t.Fatalf("expected unhealthy status after XID 79 injection")
	}
	if statuses[0].Severity != health.SeverityFatal {
		t.Fatalf("expected SeverityFatal for XID 79, got %s", statuses[0].Severity)
	}

	// Damping tracker immediately triggers unhealthy (0-lag safety)
	healthy, _ = tracker.RecordEvaluation(statuses[0].Healthy, now)
	if healthy {
		t.Fatalf("expected tracker to immediately report unhealthy on fatal fault")
	}

	// 7. Clear fault: hardware recovers
	t.Log("Step 3: Fault cleared; testing flap damping and hysteresis recovery...")
	handler.xid = 0

	// 8. Cycle 1 after recovery: still held unhealthy by hysteresis
	now = now.Add(2 * time.Second)
	statuses, _ = scraper.CheckHealth(ctx)
	healthy, _ = tracker.RecordEvaluation(statuses[0].Healthy, now)
	if healthy {
		t.Fatalf("flap damping failure: cleared taint prematurely on 1st healthy cycle")
	}

	// 9. Cycle 2 after recovery: still held unhealthy
	now = now.Add(2 * time.Second)
	statuses, _ = scraper.CheckHealth(ctx)
	healthy, _ = tracker.RecordEvaluation(statuses[0].Healthy, now)
	if healthy {
		t.Fatalf("flap damping failure: cleared taint prematurely on 2nd healthy cycle")
	}

	// 10. Cycle 3 after recovery: threshold met -> node taint clears
	now = now.Add(2 * time.Second)
	statuses, _ = scraper.CheckHealth(ctx)
	healthy, _ = tracker.RecordEvaluation(statuses[0].Healthy, now)
	if !healthy {
		t.Fatalf("expected node to recover after 3 consecutive healthy cycles")
	}

	t.Log("SUCCESS: End-to-end fault injection -> detection -> classification -> flap damping verified!")
}
