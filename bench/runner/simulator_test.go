package runner

import (
	"testing"
	"time"

	"github.com/tijani-web/meridian/pkg/tracegen"
)

func TestSimulation_MeridianZeroPartials(t *testing.T) {
	cfg := BenchmarkConfig{
		Mode:        ModeMeridian,
		NodeCount:   500,
		GPUsPerNode: 8,
		JobCount:    100,
		Seed:        42,
		Repetitions: 1,
	}

	gen := tracegen.NewGenerator(tracegen.Config{
		Seed:        42,
		JobCount:    100,
		ArrivalRate: 2.0,
		Queues:      []string{"team-nlp", "team-vision"},
	})
	jobs := gen.Generate()

	result := RunBenchmarkSimulation(cfg, jobs)

	// In Meridian, gang atomicity invariant dictates zero partially started gangs
	if result.PartiallyStartedGangs != 0 {
		t.Errorf("expected 0 partially started gangs in Meridian, got %d", result.PartiallyStartedGangs)
	}

	// Throughput must be positive
	if result.ThroughputPodsPerSec <= 0 {
		t.Errorf("expected positive throughput, got %f", result.ThroughputPodsPerSec)
	}

	// Peak allocation ratio must be positive
	if result.PeakAllocationRatio <= 0 {
		t.Errorf("expected positive allocation ratio, got %f", result.PeakAllocationRatio)
	}
}

func TestSimulation_DefaultSchedulerComparison(t *testing.T) {
	// Small fleet (16 nodes = 128 GPUs) under 200 jobs (~1200 GPUs demand) to create contention
	cfgDef := BenchmarkConfig{
		Mode:        ModeDefaultScheduler,
		NodeCount:   16,
		GPUsPerNode: 8,
		JobCount:    200,
		Seed:        42,
	}

	cfgMer := BenchmarkConfig{
		Mode:        ModeMeridian,
		NodeCount:   16,
		GPUsPerNode: 8,
		JobCount:    200,
		Seed:        42,
	}

	gen := tracegen.NewGenerator(tracegen.Config{
		Seed:        42,
		JobCount:    200,
		ArrivalRate: 2.0,
		Queues:      []string{"team-nlp", "team-vision", "team-rl"},
	})
	jobs := gen.Generate()

	resDef := RunBenchmarkSimulation(cfgDef, jobs)
	resMer := RunBenchmarkSimulation(cfgMer, jobs)

	// Default scheduler allows partially-started gangs under contention
	if resDef.PartiallyStartedGangs == 0 {
		t.Errorf("expected default scheduler to have partially-started gangs under contention")
	}

	// Meridian must have zero partially-started gangs
	if resMer.PartiallyStartedGangs != 0 {
		t.Errorf("expected Meridian to have zero partially started gangs, got %d", resMer.PartiallyStartedGangs)
	}

	// Meridian gang start latency must be lower or equal
	if resMer.MeanGangStartLatency > resDef.MeanGangStartLatency {
		t.Errorf("expected Meridian gang start latency <= default, got %v vs %v",
			resMer.MeanGangStartLatency, resDef.MeanGangStartLatency)
	}
}

func TestExecuteSuite_StatisticalRigor(t *testing.T) {
	cfg := BenchmarkConfig{
		Mode:        ModeMeridian,
		NodeCount:   500,
		GPUsPerNode: 8,
		JobCount:    50,
		Seed:        100,
		Repetitions: 5,
		WarmupRuns:  1,
	}

	stats := ExecuteSuite(cfg)

	if stats.Repetitions != 5 {
		t.Errorf("expected 5 repetitions, got %d", stats.Repetitions)
	}
	if stats.ThroughputPodsPerSec.Median <= 0 {
		t.Errorf("expected positive median throughput, got %f", stats.ThroughputPodsPerSec.Median)
	}
	if stats.ThroughputPodsPerSec.P5 > stats.ThroughputPodsPerSec.P95 {
		t.Errorf("expected p5 <= p95, got p5=%f, p95=%f",
			stats.ThroughputPodsPerSec.P5, stats.ThroughputPodsPerSec.P95)
	}
	if stats.PartiallyStartedGangs != 0 {
		t.Errorf("expected 0 partially started gangs, got %d", stats.PartiallyStartedGangs)
	}
}

func TestSimulateFaultTolerance(t *testing.T) {
	faults := SimulateFaultTolerance(500, 20)

	if faults.TotalFaultsInjected != 20 {
		t.Errorf("expected 20 faults, got %d", faults.TotalFaultsInjected)
	}
	if faults.TimeToDetect <= 0 || faults.TimeToDetect > 5*time.Second {
		t.Errorf("unexpected time to detect: %v", faults.TimeToDetect)
	}
	if faults.TimeToReschedule <= 0 || faults.TimeToReschedule > 10*time.Second {
		t.Errorf("unexpected time to reschedule: %v", faults.TimeToReschedule)
	}
	if faults.StepsLostVsLastAck <= 0 {
		t.Errorf("expected positive lost steps, got %d", faults.StepsLostVsLastAck)
	}
	if faults.JobsResumedPct != 100.0 {
		t.Errorf("expected 100%% resumed jobs, got %f", faults.JobsResumedPct)
	}
}
