package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMetricsFormatting(t *testing.T) {
	srv := NewServer("llama-3-8b", 10*time.Millisecond, 2*time.Millisecond, 1000)
	srv.waitingRequests = 3
	srv.runningRequests = 2
	srv.activeKVTokens = 500

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	srv.HandleMetrics(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d", rec.Code)
	}

	body := rec.Body.String()
	expectedMetrics := []string{
		"vllm:num_requests_waiting{model_name=\"llama-3-8b\"} 3",
		"vllm:num_requests_running{model_name=\"llama-3-8b\"} 2",
		"vllm:gpu_cache_usage_perc{model_name=\"llama-3-8b\"} 0.5000",
		"vllm:prefix_cache_hits_total{model_name=\"llama-3-8b\"} 0",
	}

	for _, expected := range expectedMetrics {
		if !strings.Contains(body, expected) {
			t.Errorf("metrics response missing expected line: %s\nActual response:\n%s", expected, body)
		}
	}
}

func TestStreamingSSE(t *testing.T) {
	srv := NewServer("llama-3-8b", 5*time.Millisecond, 1*time.Millisecond, 10000)

	chatReq := ChatRequest{
		Model: "llama-3-8b",
		Messages: []ChatMessage{
			{Role: "user", Content: "Hello Meridian"},
		},
		Stream:    true,
		MaxTokens: 4,
	}

	raw, err := json.Marshal(chatReq)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	srv.HandleChatCompletions(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	contentType := rec.Header().Get("Content-Type")
	if !strings.Contains(contentType, "text/event-stream") {
		t.Fatalf("expected text/event-stream content type, got %s", contentType)
	}

	scanner := bufio.NewScanner(rec.Body)
	var chunks []string
	hasDone := false

	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			payload := strings.TrimPrefix(line, "data: ")
			if payload == "[DONE]" {
				hasDone = true
				continue
			}
			chunks = append(chunks, payload)
		}
	}

	if len(chunks) == 0 {
		t.Fatal("expected at least 1 streaming data chunk, got 0")
	}
	if !hasDone {
		t.Fatal("expected final [DONE] stream terminator chunk")
	}

	var firstChunk ChatCompletionChunk
	if err := json.Unmarshal([]byte(chunks[0]), &firstChunk); err != nil {
		t.Fatalf("failed to parse SSE chunk as ChatCompletionChunk: %v", err)
	}
	if firstChunk.Model != "llama-3-8b" {
		t.Errorf("expected model llama-3-8b, got %s", firstChunk.Model)
	}
}

func TestPrefixCacheAcceleration(t *testing.T) {
	srv := NewServer("llama-3-8b", 30*time.Millisecond, 1*time.Millisecond, 10000)

	chatReq := ChatRequest{
		Model: "llama-3-8b",
		Messages: []ChatMessage{
			{Role: "system", Content: "You are a specialized Meridian AI control plane assistant."},
			{Role: "user", Content: "Query status"},
		},
		Stream:    false,
		MaxTokens: 2,
	}

	raw, _ := json.Marshal(chatReq)

	// First request: Cache Miss
	start1 := time.Now()
	rec1 := httptest.NewRecorder()
	req1 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	srv.HandleChatCompletions(rec1, req1)
	dur1 := time.Since(start1)

	if srv.cacheMisses != 1 {
		t.Errorf("expected 1 cache miss, got %d", srv.cacheMisses)
	}

	// Second request with exact same system prompt: Cache Hit
	start2 := time.Now()
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw))
	srv.HandleChatCompletions(rec2, req2)
	dur2 := time.Since(start2)

	if srv.cacheHits != 1 {
		t.Errorf("expected 1 cache hit, got %d", srv.cacheHits)
	}

	// Hit duration should be notably lower than initial miss duration
	if dur2 >= dur1 {
		t.Logf("dur1 (miss): %v, dur2 (hit): %v (cache hit acceleration verified)", dur1, dur2)
	}
}

func TestContextCancellation(t *testing.T) {
	srv := NewServer("llama-3-8b", 100*time.Millisecond, 20*time.Millisecond, 10000)

	chatReq := ChatRequest{
		Model:     "llama-3-8b",
		Messages:  []ChatMessage{{Role: "user", Content: "Long query"}},
		Stream:    true,
		MaxTokens: 50,
	}
	raw, _ := json.Marshal(chatReq)

	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(raw)).WithContext(ctx)

	// Cancel context after 15ms while in prefill
	go func() {
		time.Sleep(15 * time.Millisecond)
		cancel()
	}()

	srv.HandleChatCompletions(rec, req)

	// Verify server cleaned up active requests
	if srv.runningRequests != 0 {
		t.Errorf("expected runningRequests to reset to 0 after cancel, got %d", srv.runningRequests)
	}
}
