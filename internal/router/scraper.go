/*
Package router provides vLLM telemetry scraping conforming to pinned vLLM v0.9.2.
*/
package router

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// MetricScraper periodically scrapes Prometheus endpoints of active inference replicas.
type MetricScraper struct {
	client   *http.Client
	interval time.Duration
	timeout  time.Duration
}

// NewMetricScraper constructs a new MetricScraper with sensible timeouts.
func NewMetricScraper(interval, timeout time.Duration) *MetricScraper {
	return &MetricScraper{
		client: &http.Client{
			Timeout: timeout,
		},
		interval: interval,
		timeout:  timeout,
	}
}

// ParseVLLMMetrics extracts key queue and KV cache indicators from Prometheus exposition text.
func ParseVLLMMetrics(r io.Reader) (waiting int64, running int64, kvUsage float64, err error) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		metricNameAndLabels := parts[0]
		valStr := parts[len(parts)-1]

		if strings.HasPrefix(metricNameAndLabels, "vllm:num_requests_waiting") {
			if v, parseErr := strconv.ParseInt(valStr, 10, 64); parseErr == nil {
				waiting = v
			}
		} else if strings.HasPrefix(metricNameAndLabels, "vllm:num_requests_running") {
			if v, parseErr := strconv.ParseInt(valStr, 10, 64); parseErr == nil {
				running = v
			}
		} else if strings.HasPrefix(metricNameAndLabels, "vllm:gpu_cache_usage_perc") {
			if v, parseErr := strconv.ParseFloat(valStr, 64); parseErr == nil {
				kvUsage = v
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return 0, 0, 0, fmt.Errorf("scanner error while parsing metrics: %w", err)
	}
	return waiting, running, kvUsage, nil
}

// ScrapeReplica fetches and parses metrics from a single replica endpoint.
func (s *MetricScraper) ScrapeReplica(ctx context.Context, replica *ReplicaState) error {
	metricsURL := fmt.Sprintf("%s/metrics", strings.TrimRight(replica.Address, "/"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return fmt.Errorf("failed to create metrics request for %s: %w", replica.ID, err)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("metric scrape request failed for %s: %w", replica.ID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("metric scrape returned non-200 code %d for %s", resp.StatusCode, replica.ID)
	}

	waiting, running, kvUsage, err := ParseVLLMMetrics(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to parse metrics for %s: %w", replica.ID, err)
	}

	replica.mu.Lock()
	replica.WaitingRequests = waiting
	replica.RunningRequests = running
	replica.KVCacheUsage = kvUsage
	replica.LastScraped = time.Now()
	replica.Healthy = true
	replica.mu.Unlock()

	return nil
}

// StartScraping runs background polling across all replicas until context is cancelled.
func (s *MetricScraper) StartScraping(ctx context.Context, getReplicas func() []*ReplicaState) {
	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			replicas := getReplicas()
			for _, rep := range replicas {
				go func(r *ReplicaState) {
					scrapeCtx, cancel := context.WithTimeout(ctx, s.timeout)
					defer cancel()
					_ = s.ScrapeReplica(scrapeCtx, r)
				}(rep)
			}
		}
	}
}
