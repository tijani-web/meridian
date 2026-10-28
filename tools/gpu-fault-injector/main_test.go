package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFaultInjectorEndpoints(t *testing.T) {
	fi := NewFaultInjector(2)

	// 1. Test /health
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	fi.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /health, got %d", w.Code)
	}

	// 2. Test /metrics initially healthy
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	fi.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /metrics, got %d", w.Code)
	}
	body := w.Body.String()
	if !strings.Contains(body, "DCGM_FI_DEV_XID_ERRORS") {
		t.Fatalf("expected DCGM metrics in output")
	}

	// 3. Test /inject fault=xid79
	req = httptest.NewRequest(http.MethodPost, "/inject?gpu=0&fault=xid79", nil)
	w = httptest.NewRecorder()
	fi.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /inject, got %d", w.Code)
	}

	// Verify metric now reports 79
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	fi.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `DCGM_FI_DEV_XID_ERRORS{gpu="0",UUID="GPU-SIMULATED-0000",mode="simulated"} 79`) {
		t.Fatalf("expected XID 79 in metrics, got:\n%s", w.Body.String())
	}

	// 4. Test /clear
	req = httptest.NewRequest(http.MethodPost, "/clear", nil)
	w = httptest.NewRecorder()
	fi.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200 from /clear, got %d", w.Code)
	}

	// Verify metric now reports 0
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	w = httptest.NewRecorder()
	fi.ServeHTTP(w, req)
	if !strings.Contains(w.Body.String(), `DCGM_FI_DEV_XID_ERRORS{gpu="0",UUID="GPU-SIMULATED-0000",mode="simulated"} 0`) {
		t.Fatalf("expected XID 0 after clear, got:\n%s", w.Body.String())
	}
}
