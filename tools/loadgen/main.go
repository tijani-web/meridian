/*
Package main implements tools/loadgen, a high-performance open-loop Poisson arrival
load generator for LLM inference benchmarks (§8.3).

It issues streaming and non-streaming OpenAI chat completions requests across
unique and shared-prefix workload distributions, measuring TTFT, total latency,
tokens per second, and 429 load-shedding rates.
*/
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// WorkloadType specifies the prompt distribution pattern.
type WorkloadType string

const (
	WorkloadUnique       WorkloadType = "unique"
	WorkloadSharedPrefix WorkloadType = "shared-prefix"
)

// RequestResult records the measurement of a single benchmark request.
type RequestResult struct {
	Index        int           `json:"index"`
	StartTime    time.Time     `json:"start_time"`
	StatusCode   int           `json:"status_code"`
	TTFT         time.Duration `json:"ttft_ms"`
	TotalLatency time.Duration `json:"total_latency_ms"`
	Tokens       int           `json:"tokens"`
	IsShed       bool          `json:"is_shed"`
	IsError      bool          `json:"is_error"`
	PrefixShared bool          `json:"prefix_shared"`
	TargetNode   string        `json:"target_node"`
	Strategy     string        `json:"strategy"`
}

// BenchmarkSummary records aggregated statistics.
type BenchmarkSummary struct {
	Strategy          string  `json:"strategy"`
	Workload          string  `json:"workload"`
	TotalRequests     int     `json:"total_requests"`
	Successful        int     `json:"successful_requests"`
	ShedCount         int     `json:"shed_requests"`
	ShedRatePercent   float64 `json:"shed_rate_percent"`
	ErrorCount        int     `json:"error_requests"`
	ThroughputTokensS float64 `json:"throughput_tokens_per_sec"`
	TTFTMeanMs        float64 `json:"ttft_mean_ms"`
	TTFTP50Ms         float64 `json:"ttft_p50_ms"`
	TTFTP90Ms         float64 `json:"ttft_p90_ms"`
	TTFTP99Ms         float64 `json:"ttft_p99_ms"`
	LatencyMeanMs     float64 `json:"latency_mean_ms"`
	LatencyP50Ms      float64 `json:"latency_p50_ms"`
	LatencyP99Ms      float64 `json:"latency_p99_ms"`
}

// Config defines the benchmark configuration.
type Config struct {
	TargetURL    string
	Model        string
	Requests     int
	ArrivalRate  float64 // requests per second (Poisson lambda)
	SharedRatio  float64 // fraction of requests sharing common system prompt (e.g. 0.70)
	Workload     WorkloadType
	BatchRatio   float64 // fraction of batch priority requests (e.g. 0.20)
	MaxTokens    int
	Concurrency  int
	Seed         int64
	OutputCSV    string
	OutputJSON   string
}

const commonSystemPrompt = `You are a production-grade inference assistant operating under Meridian GPU control plane.
You specialize in Kubernetes infrastructure, NVIDIA GPU multi-instance slicing, NVLink topologies,
and low-latency serving optimization. Always provide concise and mathematically grounded answers.`

// GenerateRequestPayload creates a ChatRequest according to configuration and seed.
func GenerateRequestPayload(rng *rand.Rand, cfg *Config, idx int) (payload []byte, isShared bool, isBatch bool) {
	isBatch = rng.Float64() < cfg.BatchRatio
	isShared = false

	var messages []map[string]string

	if cfg.Workload == WorkloadSharedPrefix && rng.Float64() < cfg.SharedRatio {
		isShared = true
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": commonSystemPrompt,
		})
	} else {
		messages = append(messages, map[string]string{
			"role":    "system",
			"content": fmt.Sprintf("Unique prompt session id %d with seed %d.", idx, rng.Int63()),
		})
	}

	messages = append(messages, map[string]string{
		"role":    "user",
		"content": fmt.Sprintf("Evaluate latency for prompt payload index %d.", idx),
	})

	reqMap := map[string]interface{}{
		"model":      cfg.Model,
		"messages":   messages,
		"stream":     true,
		"max_tokens": cfg.MaxTokens,
	}

	payload, _ = json.Marshal(reqMap)
	return payload, isShared, isBatch
}

// ExecuteRequest dispatches a single streaming request and measures TTFT and total latency.
func ExecuteRequest(ctx context.Context, client *http.Client, targetURL string, payload []byte, isBatch bool, isShared bool, idx int) RequestResult {
	result := RequestResult{
		Index:        idx,
		StartTime:    time.Now(),
		PrefixShared: isShared,
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		result.IsError = true
		return result
	}

	req.Header.Set("Content-Type", "application/json")
	if isBatch {
		req.Header.Set("X-Meridian-Priority", "batch")
	} else {
		req.Header.Set("X-Meridian-Priority", "interactive")
	}

	t0 := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		result.IsError = true
		result.TotalLatency = time.Since(t0)
		return result
	}
	defer resp.Body.Close()

	result.StatusCode = resp.StatusCode
	result.TargetNode = resp.Header.Get("X-Meridian-Target")
	result.Strategy = resp.Header.Get("X-Meridian-Strategy")

	if resp.StatusCode == http.StatusTooManyRequests {
		result.IsShed = true
		result.TotalLatency = time.Since(t0)
		return result
	}

	if resp.StatusCode != http.StatusOK {
		result.IsError = true
		result.TotalLatency = time.Since(t0)
		return result
	}

	// Read streaming SSE tokens
	scanner := bufio.NewScanner(resp.Body)
	firstToken := false
	tokenCount := 0

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			if !firstToken {
				result.TTFT = time.Since(t0)
				firstToken = true
			}
			data := strings.TrimPrefix(line, "data: ")
			if data != "[DONE]" {
				tokenCount++
			}
		}
	}

	result.TotalLatency = time.Since(t0)
	result.Tokens = tokenCount
	if !firstToken {
		result.TTFT = result.TotalLatency
	}
	return result
}

// RunBenchmark executes a full loadgen benchmark run.
func RunBenchmark(ctx context.Context, cfg Config) (*BenchmarkSummary, []RequestResult) {
	rng := rand.New(rand.NewSource(cfg.Seed))
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			MaxIdleConns:        500,
			MaxIdleConnsPerHost: 100,
		},
	}

	results := make([]RequestResult, cfg.Requests)
	var wg sync.WaitGroup
	sem := make(chan struct{}, cfg.Concurrency)

	startTime := time.Now()

	for i := 0; i < cfg.Requests; i++ {
		// Poisson arrival interval: -ln(U) / lambda
		u := rng.Float64()
		if u <= 0.0001 {
			u = 0.0001
		}
		interval := time.Duration((-math.Log(u) / cfg.ArrivalRate) * float64(time.Second))

		payload, isShared, isBatch := GenerateRequestPayload(rng, &cfg, i)

		select {
		case <-ctx.Done():
			break
		case <-time.After(interval):
		}

		sem <- struct{}{}
		wg.Add(1)

		go func(idx int, bPayload []byte, bShared, bBatch bool) {
			defer func() {
				<-sem
				wg.Done()
			}()
			res := ExecuteRequest(ctx, client, cfg.TargetURL, bPayload, bBatch, bShared, idx)
			results[idx] = res
		}(i, payload, isShared, isBatch)
	}

	wg.Wait()
	totalDuration := time.Since(startTime)

	// Compute summary statistics
	summary := ComputeSummary(results, totalDuration, string(cfg.Workload))
	return summary, results
}

// ComputeSummary aggregates latency percentiles and throughput.
func ComputeSummary(results []RequestResult, duration time.Duration, workload string) *BenchmarkSummary {
	var ttfts []float64
	var lats []float64
	var totalTokens int
	var successful, shed, errors int
	strategy := ""

	for _, r := range results {
		if r.Strategy != "" {
			strategy = r.Strategy
		}
		if r.IsShed {
			shed++
		} else if r.IsError || r.StatusCode != http.StatusOK {
			errors++
		} else {
			successful++
			totalTokens += r.Tokens
			ttfts = append(ttfts, float64(r.TTFT.Milliseconds()))
			lats = append(lats, float64(r.TotalLatency.Milliseconds()))
		}
	}

	sort.Float64s(ttfts)
	sort.Float64s(lats)

	percentile := func(sorted []float64, p float64) float64 {
		if len(sorted) == 0 {
			return 0
		}
		idx := int(float64(len(sorted)-1) * p)
		return sorted[idx]
	}

	mean := func(vals []float64) float64 {
		if len(vals) == 0 {
			return 0
		}
		var sum float64
		for _, v := range vals {
			sum += v
		}
		return sum / float64(len(vals))
	}

	total := len(results)
	shedRate := 0.0
	if total > 0 {
		shedRate = (float64(shed) / float64(total)) * 100.0
	}

	tokS := 0.0
	if duration.Seconds() > 0 {
		tokS = float64(totalTokens) / duration.Seconds()
	}

	return &BenchmarkSummary{
		Strategy:          strategy,
		Workload:          workload,
		TotalRequests:     total,
		Successful:        successful,
		ShedCount:         shed,
		ShedRatePercent:   shedRate,
		ErrorCount:        errors,
		ThroughputTokensS: tokS,
		TTFTMeanMs:        mean(ttfts),
		TTFTP50Ms:         percentile(ttfts, 0.50),
		TTFTP90Ms:         percentile(ttfts, 0.90),
		TTFTP99Ms:         percentile(ttfts, 0.99),
		LatencyMeanMs:     mean(lats),
		LatencyP50Ms:      percentile(lats, 0.50),
		LatencyP99Ms:      percentile(lats, 0.99),
	}
}

// WriteResultsCSV serializes raw request measurements into a CSV file.
func WriteResultsCSV(filename string, results []RequestResult) error {
	if err := os.MkdirAll(filepath.Dir(filename), 0755); err != nil {
		return err
	}

	f, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	_ = w.Write([]string{"index", "status_code", "ttft_ms", "latency_ms", "tokens", "is_shed", "is_error", "prefix_shared", "target_node", "strategy"})
	for _, r := range results {
		_ = w.Write([]string{
			fmt.Sprintf("%d", r.Index),
			fmt.Sprintf("%d", r.StatusCode),
			fmt.Sprintf("%.2f", float64(r.TTFT.Microseconds())/1000.0),
			fmt.Sprintf("%.2f", float64(r.TotalLatency.Microseconds())/1000.0),
			fmt.Sprintf("%d", r.Tokens),
			fmt.Sprintf("%t", r.IsShed),
			fmt.Sprintf("%t", r.IsError),
			fmt.Sprintf("%t", r.PrefixShared),
			r.TargetNode,
			r.Strategy,
		})
	}
	return nil
}

func main() {
	target := flag.String("target", "http://localhost:8080", "Target Meridian Inference Router URL")
	model := flag.String("model", "llama-3-8b", "Target model name")
	reqCount := flag.Int("requests", 100, "Total requests to generate")
	arrivalRate := flag.Float64("rate", 20.0, "Poisson arrival rate (requests per second)")
	workload := flag.String("workload", "shared-prefix", "Workload type: unique or shared-prefix")
	sharedRatio := flag.Float64("shared-ratio", 0.70, "Fraction of requests sharing common system prompt in shared-prefix workload")
	concurrency := flag.Int("concurrency", 50, "Maximum concurrent worker goroutines")
	seed := flag.Int64("seed", 42, "Random seed for reproducibility")
	outputCSV := flag.String("out-csv", "", "Path to write raw results CSV")
	outputJSON := flag.String("out-json", "", "Path to write summary JSON")
	flag.Parse()

	cfg := Config{
		TargetURL:    *target,
		Model:        *model,
		Requests:     *reqCount,
		ArrivalRate:  *arrivalRate,
		Workload:     WorkloadType(*workload),
		SharedRatio:  *sharedRatio,
		BatchRatio:   0.20,
		MaxTokens:    16,
		Concurrency:  *concurrency,
		Seed:         *seed,
		OutputCSV:    *outputCSV,
		OutputJSON:   *outputJSON,
	}

	log.Printf("[loadgen] Starting benchmark: target=%s, requests=%d, rate=%.1f req/s, workload=%s", cfg.TargetURL, cfg.Requests, cfg.ArrivalRate, cfg.Workload)
	summary, results := RunBenchmark(context.Background(), cfg)

	log.Printf("[loadgen] Complete. Total: %d, Success: %d, Shed: %d (%.1f%%), TTFT p50: %.1fms, TTFT p99: %.1fms, Tokens/s: %.1f",
		summary.TotalRequests, summary.Successful, summary.ShedCount, summary.ShedRatePercent, summary.TTFTP50Ms, summary.TTFTP99Ms, summary.ThroughputTokensS)

	if *outputCSV != "" {
		if err := WriteResultsCSV(*outputCSV, results); err != nil {
			log.Fatalf("failed to write CSV: %v", err)
		}
		log.Printf("[loadgen] Saved raw CSV to %s", *outputCSV)
	}

	if *outputJSON != "" {
		data, _ := json.MarshalIndent(summary, "", "  ")
		if err := os.WriteFile(*outputJSON, data, 0644); err != nil {
			log.Fatalf("failed to write JSON: %v", err)
		}
		log.Printf("[loadgen] Saved summary JSON to %s", *outputJSON)
	}
}
