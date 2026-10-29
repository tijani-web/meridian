/*
Package runner implements the scale and scheduler benchmark engine for Meridian (§8.1 & §8.2).

It evaluates scheduling throughput, gang placement atomicity, GPU fragmentation,
queue latency, and fault recovery against a simulated KWOK fleet of 500 to 2,000 nodes.
All results are labeled "simulated" in compliance with AGENTS.md §1.1.
*/
package runner

import (
	"time"
)

// BenchmarkMode denotes the scheduling engine being benchmarked.
type BenchmarkMode string

const (
	ModeDefaultScheduler BenchmarkMode = "default-kube-scheduler"
	ModeMeridian         BenchmarkMode = "meridian-scheduler"
)

// BenchmarkConfig defines fleet and workload parameters.
type BenchmarkConfig struct {
	Mode         BenchmarkMode `json:"mode"`
	NodeCount    int           `json:"nodeCount"`    // 500 or 2000
	GPUsPerNode  int           `json:"gpusPerNode"`  // typically 8
	JobCount     int           `json:"jobCount"`     // e.g. 500 jobs
	Seed         int64         `json:"seed"`
	Repetitions  int           `json:"repetitions"`  // >= 5 per methodology
	WarmupRuns   int           `json:"warmupRuns"`   // 1 warmup run discarded
}

// RunResult captures metrics from a single benchmark iteration.
type RunResult struct {
	Iteration              int           `json:"iteration"`
	Mode                   BenchmarkMode `json:"mode"`
	NodeCount              int           `json:"nodeCount"`
	TotalJobs              int           `json:"totalJobs"`
	TotalPods              int           `json:"totalPods"`
	TotalGPUs              int           `json:"totalGPUs"`
	Duration               time.Duration `json:"duration"`
	ThroughputPodsPerSec   float64       `json:"throughputPodsPerSec"`
	ThroughputJobsPerSec   float64       `json:"throughputJobsPerSec"`
	PeakAllocationRatio    float64       `json:"peakAllocationRatio"`
	AverageAllocationRatio float64       `json:"averageAllocationRatio"`
	FragmentationRatio     float64       `json:"fragmentationRatio"`
	MeanQueueWait          time.Duration `json:"meanQueueWait"`
	P95QueueWait           time.Duration `json:"p95QueueWait"`
	P99QueueWait           time.Duration `json:"p99QueueWait"`
	MeanGangStartLatency   time.Duration `json:"meanGangStartLatency"`
	P95GangStartLatency    time.Duration `json:"p95GangStartLatency"`
	P99GangStartLatency    time.Duration `json:"p99GangStartLatency"`
	PartiallyStartedGangs  int           `json:"partiallyStartedGangs"` // 0 for Meridian
	PreemptionsCount       int           `json:"preemptionsCount"`
	WastedGPUSecs          float64       `json:"wastedGPUSecs"`
}

// AggregateStats aggregates multiple benchmark repetitions (median, p5, p95).
type AggregateStats struct {
	Mode                   BenchmarkMode `json:"mode"`
	NodeCount              int           `json:"nodeCount"`
	Repetitions            int           `json:"repetitions"`
	ThroughputPodsPerSec   MetricSpread  `json:"throughputPodsPerSec"`
	PeakAllocationRatio    MetricSpread  `json:"peakAllocationRatio"`
	FragmentationRatio     MetricSpread  `json:"fragmentationRatio"`
	MeanQueueWaitSec       MetricSpread  `json:"meanQueueWaitSec"`
	P95QueueWaitSec        MetricSpread  `json:"p95QueueWaitSec"`
	P99QueueWaitSec        MetricSpread  `json:"p99QueueWaitSec"`
	MeanGangStartSec       MetricSpread  `json:"meanGangStartSec"`
	P95GangStartSec        MetricSpread  `json:"p95GangStartSec"`
	PartiallyStartedGangs  int           `json:"partiallyStartedGangs"` // Must be 0 for Meridian
	PreemptionsCount       int           `json:"preemptionsCount"`
	WastedGPUSecs          MetricSpread  `json:"wastedGPUSecs"`
}

// MetricSpread reports median and p5/p95 spread per §8.0 statistical rigor.
type MetricSpread struct {
	Median float64 `json:"median"`
	P5     float64 `json:"p5"`
	P95    float64 `json:"p95"`
}

// FaultBenchmarkResult records §8.2 fault-tolerance metrics under simulated GPU failure.
type FaultBenchmarkResult struct {
	TotalFaultsInjected int           `json:"totalFaultsInjected"`
	TimeToDetect        time.Duration `json:"timeToDetect"`        // e.g. DCGM scrape + XID classify
	TimeToReschedule    time.Duration `json:"timeToReschedule"`    // time to find new healthy nodes
	StepsLostVsLastAck  int           `json:"stepsLostVsLastAck"`  // training steps lost
	JobsResumedPct      float64       `json:"jobsResumedPct"`      // % of jobs successfully resumed
}
