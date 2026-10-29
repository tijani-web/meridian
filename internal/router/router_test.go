package router

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func makeReplica(id, addr string, waiting, running int64, kv float64) *ReplicaState {
	return &ReplicaState{
		ID:              id,
		Address:         addr,
		Warm:            true,
		Healthy:         true,
		WaitingRequests: waiting,
		RunningRequests: running,
		KVCacheUsage:    kv,
		LastScraped:     time.Now(),
	}
}

func TestStrategy_RoundRobin(t *testing.T) {
	r1 := makeReplica("rep-1", "http://10.0.0.1:8000", 0, 0, 0.1)
	r2 := makeReplica("rep-2", "http://10.0.0.2:8000", 0, 0, 0.1)
	r3 := makeReplica("rep-3", "http://10.0.0.3:8000", 0, 0, 0.1)
	pool := []*ReplicaState{r1, r2, r3}

	engine := NewStrategyEngine()
	// Sequence of 6 calls must cycle r1, r2, r3, r1, r2, r3
	expectedOrder := []string{"rep-1", "rep-2", "rep-3", "rep-1", "rep-2", "rep-3"}
	for i, expected := range expectedOrder {
		dec, err := engine.Route(StrategyRoundRobin, pool, "")
		if err != nil {
			t.Fatalf("call %d: unexpected error: %v", i, err)
		}
		if dec.SelectedReplica.ID != expected {
			t.Fatalf("call %d: expected %s, got %s", i, expected, dec.SelectedReplica.ID)
		}
	}
}

func TestStrategy_LeastQueue(t *testing.T) {
	r1 := makeReplica("rep-heavy", "http://10.0.0.1:8000", 10, 5, 0.5) // load = 15
	r2 := makeReplica("rep-light", "http://10.0.0.2:8000", 1, 1, 0.2)  // load = 2
	r3 := makeReplica("rep-med", "http://10.0.0.3:8000", 4, 3, 0.4)    // load = 7
	pool := []*ReplicaState{r1, r2, r3}

	engine := NewStrategyEngine()
	dec, err := engine.Route(StrategyLeastQueue, pool, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.SelectedReplica.ID != "rep-light" {
		t.Fatalf("expected rep-light (load 2), got %s", dec.SelectedReplica.ID)
	}

	// Make rep-light stale (older than 3s)
	r2.LastScraped = time.Now().Add(-5 * time.Second)
	decStale, err := engine.Route(StrategyLeastQueue, pool, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// With +50 penalty on r2, r3 (rep-med with load 7) should win
	if decStale.SelectedReplica.ID != "rep-med" {
		t.Fatalf("expected stale penalty to divert traffic to rep-med, got %s", decStale.SelectedReplica.ID)
	}
}

func TestStrategy_KVAware(t *testing.T) {
	r1 := makeReplica("rep-kv-exhausted", "http://10.0.0.1:8000", 0, 1, 0.95) // load 1, but 95% KV
	r2 := makeReplica("rep-kv-safe", "http://10.0.0.2:8000", 2, 2, 0.60)      // load 4, but 60% KV (safe)
	pool := []*ReplicaState{r1, r2}

	engine := NewStrategyEngine()
	dec, err := engine.Route(StrategyKVAware, pool, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if dec.SelectedReplica.ID != "rep-kv-safe" {
		t.Fatalf("expected KVAware to pick rep-kv-safe to avoid preemption, got %s", dec.SelectedReplica.ID)
	}

	// When all are above threshold, pick the one with lowest KV pressure
	r2.KVCacheUsage = 0.92
	r1.KVCacheUsage = 0.88 // lower than 0.92
	decSat, err := engine.Route(StrategyKVAware, pool, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decSat.SelectedReplica.ID != "rep-kv-exhausted" {
		t.Fatalf("expected saturated cluster to pick rep-kv-exhausted (0.88 < 0.92), got %s", decSat.SelectedReplica.ID)
	}
}

func TestStrategy_PrefixAffinity_BoundedLoad(t *testing.T) {
	r1 := makeReplica("rep-1", "http://10.0.0.1:8000", 0, 1, 0.2)
	r2 := makeReplica("rep-2", "http://10.0.0.2:8000", 0, 1, 0.2)
	pool := []*ReplicaState{r1, r2}

	engine := NewStrategyEngine()
	prefix := "You are a code generation model specialized in Go."

	// 1. Initial request: routes to consistent hash primary (cache hit)
	dec1, err := engine.Route(StrategyPrefixAffinity, pool, prefix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	primaryID := dec1.SelectedReplica.ID
	if !dec1.IsCacheHit {
		t.Fatal("expected initial prefix routing to be marked cache hit")
	}

	// 2. Overload the primary replica beyond bounded load threshold
	dec1.SelectedReplica.WaitingRequests = 20 // heavily overloaded
	dec2, err := engine.Route(StrategyPrefixAffinity, pool, prefix)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should spill over to the other replica
	if dec2.SelectedReplica.ID == primaryID {
		t.Fatalf("expected bounded load to spillover from overloaded primary %s", primaryID)
	}
	if dec2.IsCacheHit {
		t.Fatal("expected spillover request to be marked cache miss")
	}
}

func TestLoadShedding(t *testing.T) {
	r1 := makeReplica("rep-1", "http://10.0.0.1:8000", 15, 0, 0.5) // total queue = 15
	pool := []*ReplicaState{r1}

	cfg := ProxyConfig{
		MaxQueueDepth:     20,
		BatchShedRatio:    0.70, // 70% of 20 = 14 limit for batch
		DefaultRetryAfter: 3,
		UpstreamTimeout:   5 * time.Second,
	}

	engine := NewStrategyEngine()
	proxy := NewProxyHandler(engine, cfg, StrategyLeastQueue, func() []*ReplicaState { return pool })

	// Interactive request at queue=15 (limit 20): NOT shed
	shedInter, _, _ := proxy.ShouldShed(pool, PriorityInteractive)
	if shedInter {
		t.Fatal("expected interactive request not to be shed at queue 15/20")
	}

	// Batch request at queue=15 (batch limit 14): SHED
	shedBatch, retry, reason := proxy.ShouldShed(pool, PriorityBatch)
	if !shedBatch {
		t.Fatal("expected batch request to be shed at queue 15/14")
	}
	if retry != 3 {
		t.Fatalf("expected Retry-After 3, got %d", retry)
	}
	if !strings.Contains(reason, "exceeded threshold") {
		t.Fatalf("unexpected reason: %s", reason)
	}

	// Increase queue to 25: Interactive is now also SHED
	r1.WaitingRequests = 25
	shedInter2, retry2, _ := proxy.ShouldShed(pool, PriorityInteractive)
	if !shedInter2 {
		t.Fatal("expected interactive request to be shed when exceeding 20")
	}
	if retry2 != 3 {
		t.Fatalf("expected Retry-After 3, got %d", retry2)
	}
}

func TestProxy_StreamingSSE(t *testing.T) {
	// Spin up simulated upstream vLLM server
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", 500)
			return
		}
		for i := 0; i < 3; i++ {
			chunk := fmt.Sprintf(`{"choices":[{"delta":{"content":"token%d"}}]}`, i)
			fmt.Fprintf(w, "data: %s\n\n", chunk)
			flusher.Flush()
			time.Sleep(5 * time.Millisecond)
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	defer upstream.Close()

	rep := makeReplica("upstream-1", upstream.URL, 0, 0, 0.1)
	pool := []*ReplicaState{rep}

	cfg := DefaultProxyConfig()
	engine := NewStrategyEngine()
	proxy := NewProxyHandler(engine, cfg, StrategyLeastQueue, func() []*ReplicaState { return pool })

	reqBody := `{"model":"llama-3-8b","messages":[{"role":"user","content":"hi"}],"stream":true}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	rec := httptest.NewRecorder()

	proxy.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 OK, got %d: %s", rec.Code, rec.Body.String())
	}

	scanner := bufio.NewScanner(rec.Body)
	chunksReceived := 0
	hasDone := false
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "data: ") {
			data := strings.TrimPrefix(line, "data: ")
			if data == "[DONE]" {
				hasDone = true
			} else {
				chunksReceived++
			}
		}
	}

	if chunksReceived != 3 {
		t.Fatalf("expected 3 stream chunks, got %d", chunksReceived)
	}
	if !hasDone {
		t.Fatal("expected [DONE] terminator")
	}
}

func TestProxy_ClientCancellation(t *testing.T) {
	upstreamHit := int32(0)
	upstreamCancelled := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.StoreInt32(&upstreamHit, 1)
		w.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := w.(http.Flusher); ok {
			fmt.Fprintf(w, "data: {\"token\":\"first\"}\n\n")
			flusher.Flush()
		}
		select {
		case <-r.Context().Done():
			close(upstreamCancelled)
			return
		case <-time.After(2 * time.Second):
			return
		}
	}))
	defer upstream.Close()

	rep := makeReplica("upstream-cancel", upstream.URL, 0, 0, 0.1)
	pool := []*ReplicaState{rep}

	cfg := DefaultProxyConfig()
	engine := NewStrategyEngine()
	proxy := NewProxyHandler(engine, cfg, StrategyLeastQueue, func() []*ReplicaState { return pool })

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader([]byte(`{"messages":[]}`))).WithContext(ctx)
	rec := httptest.NewRecorder()

	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel() // Simulate client closing connection mid-stream
	}()

	proxy.ServeHTTP(rec, req)

	select {
	case <-upstreamCancelled:
		// Success: cancellation propagated upstream
	case <-time.After(500 * time.Millisecond):
		t.Fatal("expected client cancellation to propagate to upstream request context")
	}

	// Verify inflight request was properly decremented
	if rep.InflightRouted != 0 {
		t.Errorf("expected InflightRouted to reset to 0, got %d", rep.InflightRouted)
	}
}
