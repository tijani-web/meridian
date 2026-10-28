/*
Package dcgm implements the DCGM exporter Prometheus scraper telemetry source
for the Meridian node health agent.
*/
package dcgm

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tijani-web/meridian/internal/agent/health"
)

const (
	// MetricXIDErrors is the DCGM metric indicating current or last XID error code.
	MetricXIDErrors = "DCGM_FI_DEV_XID_ERRORS"

	// MetricGPUTemp is the DCGM metric indicating current GPU temperature in Celsius.
	MetricGPUTemp = "DCGM_FI_DEV_GPU_TEMP"

	// MetricECCDBETotal is the DCGM metric indicating double-bit uncorrectable ECC errors.
	MetricECCDBETotal = "DCGM_FI_DEV_ECC_DBE_VOL_TOTAL"

	// MetricThermalViolation is the DCGM metric indicating thermal throttling active.
	MetricThermalViolation = "DCGM_FI_DEV_THERMAL_VIOLATION"

	// DefaultMaxTempCelsius is the temperature threshold above which a GPU is flagged degraded.
	DefaultMaxTempCelsius = 88.0

	// DefaultCriticalTempCelsius is the shutdown temperature threshold for fatal shutdown.
	DefaultCriticalTempCelsius = 95.0
)

// ScraperConfig parameterizes the DCGM Prometheus endpoint connection.
type ScraperConfig struct {
	// Endpoint is the full URL to the dcgm-exporter metrics endpoint (e.g. "http://localhost:9400/metrics").
	Endpoint string

	// HTTPTimeout is the request timeout for scraping.
	HTTPTimeout time.Duration

	// MaxTempCelsius triggers recoverable degradation when exceeded.
	MaxTempCelsius float64

	// CriticalTempCelsius triggers fatal shutdown when exceeded.
	CriticalTempCelsius float64
}

// Scraper implements health.HealthSource by querying a dcgm-exporter Prometheus endpoint.
type Scraper struct {
	config ScraperConfig
	client *http.Client
}

// NewScraper constructs a new DCGM Scraper.
func NewScraper(cfg ScraperConfig) *Scraper {
	if cfg.HTTPTimeout <= 0 {
		cfg.HTTPTimeout = 3 * time.Second
	}
	if cfg.MaxTempCelsius <= 0 {
		cfg.MaxTempCelsius = DefaultMaxTempCelsius
	}
	if cfg.CriticalTempCelsius <= 0 {
		cfg.CriticalTempCelsius = DefaultCriticalTempCelsius
	}

	return &Scraper{
		config: cfg,
		client: &http.Client{Timeout: cfg.HTTPTimeout},
	}
}

// Name returns the source identifier.
func (s *Scraper) Name() string {
	return "dcgm-exporter"
}

// CheckHealth queries the configured DCGM exporter endpoint and returns per-GPU health status.
func (s *Scraper) CheckHealth(ctx context.Context) ([]health.GPUStatus, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.config.Endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("create dcgm scrape request: %w", err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dcgm scrape endpoint %s failed: %w", s.config.Endpoint, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("dcgm scrape returned non-200 status: %d", resp.StatusCode)
	}

	return s.ParseMetrics(resp.Body)
}

// ParseMetrics parses Prometheus text exposition format into structured GPUStatus records.
func (s *Scraper) ParseMetrics(r io.Reader) ([]health.GPUStatus, error) {
	scanner := bufio.NewScanner(r)
	now := time.Now().UTC()

	// Map GPU index/UUID to GPUStatus
	deviceMap := make(map[string]*health.GPUStatus)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue // Skip comments and empty lines
		}

		metricName, labels, val, err := parsePromLine(line)
		if err != nil {
			continue
		}

		gpuKey := labels["gpu"]
		if gpuKey == "" {
			gpuKey = labels["device"]
		}
		if gpuKey == "" {
			continue
		}

		status, exists := deviceMap[gpuKey]
		if !exists {
			idx, _ := strconv.Atoi(gpuKey)
			status = &health.GPUStatus{
				Index:      idx,
				UUID:       labels["UUID"],
				Healthy:    true,
				Severity:   health.SeverityHealthy,
				ObservedAt: now,
			}
			deviceMap[gpuKey] = status
		}
		if status.UUID == "" && labels["UUID"] != "" {
			status.UUID = labels["UUID"]
		}

		switch metricName {
		case MetricXIDErrors:
			xid := int(val)
			if xid > 0 {
				status.XID = xid
				sev := health.ClassifyXID(xid)
				if sev != health.SeverityHealthy {
					status.Healthy = false
					status.Severity = sev
					status.ErrorReason = fmt.Sprintf("XID%dError", xid)
					status.ErrorMessage = fmt.Sprintf("NVIDIA driver reported XID error %d", xid)
				}
			}

		case MetricGPUTemp:
			status.TemperatureCelsius = val
			if val >= s.config.CriticalTempCelsius {
				status.Healthy = false
				status.Severity = health.SeverityFatal
				status.ErrorReason = "CriticalThermalShutdown"
				status.ErrorMessage = fmt.Sprintf("GPU temperature %.1fC exceeds critical limit %.1fC", val, s.config.CriticalTempCelsius)
			} else if val >= s.config.MaxTempCelsius && status.Severity != health.SeverityFatal {
				status.Healthy = false
				status.Severity = health.SeverityRecoverable
				status.ErrorReason = "ThermalThrottling"
				status.ErrorMessage = fmt.Sprintf("GPU temperature %.1fC exceeds throttling limit %.1fC", val, s.config.MaxTempCelsius)
			}

		case MetricECCDBETotal:
			dbe := int64(val)
			status.ECCUncorrectableErrors = dbe
			if dbe > 0 {
				status.Healthy = false
				status.Severity = health.SeverityFatal
				status.ErrorReason = "ECCUncorrectableError"
				status.ErrorMessage = fmt.Sprintf("Detected %d uncorrectable double-bit ECC memory errors", dbe)
			}

		case MetricThermalViolation:
			if val > 0 && status.Severity == health.SeverityHealthy {
				status.Healthy = false
				status.Severity = health.SeverityRecoverable
				status.ErrorReason = "ThermalViolation"
				status.ErrorMessage = "Active thermal clock throttling detected"
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read metrics stream: %w", err)
	}

	results := make([]health.GPUStatus, 0, len(deviceMap))
	for _, st := range deviceMap {
		results = append(results, *st)
	}
	return results, nil
}

// parsePromLine parses a single line of Prometheus text format into metric name, labels, and float value.
func parsePromLine(line string) (name string, labels map[string]string, val float64, err error) {
	labels = make(map[string]string)
	braceOpen := strings.IndexByte(line, '{')
	braceClose := strings.IndexByte(line, '}')

	var valStr string
	if braceOpen != -1 && braceClose != -1 && braceClose > braceOpen {
		name = strings.TrimSpace(line[:braceOpen])
		labelSection := line[braceOpen+1 : braceClose]
		valStr = strings.TrimSpace(line[braceClose+1:])

		for _, item := range strings.Split(labelSection, ",") {
			parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
			if len(parts) == 2 {
				k := strings.TrimSpace(parts[0])
				v := strings.Trim(strings.TrimSpace(parts[1]), `"`)
				labels[k] = v
			}
		}
	} else {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			name = fields[0]
			valStr = fields[1]
		} else {
			return "", nil, 0, fmt.Errorf("invalid format")
		}
	}

	val, err = strconv.ParseFloat(valStr, 64)
	if err != nil {
		return "", nil, 0, fmt.Errorf("invalid value %q: %w", valStr, err)
	}

	return name, labels, val, nil
}
