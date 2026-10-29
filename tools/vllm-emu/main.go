/*
Package main implements tools/vllm-emu, an OpenAI-compatible HTTP inference emulator
that emits Prometheus metrics matching the pinned vLLM version (v0.9.2).

As defined in Meridian §1.1, this is allowed test infrastructure clearly labeled
as simulated, enabling high-fidelity benchmarking of the Meridian Inference Router
under varied queue depths, KV-cache loads, and prefix-sharing workloads without
requiring multi-GPU hardware.
*/
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"math/rand"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

// Server encapsulates the simulated vLLM engine state and HTTP API endpoints.
type Server struct {
	modelName       string
	baseTTFT        time.Duration
	baseTPOT        time.Duration
	maxKVTokens     int64
	prefixCacheHit  float64 // fraction of TTFT saved on prefix cache hit (e.g. 0.80)

	mu              sync.RWMutex
	waitingRequests int64
	runningRequests int64
	activeKVTokens  int64
	cachedPrefixes  map[string]time.Time // simulated RadixAttention prefix table

	totalRequests   uint64
	cacheHits       uint64
	cacheMisses     uint64
}

// NewServer initializes a new simulated vLLM engine instance.
func NewServer(model string, ttft, tpot time.Duration, maxKV int64) *Server {
	return &Server{
		modelName:      model,
		baseTTFT:       ttft,
		baseTPOT:       tpot,
		maxKVTokens:    maxKV,
		prefixCacheHit: 0.80,
		cachedPrefixes: make(map[string]time.Time),
	}
}

// ChatRequest represents the standard OpenAI chat completions payload.
type ChatRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	Temperature float32       `json:"temperature,omitempty"`
}

// ChatMessage represents a single chat conversation message.
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletionChunk represents an SSE chunk conforming to OpenAI streaming spec.
type ChatCompletionChunk struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []ChunkChoice `json:"choices"`
}

// ChunkChoice represents a single completion choice inside an SSE chunk.
type ChunkChoice struct {
	Index        int         `json:"index"`
	Delta        ChunkDelta  `json:"delta"`
	FinishReason *string     `json:"finish_reason"`
}

// ChunkDelta represents the incremental delta content of a completion token.
type ChunkDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// extractPrefix computes a stable hash for the initial system prompt or first message
// to simulate vLLM's RadixAttention prefix cache hit detection.
func extractPrefix(req *ChatRequest) string {
	if len(req.Messages) == 0 {
		return ""
	}
	// If first message is system prompt, hash it; otherwise hash first 64 chars of prompt
	content := req.Messages[0].Content
	if len(content) > 128 {
		content = content[:128]
	}
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:8])
}

// HandleChatCompletions handles OpenAI-compatible /v1/chat/completions requests.
func (s *Server) HandleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, fmt.Sprintf("invalid JSON body: %v", err), http.StatusBadRequest)
		return
	}

	atomic.AddUint64(&s.totalRequests, 1)
	s.mu.Lock()
	s.waitingRequests++
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		if s.waitingRequests > 0 {
			s.waitingRequests--
		}
		s.mu.Unlock()
	}()

	// Simulate queue wait time based on current running load
	s.mu.RLock()
	currentRunning := s.runningRequests
	s.mu.RUnlock()
	queueDelay := time.Duration(currentRunning) * (s.baseTTFT / 4)
	if queueDelay > 500*time.Millisecond {
		queueDelay = 500 * time.Millisecond
	}

	select {
	case <-time.After(queueDelay):
	case <-r.Context().Done():
		return // Client disconnected while waiting in queue
	}

	// Transition from waiting to running
	s.mu.Lock()
	s.waitingRequests--
	s.runningRequests++
	prefix := extractPrefix(&req)
	isCacheHit := false
	if prefix != "" {
		if _, exists := s.cachedPrefixes[prefix]; exists {
			isCacheHit = true
			s.cacheHits++
		} else {
			s.cachedPrefixes[prefix] = time.Now()
			s.cacheMisses++
		}
	}
	// Simulate KV-cache allocation (estimate ~256 tokens per request)
	reqTokens := int64(256)
	if req.MaxTokens > 0 {
		reqTokens += int64(req.MaxTokens)
	}
	s.activeKVTokens += reqTokens
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		s.runningRequests--
		s.activeKVTokens -= reqTokens
		if s.activeKVTokens < 0 {
			s.activeKVTokens = 0
		}
		s.mu.Unlock()
	}()

	// Calculate TTFT: apply prefix cache acceleration if hit
	effectiveTTFT := s.baseTTFT
	if isCacheHit {
		effectiveTTFT = time.Duration(float64(s.baseTTFT) * (1.0 - s.prefixCacheHit))
		if effectiveTTFT < 2*time.Millisecond {
			effectiveTTFT = 2 * time.Millisecond
		}
	}

	// Prefill phase delay
	select {
	case <-time.After(effectiveTTFT):
	case <-r.Context().Done():
		return // Client cancelled during prefill
	}

	// Determine output token count
	tokensToGenerate := 16
	if req.MaxTokens > 0 && req.MaxTokens < 128 {
		tokensToGenerate = req.MaxTokens
	}

	if req.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		reqID := fmt.Sprintf("chatcmpl-emu-%d", rand.Int63())
		created := time.Now().Unix()

		// Stream tokens one by one with TPOT delay
		words := []string{"Simulated", "token", "response", "from", "Meridian", "vLLM", "emulator", "node."}
		for i := 0; i < tokensToGenerate; i++ {
			word := words[i%len(words)]
			chunk := ChatCompletionChunk{
				ID:      reqID,
				Object:  "chat.completion.chunk",
				Created: created,
				Model:   s.modelName,
				Choices: []ChunkChoice{
					{
						Index: 0,
						Delta: ChunkDelta{Content: word + " "},
					},
				},
			}
			data, _ := json.Marshal(chunk)
			fmt.Fprintf(w, "data: %s\n\n", data)
			flusher.Flush()

			select {
			case <-time.After(s.baseTPOT):
			case <-r.Context().Done():
				return // Client disconnected mid-stream
			}
		}

		// Stream finish chunk
		doneReason := "stop"
		finalChunk := ChatCompletionChunk{
			ID:      reqID,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   s.modelName,
			Choices: []ChunkChoice{
				{
					Index:        0,
					FinishReason: &doneReason,
				},
			},
		}
		finalData, _ := json.Marshal(finalChunk)
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", finalData)
		flusher.Flush()
		return
	}

	// Non-streaming response
	select {
	case <-time.After(time.Duration(tokensToGenerate) * s.baseTPOT):
	case <-r.Context().Done():
		return
	}

	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"id":      fmt.Sprintf("chatcmpl-emu-%d", rand.Int63()),
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   s.modelName,
		"choices": []map[string]interface{}{
			{
				"index": 0,
				"message": map[string]string{
					"role":    "assistant",
					"content": "Simulated non-streaming response from Meridian vLLM emulator.",
				},
				"finish_reason": "stop",
			},
		},
		"usage": map[string]int{
			"prompt_tokens":     32,
			"completion_tokens": tokensToGenerate,
			"total_tokens":      32 + tokensToGenerate,
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

// HandleMetrics emits Prometheus metrics formatted strictly according to vLLM v0.9.2.
func (s *Server) HandleMetrics(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	waiting := s.waitingRequests
	running := s.runningRequests
	kvUsage := float64(s.activeKVTokens) / float64(s.maxKVTokens)
	if kvUsage > 1.0 {
		kvUsage = 1.0
	}
	hits := s.cacheHits
	misses := s.cacheMisses
	s.mu.RUnlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// Pinned metrics per docs/VERSIONS.md and vLLM v0.9.2 release
	fmt.Fprintf(w, "# HELP vllm:num_requests_waiting Number of requests waiting to be processed.\n")
	fmt.Fprintf(w, "# TYPE vllm:num_requests_waiting gauge\n")
	fmt.Fprintf(w, "vllm:num_requests_waiting{model_name=\"%s\"} %d\n", s.modelName, waiting)

	fmt.Fprintf(w, "# HELP vllm:num_requests_running Number of requests currently running.\n")
	fmt.Fprintf(w, "# TYPE vllm:num_requests_running gauge\n")
	fmt.Fprintf(w, "vllm:num_requests_running{model_name=\"%s\"} %d\n", s.modelName, running)

	fmt.Fprintf(w, "# HELP vllm:gpu_cache_usage_perc GPU KV-cache usage percentage.\n")
	fmt.Fprintf(w, "# TYPE vllm:gpu_cache_usage_perc gauge\n")
	fmt.Fprintf(w, "vllm:gpu_cache_usage_perc{model_name=\"%s\"} %.4f\n", s.modelName, kvUsage)

	fmt.Fprintf(w, "# HELP vllm:prefix_cache_hits_total Total prefix cache hits in RadixAttention.\n")
	fmt.Fprintf(w, "# TYPE vllm:prefix_cache_hits_total counter\n")
	fmt.Fprintf(w, "vllm:prefix_cache_hits_total{model_name=\"%s\"} %d\n", s.modelName, hits)

	fmt.Fprintf(w, "# HELP vllm:prefix_cache_misses_total Total prefix cache misses.\n")
	fmt.Fprintf(w, "# TYPE vllm:prefix_cache_misses_total counter\n")
	fmt.Fprintf(w, "vllm:prefix_cache_misses_total{model_name=\"%s\"} %d\n", s.modelName, misses)
}

// HandleHealth returns 200 OK for k8s liveness and readiness probes.
func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK\n"))
}

// HandleModels returns simulated model metadata.
func (s *Server) HandleModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	resp := map[string]interface{}{
		"object": "list",
		"data": []map[string]interface{}{
			{
				"id":       s.modelName,
				"object":   "model",
				"created":  time.Now().Unix(),
				"owned_by": "vllm-emulator",
			},
		},
	}
	_ = json.NewEncoder(w).Encode(resp)
}

func main() {
	port := flag.Int("port", 8000, "Port for vLLM emulator HTTP server")
	model := flag.String("model", "llama-3-8b", "Simulated model name")
	ttftMs := flag.Int("ttft-ms", 40, "Base Time-to-First-Token prefill latency in ms")
	tpotMs := flag.Int("tpot-ms", 10, "Base Time-per-Output-Token decode latency in ms")
	maxKV := flag.Int64("max-kv-tokens", 50000, "Maximum simulated KV cache tokens before 100% saturation")
	flag.Parse()

	srv := NewServer(*model, time.Duration(*ttftMs)*time.Millisecond, time.Duration(*tpotMs)*time.Millisecond, *maxKV)

	mux := http.NewServeMux()
	mux.HandleFunc("/health", srv.HandleHealth)
	mux.HandleFunc("/healthz", srv.HandleHealth)
	mux.HandleFunc("/v1/models", srv.HandleModels)
	mux.HandleFunc("/v1/chat/completions", srv.HandleChatCompletions)
	mux.HandleFunc("/metrics", srv.HandleMetrics)

	httpServer := &http.Server{
		Addr:         fmt.Sprintf(":%d", *port),
		Handler:      mux,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 60 * time.Second,
	}

	listener, err := net.Listen("tcp", httpServer.Addr)
	if err != nil {
		log.Fatalf("failed to listen on %s: %v", httpServer.Addr, err)
	}

	go func() {
		log.Printf("[vllm-emu] Starting simulated vLLM server on port %d (model=%s, ttft=%dms, tpot=%dms)", *port, *model, *ttftMs, *tpotMs)
		if err := httpServer.Serve(listener); err != nil && err != http.ErrServerClosed {
			log.Fatalf("http server error: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	<-sigCh

	log.Printf("[vllm-emu] Shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(ctx)
}
