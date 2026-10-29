/*
Package main implements the meridian-router CLI binary.

It launches the Meridian Inference Router reverse proxy, coordinating
vLLM metric scrapers, pluggable dispatch strategies, priority load shedding,
and streaming pass-through.
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/tijani-web/meridian/internal/router"
)

func main() {
	port := flag.Int("port", 8080, "Port on which the router listens for OpenAI-compatible inference requests")
	strategyStr := flag.String("strategy", "LeastQueue", "Routing strategy: RoundRobin, LeastQueue, KVAware, PrefixAffinity")
	endpointsStr := flag.String("endpoints", "", "Comma-separated list of replica addresses (e.g. http://10.244.1.5:8000,http://10.244.2.8:8000)")
	maxQueue := flag.Int64("max-queue", 200, "Maximum cluster queue depth before shedding excess traffic with HTTP 429")
	batchShedRatio := flag.Float64("batch-shed-ratio", 0.70, "Fraction of max-queue at which batch traffic is shed")
	retryAfter := flag.Int("retry-after", 2, "Retry-After header duration in seconds on 429 shedding")
	flag.Parse()

	strategy := router.Strategy(*strategyStr)
	switch strategy {
	case router.StrategyRoundRobin, router.StrategyLeastQueue, router.StrategyKVAware, router.StrategyPrefixAffinity:
	default:
		log.Fatalf("unsupported strategy: %s (supported: RoundRobin, LeastQueue, KVAware, PrefixAffinity)", *strategyStr)
	}

	cfg := router.ProxyConfig{
		MaxQueueDepth:     *maxQueue,
		BatchShedRatio:    *batchShedRatio,
		DefaultRetryAfter: *retryAfter,
		UpstreamTimeout:   60 * time.Second,
	}

	listenAddr := fmt.Sprintf(":%d", *port)
	srv := router.NewServer(listenAddr, strategy, cfg)

	// Populate initial replica pool if provided
	if *endpointsStr != "" {
		parts := strings.Split(*endpointsStr, ",")
		var initialPool []*router.ReplicaState
		for i, p := range parts {
			addr := strings.TrimSpace(p)
			if addr == "" {
				continue
			}
			initialPool = append(initialPool, &router.ReplicaState{
				ID:          fmt.Sprintf("replica-%d", i),
				Address:     addr,
				Warm:        true,
				Healthy:     true,
				LastScraped: time.Now(),
			})
		}
		srv.UpdatePool(initialPool)
		log.Printf("[meridian-router] Initialized with %d replica(s)", len(initialPool))
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Printf("[meridian-router] Termination signal received; draining...")
		cancel()
	}()

	log.Printf("[meridian-router] Starting inference router on %s (strategy=%s, maxQueue=%d)", listenAddr, strategy, *maxQueue)
	if err := srv.Start(ctx); err != nil && err != context.Canceled {
		log.Fatalf("[meridian-router] Server error: %v", err)
	}
	log.Printf("[meridian-router] Shutdown complete.")
}
