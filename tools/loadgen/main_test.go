package main

import (
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestGenerateRequestPayload(t *testing.T) {
	cfg := Config{
		Model:       "llama-3-8b",
		Workload:    WorkloadSharedPrefix,
		SharedRatio: 0.70,
		BatchRatio:  0.20,
		MaxTokens:   32,
	}

	rng := rand.New(rand.NewSource(12345))

	sharedCount := 0
	total := 1000
	for i := 0; i < total; i++ {
		_, isShared, _ := GenerateRequestPayload(rng, &cfg, i)
		if isShared {
			sharedCount++
		}
	}

	ratio := float64(sharedCount) / float64(total)
	// Expected ~70% shared (+- 5%)
	if ratio < 0.65 || ratio > 0.75 {
		t.Errorf("expected shared ratio around 0.70, got %.3f", ratio)
	}
}

func TestComputeSummary(t *testing.T) {
	results := []RequestResult{
		{StatusCode: 200, TTFT: 10 * time.Millisecond, TotalLatency: 50 * time.Millisecond, Tokens: 5, Strategy: "LeastQueue"},
		{StatusCode: 200, TTFT: 20 * time.Millisecond, TotalLatency: 60 * time.Millisecond, Tokens: 5, Strategy: "LeastQueue"},
		{StatusCode: 200, TTFT: 30 * time.Millisecond, TotalLatency: 70 * time.Millisecond, Tokens: 5, Strategy: "LeastQueue"},
		{StatusCode: 429, IsShed: true, TotalLatency: 2 * time.Millisecond, Strategy: "LeastQueue"},
	}

	summary := ComputeSummary(results, 1*time.Second, "test-workload")

	if summary.TotalRequests != 4 {
		t.Errorf("expected 4 total requests, got %d", summary.TotalRequests)
	}
	if summary.Successful != 3 {
		t.Errorf("expected 3 successful requests, got %d", summary.Successful)
	}
	if summary.ShedCount != 1 {
		t.Errorf("expected 1 shed request, got %d", summary.ShedCount)
	}
	if summary.ShedRatePercent != 25.0 {
		t.Errorf("expected 25%% shed rate, got %.1f%%", summary.ShedRatePercent)
	}
	if summary.TTFTP50Ms != 20.0 {
		t.Errorf("expected TTFT p50 to be 20ms, got %.1f", summary.TTFTP50Ms)
	}
	if summary.ThroughputTokensS != 15.0 {
		t.Errorf("expected 15 tokens/sec, got %.1f", summary.ThroughputTokensS)
	}
}

func TestWriteResultsCSV(t *testing.T) {
	tmpDir := t.TempDir()
	csvPath := filepath.Join(tmpDir, "results.csv")

	results := []RequestResult{
		{
			Index:        0,
			StatusCode:   200,
			TTFT:         15 * time.Millisecond,
			TotalLatency: 80 * time.Millisecond,
			Tokens:       8,
			PrefixShared: true,
			TargetNode:   "node-1",
			Strategy:     "PrefixAffinity",
		},
	}

	if err := WriteResultsCSV(csvPath, results); err != nil {
		t.Fatalf("failed to write CSV: %v", err)
	}

	content, err := os.ReadFile(csvPath)
	if err != nil {
		t.Fatalf("failed to read written CSV: %v", err)
	}
	if len(content) == 0 {
		t.Fatal("written CSV is empty")
	}
}
