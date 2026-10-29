/*
Package router provides the top-level Router server and HTTP mux.
*/
package router

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Server coordinates the proxy handler, replica pool, metric scraper, and HTTP server.
type Server struct {
	proxy      *ProxyHandler
	scraper    *MetricScraper
	mu         sync.RWMutex
	pool       []*ReplicaState
	listenAddr string
	server     *http.Server
}

// NewServer initializes a Meridian router server instance.
func NewServer(listenAddr string, strategy Strategy, cfg ProxyConfig) *Server {
	s := &Server{
		listenAddr: listenAddr,
		scraper:    NewMetricScraper(500*time.Millisecond, 2*time.Second),
		pool:       make([]*ReplicaState, 0),
	}

	engine := NewStrategyEngine()
	s.proxy = NewProxyHandler(engine, cfg, strategy, s.GetPool)
	return s
}

// UpdatePool updates the active replica pool safely.
func (s *Server) UpdatePool(replicas []*ReplicaState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pool = replicas
}

// GetPool returns a thread-safe snapshot of active replicas.
func (s *Server) GetPool() []*ReplicaState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	copied := make([]*ReplicaState, len(s.pool))
	copy(copied, s.pool)
	return copied
}

// SetStrategy modifies the active routing strategy dynamically.
func (s *Server) SetStrategy(strat Strategy) {
	s.proxy.SetStrategy(strat)
}

// HandleRoutes dumps the current routing topology and replica states as JSON.
func (s *Server) HandleRoutes(w http.ResponseWriter, r *http.Request) {
	pool := s.GetPool()
	type ReplicaSummary struct {
		ID              string  `json:"id"`
		Address         string  `json:"address"`
		Warm            bool    `json:"warm"`
		Healthy         bool    `json:"healthy"`
		WaitingRequests int64   `json:"waiting_requests"`
		RunningRequests int64   `json:"running_requests"`
		InflightRouted  int64   `json:"inflight_routed"`
		EffectiveLoad   int64   `json:"effective_load"`
		KVCacheUsage    float64 `json:"kv_cache_usage"`
	}

	summaries := make([]ReplicaSummary, 0, len(pool))
	for _, rep := range pool {
		rep.mu.RLock()
		summaries = append(summaries, ReplicaSummary{
			ID:              rep.ID,
			Address:         rep.Address,
			Warm:            rep.Warm,
			Healthy:         rep.Healthy,
			WaitingRequests: rep.WaitingRequests,
			RunningRequests: rep.RunningRequests,
			InflightRouted:  rep.InflightRouted,
			EffectiveLoad:   rep.WaitingRequests + rep.RunningRequests + rep.InflightRouted,
			KVCacheUsage:    rep.KVCacheUsage,
		})
		rep.mu.RUnlock()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"active_strategy": s.proxy.strategy,
		"total_routed":    atomic.LoadUint64(&s.proxy.TotalRouted),
		"total_shed":      atomic.LoadUint64(&s.proxy.TotalShed),
		"replicas":        summaries,
	})
}

// HandleHealthz handles liveness probe.
func (s *Server) HandleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("OK\n"))
}

// HandleReadyz checks that at least one healthy, warm replica is registered.
func (s *Server) HandleReadyz(w http.ResponseWriter, r *http.Request) {
	pool := s.GetPool()
	for _, rep := range pool {
		if rep.Warm && rep.Healthy {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("READY\n"))
			return
		}
	}
	http.Error(w, "no ready warm replicas available", http.StatusServiceUnavailable)
}

// Start begins serving traffic and background metric scraping.
func (s *Server) Start(ctx context.Context) error {
	go s.scraper.StartScraping(ctx, s.GetPool)

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.HandleHealthz)
	mux.HandleFunc("/readyz", s.HandleReadyz)
	mux.HandleFunc("/routes", s.HandleRoutes)
	mux.Handle("/metrics", promhttp.Handler())
	mux.Handle("/v1/chat/completions", s.proxy)
	mux.Handle("/v1/completions", s.proxy)

	s.server = &http.Server{
		Addr:         s.listenAddr,
		Handler:      mux,
		ReadTimeout:  60 * time.Second,
		WriteTimeout: 120 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.server.Shutdown(shutdownCtx)
	case err := <-errCh:
		return fmt.Errorf("router server failed: %w", err)
	}
}
