package dcgm_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/tijani-web/meridian/internal/agent/dcgm"
	"github.com/tijani-web/meridian/internal/agent/health"
)

func TestParseMetrics_Healthy(t *testing.T) {
	metrics := `
# HELP DCGM_FI_DEV_GPU_TEMP GPU temperature (in C).
# TYPE DCGM_FI_DEV_GPU_TEMP gauge
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-aaaa-bbbb"} 65
DCGM_FI_DEV_GPU_TEMP{gpu="1",UUID="GPU-cccc-dddd"} 70
# HELP DCGM_FI_DEV_XID_ERRORS Value of last reported XID error.
# TYPE DCGM_FI_DEV_XID_ERRORS gauge
DCGM_FI_DEV_XID_ERRORS{gpu="0",UUID="GPU-aaaa-bbbb"} 0
DCGM_FI_DEV_XID_ERRORS{gpu="1",UUID="GPU-cccc-dddd"} 0
# HELP DCGM_FI_DEV_ECC_DBE_VOL_TOTAL Total volatile double bit errors.
# TYPE DCGM_FI_DEV_ECC_DBE_VOL_TOTAL counter
DCGM_FI_DEV_ECC_DBE_VOL_TOTAL{gpu="0",UUID="GPU-aaaa-bbbb"} 0
DCGM_FI_DEV_ECC_DBE_VOL_TOTAL{gpu="1",UUID="GPU-cccc-dddd"} 0
`

	scraper := dcgm.NewScraper(dcgm.ScraperConfig{})
	statuses, err := scraper.ParseMetrics(strings.NewReader(metrics))
	if err != nil {
		t.Fatalf("ParseMetrics failed: %v", err)
	}

	if len(statuses) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(statuses))
	}

	for _, s := range statuses {
		if !s.Healthy {
			t.Errorf("expected GPU %s to be healthy, got unhealthy", s.UUID)
		}
		if s.Severity != health.SeverityHealthy {
			t.Errorf("expected severity Healthy, got %s", s.Severity)
		}
	}
}

func TestParseMetrics_XIDFatal(t *testing.T) {
	metrics := `
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0"} 65
DCGM_FI_DEV_XID_ERRORS{gpu="0",UUID="GPU-0"} 79
`
	scraper := dcgm.NewScraper(dcgm.ScraperConfig{})
	statuses, err := scraper.ParseMetrics(strings.NewReader(metrics))
	if err != nil {
		t.Fatalf("ParseMetrics failed: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Healthy {
		t.Errorf("expected GPU to be unhealthy due to XID 79")
	}
	if s.Severity != health.SeverityFatal {
		t.Errorf("expected SeverityFatal for XID 79 (bus drop), got %s", s.Severity)
	}
	if s.XID != 79 {
		t.Errorf("expected XID 79, got %d", s.XID)
	}
}

func TestParseMetrics_ECCUncorrectable(t *testing.T) {
	metrics := `
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0"} 65
DCGM_FI_DEV_ECC_DBE_VOL_TOTAL{gpu="0",UUID="GPU-0"} 3
`
	scraper := dcgm.NewScraper(dcgm.ScraperConfig{})
	statuses, err := scraper.ParseMetrics(strings.NewReader(metrics))
	if err != nil {
		t.Fatalf("ParseMetrics failed: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Healthy {
		t.Errorf("expected GPU to be unhealthy due to ECC DBE")
	}
	if s.Severity != health.SeverityFatal {
		t.Errorf("expected SeverityFatal for uncorrectable ECC, got %s", s.Severity)
	}
	if s.ECCUncorrectableErrors != 3 {
		t.Errorf("expected 3 ECC DBEs, got %d", s.ECCUncorrectableErrors)
	}
}

func TestParseMetrics_CriticalTemperature(t *testing.T) {
	metrics := `
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-0"} 98.5
`
	scraper := dcgm.NewScraper(dcgm.ScraperConfig{
		MaxTempCelsius:      88.0,
		CriticalTempCelsius: 95.0,
	})
	statuses, err := scraper.ParseMetrics(strings.NewReader(metrics))
	if err != nil {
		t.Fatalf("ParseMetrics failed: %v", err)
	}

	if len(statuses) != 1 {
		t.Fatalf("expected 1 GPU, got %d", len(statuses))
	}
	s := statuses[0]
	if s.Healthy {
		t.Errorf("expected GPU to be unhealthy due to 98.5C temp")
	}
	if s.Severity != health.SeverityFatal {
		t.Errorf("expected SeverityFatal for >95C, got %s", s.Severity)
	}
}

func TestCheckHealth_HTTPServer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("DCGM_FI_DEV_GPU_TEMP{gpu=\"0\",UUID=\"GPU-0\"} 72\n"))
	}))
	defer server.Close()

	scraper := dcgm.NewScraper(dcgm.ScraperConfig{
		Endpoint:    server.URL,
		HTTPTimeout: 2 * time.Second,
	})

	if scraper.Name() != "dcgm-exporter" {
		t.Errorf("expected source name dcgm-exporter, got %s", scraper.Name())
	}

	statuses, err := scraper.CheckHealth(context.Background())
	if err != nil {
		t.Fatalf("CheckHealth failed: %v", err)
	}
	if len(statuses) != 1 || statuses[0].TemperatureCelsius != 72 {
		t.Errorf("unexpected statuses: %v", statuses)
	}
}
