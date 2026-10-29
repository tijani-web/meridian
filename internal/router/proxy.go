/*
Package router provides the streaming reverse proxy with load shedding.
*/
package router

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// ProxyConfig defines operational thresholds for the inference reverse proxy.
type ProxyConfig struct {
	MaxQueueDepth     int64
	BatchShedRatio    float64 // default 0.70 (batch shed at 70% threshold)
	DefaultRetryAfter int     // seconds
	UpstreamTimeout   time.Duration
}

// DefaultProxyConfig provides sensible defaults.
func DefaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		MaxQueueDepth:     200,
		BatchShedRatio:    0.70,
		DefaultRetryAfter: 2,
		UpstreamTimeout:   60 * time.Second,
	}
}

// ProxyHandler handles inbound OpenAI requests, strategy selection, shedding, and streaming passthrough.
type ProxyHandler struct {
	engine   *StrategyEngine
	config   ProxyConfig
	client   *http.Client
	getPool  func() []*ReplicaState
	strategy Strategy

	TotalRouted uint64
	TotalShed   uint64
}

// NewProxyHandler constructs an instance of ProxyHandler.
func NewProxyHandler(engine *StrategyEngine, cfg ProxyConfig, strategy Strategy, getPool func() []*ReplicaState) *ProxyHandler {
	transport := &http.Transport{
		MaxIdleConns:        1000,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression: true,
	}

	return &ProxyHandler{
		engine:   engine,
		config:   cfg,
		client:   &http.Client{Transport: transport, Timeout: cfg.UpstreamTimeout},
		getPool:  getPool,
		strategy: strategy,
	}
}

// SetStrategy updates the active routing strategy dynamically.
func (p *ProxyHandler) SetStrategy(s Strategy) {
	p.strategy = s
}

// ShouldShed determines if the incoming request should be shed under current queue load.
func (p *ProxyHandler) ShouldShed(pool []*ReplicaState, priority PriorityClass) (bool, int, string) {
	var totalQueue int64
	for _, r := range pool {
		if r == nil {
			continue
		}
		r.mu.RLock()
		totalQueue += r.WaitingRequests + r.InflightRouted
		r.mu.RUnlock()
	}

	limit := p.config.MaxQueueDepth
	if priority == PriorityBatch {
		limit = int64(float64(p.config.MaxQueueDepth) * p.config.BatchShedRatio)
	}

	if totalQueue >= limit {
		retry := p.config.DefaultRetryAfter
		if retry < 1 {
			retry = 1
		}
		return true, retry, fmt.Sprintf("Cluster queue depth (%d) exceeded threshold (%d) for priority %s", totalQueue, limit, priority)
	}

	return false, 0, ""
}

func (p *ProxyHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only accept standard inference paths
	path := r.URL.Path
	if !strings.HasPrefix(path, "/v1/chat/completions") && !strings.HasPrefix(path, "/v1/completions") {
		http.NotFound(w, r)
		return
	}

	pool := p.getPool()
	priority := PriorityInteractive
	if strings.ToLower(r.Header.Get("X-Meridian-Priority")) == "batch" {
		priority = PriorityBatch
	}

	// 1. Evaluate Load Shedding
	if shed, retryAfter, reason := p.ShouldShed(pool, priority); shed {
		atomic.AddUint64(&p.TotalShed, 1)
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
		w.WriteHeader(http.StatusTooManyRequests)

		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": reason,
				"type":    "load_shed",
				"code":    http.StatusTooManyRequests,
			},
		})
		return
	}

	// 2. Read request body to extract prompt for PrefixAffinity routing
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to read request body: %v", err), http.StatusBadRequest)
		return
	}
	r.Body.Close()

	var prefixText string
	var parsed struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		Prompt interface{} `json:"prompt"`
	}

	if jsonErr := json.Unmarshal(bodyBytes, &parsed); jsonErr == nil {
		if len(parsed.Messages) > 0 {
			prefixText = parsed.Messages[0].Content
		} else if strPrompt, ok := parsed.Prompt.(string); ok {
			prefixText = strPrompt
		}
	}

	// 3. Select Target Replica via Strategy
	decision, err := p.engine.Route(p.strategy, pool, prefixText)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"error": map[string]interface{}{
				"message": err.Error(),
				"type":    "no_replicas_available",
				"code":    http.StatusServiceUnavailable,
			},
		})
		return
	}

	target := decision.SelectedReplica
	atomic.AddUint64(&p.TotalRouted, 1)

	// Increment inflight load on selected target
	target.mu.Lock()
	target.InflightRouted++
	target.mu.Unlock()

	defer func() {
		target.mu.Lock()
		if target.InflightRouted > 0 {
			target.InflightRouted--
		}
		target.mu.Unlock()
	}()

	// 4. Construct Upstream Request
	targetURL := fmt.Sprintf("%s%s", strings.TrimRight(target.Address, "/"), r.URL.RequestURI())
	upstreamReq, err := http.NewRequestWithContext(r.Context(), r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, fmt.Sprintf("failed to create upstream request: %v", err), http.StatusInternalServerError)
		return
	}

	// Pass through headers
	for k, vv := range r.Header {
		for _, v := range vv {
			upstreamReq.Header.Add(k, v)
		}
	}
	upstreamReq.Header.Set("X-Forwarded-For", r.RemoteAddr)
	upstreamReq.Header.Set("X-Meridian-Strategy", string(decision.StrategyUsed))
	upstreamReq.Header.Set("X-Meridian-Target", target.ID)

	// 5. Execute Upstream Call
	resp, err := p.client.Do(upstreamReq)
	if err != nil {
		// Mark target as unhealthy if connection refused or dropped
		target.mu.Lock()
		target.Healthy = false
		target.mu.Unlock()

		http.Error(w, fmt.Sprintf("upstream replica error: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	// 6. Forward Response Headers
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	// 7. Streaming SSE / Chunked Passthrough
	flusher, isFlusher := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			if _, writeErr := w.Write(buf[:n]); writeErr != nil {
				return // Downstream client disconnected
			}
			if isFlusher {
				flusher.Flush()
			}
		}
		if readErr != nil {
			break
		}
	}
}
