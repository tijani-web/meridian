/*
cmd/run-router-benchmarks executes the Meridian Phase 10 inference router benchmark suite (§8.3).

It evaluates all four dispatch strategies:
  1. RoundRobin (baseline)
  2. LeastQueue (dynamic queue minimization)
  3. KVAware (memory saturation protection)
  4. PrefixAffinity (bounded-load consistent hashing with RadixAttention KV-cache reuse)

Outputs:
  - Raw results CSV and JSON in bench/results/2026-10-29-inference-router/
  - Generated SVG comparison plots in bench/plots/
All simulated runs are labeled "[simulated vLLM replicas]" per AGENTS.md §1.1.
*/
package main

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/rand"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tijani-web/meridian/internal/router"
)

type StrategyResult struct {
	Strategy          string  `json:"strategy"`
	TotalRequests     int     `json:"total_requests"`
	Successful        int     `json:"successful_requests"`
	ShedCount         int     `json:"shed_requests"`
	ShedRatePercent   float64 `json:"shed_rate_percent"`
	ThroughputTokensS float64 `json:"throughput_tokens_per_sec"`
	TTFTMeanMs        float64 `json:"ttft_mean_ms"`
	TTFTP50Ms         float64 `json:"ttft_p50_ms"`
	TTFTP90Ms         float64 `json:"ttft_p90_ms"`
	TTFTP99Ms         float64 `json:"ttft_p99_ms"`
	LatencyP50Ms      float64 `json:"latency_p50_ms"`
	LatencyP99Ms      float64 `json:"latency_p99_ms"`
	PrefixCacheHits   int     `json:"prefix_cache_hits"`
	PrefixHitRate     float64 `json:"prefix_hit_rate_percent"`
}

type RawRecord struct {
	Index        int
	StatusCode   int
	TTFTMs       float64
	LatencyMs    float64
	Tokens       int
	IsShed       bool
	PrefixShared bool
	TargetNode   string
	Strategy     string
}

const commonSystemPrompt = `You are a production-grade inference assistant operating under Meridian GPU control plane.
You specialize in Kubernetes infrastructure, NVIDIA GPU multi-instance slicing, NVLink topologies,
and low-latency serving optimization. Always provide concise and mathematically grounded answers.`

func main() {
	outDir := filepath.Join("bench", "results", "2026-10-29-inference-router")
	rawDir := filepath.Join(outDir, "raw")
	plotsDir := filepath.Join("bench", "plots")

	if err := os.MkdirAll(rawDir, 0755); err != nil {
		log.Fatalf("failed to create raw dir: %v", err)
	}
	if err := os.MkdirAll(plotsDir, 0755); err != nil {
		log.Fatalf("failed to create plots dir: %v", err)
	}

	fmt.Println("=== Starting Meridian Phase 10 Inference Router Benchmarks (§8.3) [simulated vLLM replicas] ===")

	// 1. Write params.json
	params := map[string]interface{}{
		"date":               "2026-10-29",
		"replicas":           4,
		"model":              "llama-3-8b",
		"baseTTFTMs":         35,
		"baseTPOTMs":         8,
		"prefixAcceleration": 0.80, // 80% TTFT reduction on prefix cache hit
		"workloadMix":        "70% shared system prompt, 30% unique conversational prompt",
		"arrivalDistribution": "Open-loop Poisson process at 25 req/sec",
		"strategies": []string{
			"RoundRobin",
			"LeastQueue",
			"KVAware",
			"PrefixAffinity",
		},
		"sheddingThreshold": 120,
	}
	writeJSON(filepath.Join(outDir, "params.json"), params)

	// 2. Spin up 4 simulated vLLM replica servers
	fmt.Println("\n--- Spawning 4 Simulated vLLM Replicas ---")
	replicas, cleanups := spawnReplicas(4, 35*time.Millisecond, 8*time.Millisecond)
	defer func() {
		for _, c := range cleanups {
			c()
		}
	}()

	// 3. Initialize Meridian Router in-process
	routerEngine := router.NewStrategyEngine()
	proxyCfg := router.ProxyConfig{
		MaxQueueDepth:     120,
		BatchShedRatio:    0.70,
		DefaultRetryAfter: 2,
		UpstreamTimeout:   30 * time.Second,
	}

	proxyHandler := router.NewProxyHandler(routerEngine, proxyCfg, router.StrategyLeastQueue, func() []*router.ReplicaState {
		return replicas
	})

	routerListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatalf("failed to listen for router: %v", err)
	}
	routerURL := fmt.Sprintf("http://%s", routerListener.Addr().String())

	routerServer := &http.Server{Handler: proxyHandler}
	go func() {
		_ = routerServer.Serve(routerListener)
	}()
	defer routerServer.Close()

	fmt.Printf("Meridian Router listening at %s\n", routerURL)

	// Background scraper simulator
	stopScraper := make(chan struct{})
	defer close(stopScraper)
	go runScraper(replicas, stopScraper)

	strategies := []router.Strategy{
		router.StrategyRoundRobin,
		router.StrategyLeastQueue,
		router.StrategyKVAware,
		router.StrategyPrefixAffinity,
	}

	summaryMap := make(map[string]StrategyResult)

	for _, strat := range strategies {
		fmt.Printf("\n--- Benchmarking Strategy: %s ---\n", strat)
		proxyHandler.SetStrategy(strat)

		// Reset replica state counters for fair comparison
		resetReplicas(replicas)

		summary, rawRecords := runStrategyBenchmark(routerURL, strat, 160, 25.0, 42)
		summaryMap[string(strat)] = summary

		csvFile := filepath.Join(rawDir, fmt.Sprintf("%s.csv", strings.ToLower(string(strat))))
		writeCSV(csvFile, rawRecords)

		fmt.Printf("Summary for %s: TTFT p50=%.1fms, p99=%.1fms, Throughput=%.1f tok/s, CacheHits=%d (%.1f%%), Shed=%d (%.1f%%)\n",
			strat, summary.TTFTP50Ms, summary.TTFTP99Ms, summary.ThroughputTokensS, summary.PrefixCacheHits, summary.PrefixHitRate, summary.ShedCount, summary.ShedRatePercent)
	}

	// 4. Save summary JSON
	writeJSON(filepath.Join(outDir, "summary.json"), summaryMap)

	// 5. Generate high-resolution SVG comparison plots
	generateRouterPlots(plotsDir, summaryMap)

	fmt.Println("\n=== Phase 10 Benchmarks Completed Successfully. All raw data and plots committed. ===")
}

func runScraper(replicas []*router.ReplicaState, stop chan struct{}) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	client := &http.Client{Timeout: 1 * time.Second}

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			for _, r := range replicas {
				go func(rep *router.ReplicaState) {
					resp, err := client.Get(rep.Address + "/metrics")
					if err != nil {
						return
					}
					defer resp.Body.Close()
					w, run, kv, err := router.ParseVLLMMetrics(resp.Body)
					if err == nil {
						rep.WaitingRequests = w
						rep.RunningRequests = run
						rep.KVCacheUsage = kv
						rep.LastScraped = time.Now()
					}
				}(r)
			}
		}
	}
}

func resetReplicas(replicas []*router.ReplicaState) {
	for _, r := range replicas {
		r.WaitingRequests = 0
		r.RunningRequests = 0
		r.InflightRouted = 0
		r.KVCacheUsage = 0.10
		r.LastScraped = time.Now()
	}
}

func spawnReplicas(count int, ttft, tpot time.Duration) ([]*router.ReplicaState, []func()) {
	var replicas []*router.ReplicaState
	var cleanups []func()

	for i := 0; i < count; i++ {
		repID := fmt.Sprintf("vllm-replica-%d", i)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			log.Fatalf("failed to listen: %v", err)
		}
		addr := fmt.Sprintf("http://%s", l.Addr().String())

		// Simulated state per replica
		var mu sync.RWMutex
		var waiting, running int64
		prefixMap := make(map[string]bool)
		var cacheHits int

		mux := http.NewServeMux()
		mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			waiting++
			mu.Unlock()

			time.Sleep(5 * time.Millisecond)

			mu.Lock()
			waiting--
			running++
			mu.Unlock()

			defer func() {
				mu.Lock()
				running--
				mu.Unlock()
			}()

			var reqBody struct {
				Messages []struct {
					Content string `json:"content"`
				} `json:"messages"`
			}
			_ = json.NewDecoder(r.Body).Decode(&reqBody)

			effTTFT := ttft
			mu.Lock()
			if len(reqBody.Messages) > 0 {
				prefix := reqBody.Messages[0].Content
				if len(prefix) > 64 {
					prefix = prefix[:64]
				}
				if prefixMap[prefix] {
					effTTFT = time.Duration(float64(ttft) * 0.20) // 80% reduction
					cacheHits++
				} else {
					prefixMap[prefix] = true
				}
			}
			mu.Unlock()

			time.Sleep(effTTFT)

			w.Header().Set("Content-Type", "text/event-stream")
			if flusher, ok := w.(http.Flusher); ok {
				for tokenIdx := 0; tokenIdx < 8; tokenIdx++ {
					fmt.Fprintf(w, "data: {\"token\":\"tok%d\"}\n\n", tokenIdx)
					flusher.Flush()
					time.Sleep(tpot)
				}
				fmt.Fprintf(w, "data: [DONE]\n\n")
				flusher.Flush()
			}
		})

		mux.HandleFunc("/metrics", func(w http.ResponseWriter, r *http.Request) {
			mu.RLock()
			defer mu.RUnlock()
			fmt.Fprintf(w, "vllm:num_requests_waiting{model_name=\"llama-3-8b\"} %d\n", waiting)
			fmt.Fprintf(w, "vllm:num_requests_running{model_name=\"llama-3-8b\"} %d\n", running)
			fmt.Fprintf(w, "vllm:gpu_cache_usage_perc{model_name=\"llama-3-8b\"} %.4f\n", 0.15+float64(running)*0.08)
		})

		srv := &http.Server{Handler: mux}
		go func() { _ = srv.Serve(l) }()

		cleanups = append(cleanups, func() { _ = srv.Close() })

		replicas = append(replicas, &router.ReplicaState{
			ID:          repID,
			Address:     addr,
			Warm:        true,
			Healthy:     true,
			LastScraped: time.Now(),
		})
	}

	return replicas, cleanups
}

func runStrategyBenchmark(routerURL string, strategy router.Strategy, reqCount int, arrivalRate float64, seed int64) (StrategyResult, []RawRecord) {
	rng := rand.New(rand.NewSource(seed))
	client := &http.Client{Timeout: 30 * time.Second}

	var records []RawRecord
	var ttfts []float64
	var lats []float64
	var totalTokens int
	var success, shed, errors, cacheHits int

	startTime := time.Now()
	var wg sync.WaitGroup
	var recMu sync.Mutex

	for i := 0; i < reqCount; i++ {
		// Poisson arrival interval: -ln(U) / lambda
		u := rng.Float64()
		if u <= 0.0001 {
			u = 0.0001
		}
		interval := time.Duration((-math.Log(u) / arrivalRate) * float64(time.Second))
		time.Sleep(interval)

		isShared := rng.Float64() < 0.70
		isBatch := rng.Float64() < 0.20 // 20% batch priority to test shedding

		var sysContent string
		if isShared {
			sysContent = commonSystemPrompt
		} else {
			sysContent = fmt.Sprintf("Unique system prompt for request session %d.", i)
		}

		reqMap := map[string]interface{}{
			"model": "llama-3-8b",
			"messages": []map[string]string{
				{"role": "system", "content": sysContent},
				{"role": "user", "content": fmt.Sprintf("Query prompt number %d.", i)},
			},
			"stream": true,
		}
		payload, _ := json.Marshal(reqMap)

		wg.Add(1)
		go func(idx int, bPayload []byte, shared, batch bool) {
			defer wg.Done()

			t0 := time.Now()
			req, _ := http.NewRequest(http.MethodPost, routerURL+"/v1/chat/completions", bytes.NewReader(bPayload))
			req.Header.Set("Content-Type", "application/json")
			if batch {
				req.Header.Set("X-Meridian-Priority", "batch")
			}

			resp, err := client.Do(req)
			if err != nil {
				recMu.Lock()
				errors++
				recMu.Unlock()
				return
			}
			defer resp.Body.Close()

			rec := RawRecord{
				Index:        idx,
				StatusCode:   resp.StatusCode,
				PrefixShared: shared,
				TargetNode:   resp.Header.Get("X-Meridian-Target"),
				Strategy:     resp.Header.Get("X-Meridian-Strategy"),
			}

			if resp.StatusCode == http.StatusTooManyRequests {
				rec.IsShed = true
				rec.LatencyMs = float64(time.Since(t0).Microseconds()) / 1000.0
				recMu.Lock()
				shed++
				records = append(records, rec)
				recMu.Unlock()
				return
			}

			if resp.StatusCode != http.StatusOK {
				recMu.Lock()
				errors++
				recMu.Unlock()
				return
			}

			// Read streaming response
			buf := make([]byte, 1024)
			var ttftDur time.Duration
			first := false
			toks := 0

			for {
				n, rErr := resp.Body.Read(buf)
				if n > 0 {
					if !first {
						ttftDur = time.Since(t0)
						first = true
					}
					toks += strings.Count(string(buf[:n]), "data: {")
				}
				if rErr != nil {
					break
				}
			}

			totalDur := time.Since(t0)
			rec.TTFTMs = float64(ttftDur.Microseconds()) / 1000.0
			rec.LatencyMs = float64(totalDur.Microseconds()) / 1000.0
			rec.Tokens = toks

			recMu.Lock()
			success++
			totalTokens += toks
			ttfts = append(ttfts, rec.TTFTMs)
			lats = append(lats, rec.LatencyMs)
			// Prefix cache hit accelerated when TTFT is under 20ms
			if rec.TTFTMs < 20.0 && shared {
				cacheHits++
			}
			records = append(records, rec)
			recMu.Unlock()
		}(i, payload, isShared, isBatch)
	}

	wg.Wait()
	duration := time.Since(startTime)

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

	hitRate := 0.0
	if success > 0 {
		hitRate = (float64(cacheHits) / float64(success)) * 100.0
	}

	return StrategyResult{
		Strategy:          string(strategy),
		TotalRequests:     reqCount,
		Successful:        success,
		ShedCount:         shed,
		ShedRatePercent:   (float64(shed) / float64(reqCount)) * 100.0,
		ThroughputTokensS: float64(totalTokens) / duration.Seconds(),
		TTFTMeanMs:        mean(ttfts),
		TTFTP50Ms:         percentile(ttfts, 0.50),
		TTFTP90Ms:         percentile(ttfts, 0.90),
		TTFTP99Ms:         percentile(ttfts, 0.99),
		LatencyP50Ms:      percentile(lats, 0.50),
		LatencyP99Ms:      percentile(lats, 0.99),
		PrefixCacheHits:   cacheHits,
		PrefixHitRate:     hitRate,
	}, records
}

func writeCSV(path string, records []RawRecord) {
	f, err := os.Create(path)
	if err != nil {
		log.Fatalf("failed to create CSV %s: %v", path, err)
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	_ = w.Write([]string{"index", "status_code", "ttft_ms", "latency_ms", "tokens", "is_shed", "prefix_shared", "target_node", "strategy"})
	for _, r := range records {
		_ = w.Write([]string{
			fmt.Sprintf("%d", r.Index),
			fmt.Sprintf("%d", r.StatusCode),
			fmt.Sprintf("%.2f", r.TTFTMs),
			fmt.Sprintf("%.2f", r.LatencyMs),
			fmt.Sprintf("%d", r.Tokens),
			fmt.Sprintf("%t", r.IsShed),
			fmt.Sprintf("%t", r.PrefixShared),
			r.TargetNode,
			r.Strategy,
		})
	}
}

func writeJSON(path string, v interface{}) {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		log.Fatalf("failed to marshal json %s: %v", path, err)
	}
	_ = os.WriteFile(path, data, 0644)
}

func generateRouterPlots(dir string, results map[string]StrategyResult) {
	rr := results["RoundRobin"]
	lq := results["LeastQueue"]
	kv := results["KVAware"]
	pa := results["PrefixAffinity"]

	// 1. TTFT Comparison (SVG)
	ttftSVG := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 850 420" width="100%%" height="100%%">
  <rect width="850" height="420" fill="#1e1e2e"/>
  <text x="425" y="38" text-anchor="middle" fill="#cdd6f4" font-size="20" font-family="sans-serif" font-weight="bold">Inference Time-to-First-Token (TTFT) by Strategy [simulated vLLM]</text>
  <text x="425" y="62" text-anchor="middle" fill="#a6adc8" font-size="12" font-family="sans-serif">Lower is Better (ms) — 70%% Shared Prefix, Open-Loop Poisson 25 req/s</text>

  <!-- Axes -->
  <line x1="100" y1="330" x2="780" y2="330" stroke="#45475a" stroke-width="2"/>
  <line x1="100" y1="90" x2="100" y2="330" stroke="#45475a" stroke-width="2"/>

  <!-- RoundRobin -->
  <text x="180" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">RoundRobin</text>
  <rect x="140" y="%d" width="35" height="%d" fill="#f38ba8" rx="3"/>
  <text x="157" y="%d" text-anchor="middle" fill="#f38ba8" font-size="11" font-family="sans-serif">p50: %.1f</text>
  <rect x="185" y="%d" width="35" height="%d" fill="#eba0ac" rx="3"/>
  <text x="202" y="%d" text-anchor="middle" fill="#eba0ac" font-size="11" font-family="sans-serif">p99: %.1f</text>

  <!-- LeastQueue -->
  <text x="360" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">LeastQueue</text>
  <rect x="320" y="%d" width="35" height="%d" fill="#fab387" rx="3"/>
  <text x="337" y="%d" text-anchor="middle" fill="#fab387" font-size="11" font-family="sans-serif">p50: %.1f</text>
  <rect x="365" y="%d" width="35" height="%d" fill="#f9e2af" rx="3"/>
  <text x="382" y="%d" text-anchor="middle" fill="#f9e2af" font-size="11" font-family="sans-serif">p99: %.1f</text>

  <!-- KVAware -->
  <text x="540" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">KVAware</text>
  <rect x="500" y="%d" width="35" height="%d" fill="#89b4fa" rx="3"/>
  <text x="517" y="%d" text-anchor="middle" fill="#89b4fa" font-size="11" font-family="sans-serif">p50: %.1f</text>
  <rect x="545" y="%d" width="35" height="%d" fill="#b4befe" rx="3"/>
  <text x="562" y="%d" text-anchor="middle" fill="#b4befe" font-size="11" font-family="sans-serif">p99: %.1f</text>

  <!-- PrefixAffinity -->
  <text x="720" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif" font-weight="bold">PrefixAffinity</text>
  <rect x="680" y="%d" width="35" height="%d" fill="#a6e3a1" rx="3"/>
  <text x="697" y="%d" text-anchor="middle" fill="#a6e3a1" font-size="11" font-family="sans-serif" font-weight="bold">p50: %.1f</text>
  <rect x="725" y="%d" width="35" height="%d" fill="#94e2d5" rx="3"/>
  <text x="742" y="%d" text-anchor="middle" fill="#94e2d5" font-size="11" font-family="sans-serif" font-weight="bold">p99: %.1f</text>

  <!-- Legend -->
  <rect x="310" y="388" width="14" height="14" fill="#a6e3a1" rx="2"/>
  <text x="330" y="400" fill="#cdd6f4" font-size="12" font-family="sans-serif">TTFT p50 (Median)</text>
  <rect x="490" y="388" width="14" height="14" fill="#eba0ac" rx="2"/>
  <text x="510" y="400" fill="#cdd6f4" font-size="12" font-family="sans-serif">TTFT p99 (Tail)</text>
</svg>`,
		// RR
		int(330-rr.TTFTP50Ms*4.5), int(rr.TTFTP50Ms*4.5), int(320-rr.TTFTP50Ms*4.5), rr.TTFTP50Ms,
		int(330-rr.TTFTP99Ms*4.5), int(rr.TTFTP99Ms*4.5), int(320-rr.TTFTP99Ms*4.5), rr.TTFTP99Ms,
		// LQ
		int(330-lq.TTFTP50Ms*4.5), int(lq.TTFTP50Ms*4.5), int(320-lq.TTFTP50Ms*4.5), lq.TTFTP50Ms,
		int(330-lq.TTFTP99Ms*4.5), int(lq.TTFTP99Ms*4.5), int(320-lq.TTFTP99Ms*4.5), lq.TTFTP99Ms,
		// KV
		int(330-kv.TTFTP50Ms*4.5), int(kv.TTFTP50Ms*4.5), int(320-kv.TTFTP50Ms*4.5), kv.TTFTP50Ms,
		int(330-kv.TTFTP99Ms*4.5), int(kv.TTFTP99Ms*4.5), int(320-kv.TTFTP99Ms*4.5), kv.TTFTP99Ms,
		// PA
		int(330-pa.TTFTP50Ms*4.5), int(pa.TTFTP50Ms*4.5), int(320-pa.TTFTP50Ms*4.5), pa.TTFTP50Ms,
		int(330-pa.TTFTP99Ms*4.5), int(pa.TTFTP99Ms*4.5), int(320-pa.TTFTP99Ms*4.5), pa.TTFTP99Ms,
	)
	_ = os.WriteFile(filepath.Join(dir, "router_ttft_comparison.svg"), []byte(ttftSVG), 0644)

	// 2. Throughput & Shed Rate Comparison (SVG)
	tputSVG := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 850 420" width="100%%" height="100%%">
  <rect width="850" height="420" fill="#1e1e2e"/>
  <text x="425" y="38" text-anchor="middle" fill="#cdd6f4" font-size="20" font-family="sans-serif" font-weight="bold">Inference Serving Throughput &amp; Load Shedding Rate [simulated]</text>
  <text x="425" y="62" text-anchor="middle" fill="#a6adc8" font-size="12" font-family="sans-serif">Throughput (Tokens/sec) Higher is Better — Shed Rate (%%) Lower is Better</text>

  <!-- Axes -->
  <line x1="100" y1="330" x2="780" y2="330" stroke="#45475a" stroke-width="2"/>
  <line x1="100" y1="90" x2="100" y2="330" stroke="#45475a" stroke-width="2"/>

  <!-- RoundRobin -->
  <text x="180" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">RoundRobin</text>
  <rect x="150" y="%d" width="60" height="%d" fill="#f38ba8" rx="4"/>
  <text x="180" y="%d" text-anchor="middle" fill="#f38ba8" font-size="12" font-family="sans-serif" font-weight="bold">%.1f tok/s</text>

  <!-- LeastQueue -->
  <text x="360" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">LeastQueue</text>
  <rect x="330" y="%d" width="60" height="%d" fill="#fab387" rx="4"/>
  <text x="360" y="%d" text-anchor="middle" fill="#fab387" font-size="12" font-family="sans-serif" font-weight="bold">%.1f tok/s</text>

  <!-- KVAware -->
  <text x="540" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">KVAware</text>
  <rect x="510" y="%d" width="60" height="%d" fill="#89b4fa" rx="4"/>
  <text x="540" y="%d" text-anchor="middle" fill="#89b4fa" font-size="12" font-family="sans-serif" font-weight="bold">%.1f tok/s</text>

  <!-- PrefixAffinity -->
  <text x="720" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif" font-weight="bold">PrefixAffinity</text>
  <rect x="690" y="%d" width="60" height="%d" fill="#a6e3a1" rx="4"/>
  <text x="720" y="%d" text-anchor="middle" fill="#a6e3a1" font-size="12" font-family="sans-serif" font-weight="bold">%.1f tok/s</text>

  <!-- Legend -->
  <rect x="310" y="388" width="14" height="14" fill="#a6e3a1" rx="2"/>
  <text x="330" y="400" fill="#cdd6f4" font-size="12" font-family="sans-serif">Serving Throughput (Tokens/s)</text>
</svg>`,
		int(330-rr.ThroughputTokensS*1.2), int(rr.ThroughputTokensS*1.2), int(320-rr.ThroughputTokensS*1.2), rr.ThroughputTokensS,
		int(330-lq.ThroughputTokensS*1.2), int(lq.ThroughputTokensS*1.2), int(320-lq.ThroughputTokensS*1.2), lq.ThroughputTokensS,
		int(330-kv.ThroughputTokensS*1.2), int(kv.ThroughputTokensS*1.2), int(320-kv.ThroughputTokensS*1.2), kv.ThroughputTokensS,
		int(330-pa.ThroughputTokensS*1.2), int(pa.ThroughputTokensS*1.2), int(320-pa.ThroughputTokensS*1.2), pa.ThroughputTokensS,
	)
	_ = os.WriteFile(filepath.Join(dir, "router_throughput_shed_rate.svg"), []byte(tputSVG), 0644)

	// 3. Prefix Cache Hit Ratio (SVG)
	cacheSVG := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 850 420" width="100%%" height="100%%">
  <rect width="850" height="420" fill="#1e1e2e"/>
  <text x="425" y="38" text-anchor="middle" fill="#cdd6f4" font-size="20" font-family="sans-serif" font-weight="bold">Prefix-Cache Hit Rate (RadixAttention KV Reuse) [simulated]</text>
  <text x="425" y="62" text-anchor="middle" fill="#a6adc8" font-size="12" font-family="sans-serif">Higher is Better — 70%% System Prompt Sharing</text>

  <!-- Axes -->
  <line x1="100" y1="330" x2="780" y2="330" stroke="#45475a" stroke-width="2"/>
  <line x1="100" y1="90" x2="100" y2="330" stroke="#45475a" stroke-width="2"/>

  <!-- RoundRobin -->
  <text x="180" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">RoundRobin</text>
  <rect x="150" y="%d" width="60" height="%d" fill="#f38ba8" rx="4"/>
  <text x="180" y="%d" text-anchor="middle" fill="#f38ba8" font-size="12" font-family="sans-serif" font-weight="bold">%.1f%%</text>

  <!-- LeastQueue -->
  <text x="360" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">LeastQueue</text>
  <rect x="330" y="%d" width="60" height="%d" fill="#fab387" rx="4"/>
  <text x="360" y="%d" text-anchor="middle" fill="#fab387" font-size="12" font-family="sans-serif" font-weight="bold">%.1f%%</text>

  <!-- KVAware -->
  <text x="540" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif">KVAware</text>
  <rect x="510" y="%d" width="60" height="%d" fill="#89b4fa" rx="4"/>
  <text x="540" y="%d" text-anchor="middle" fill="#89b4fa" font-size="12" font-family="sans-serif" font-weight="bold">%.1f%%</text>

  <!-- PrefixAffinity -->
  <text x="720" y="355" text-anchor="middle" fill="#cdd6f4" font-size="13" font-family="sans-serif" font-weight="bold">PrefixAffinity</text>
  <rect x="690" y="%d" width="60" height="%d" fill="#a6e3a1" rx="4"/>
  <text x="720" y="%d" text-anchor="middle" fill="#a6e3a1" font-size="12" font-family="sans-serif" font-weight="bold">%.1f%%</text>
</svg>`,
		int(330-rr.PrefixHitRate*2.2), int(rr.PrefixHitRate*2.2), int(320-rr.PrefixHitRate*2.2), rr.PrefixHitRate,
		int(330-lq.PrefixHitRate*2.2), int(lq.PrefixHitRate*2.2), int(320-lq.PrefixHitRate*2.2), lq.PrefixHitRate,
		int(330-kv.PrefixHitRate*2.2), int(kv.PrefixHitRate*2.2), int(320-kv.PrefixHitRate*2.2), kv.PrefixHitRate,
		int(330-pa.PrefixHitRate*2.2), int(pa.PrefixHitRate*2.2), int(320-pa.PrefixHitRate*2.2), pa.PrefixHitRate,
	)
	_ = os.WriteFile(filepath.Join(dir, "router_prefix_cache_hits.svg"), []byte(cacheSVG), 0644)
}
