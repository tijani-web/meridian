/*
Package metrics provides Prometheus instrumentation for Meridian's control and data planes.

It implements the metrics catalog specified in docs/design/observability.md, providing
thread-safe, high-performance telemetry for workload admission, gang scheduling invariants,
inference router TTFT/SLOs, GPU hardware health, and checkpoint-coordinated preemption.
*/
package metrics

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	crmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	defaultMetrics *Metrics
	defaultOnce    sync.Once
)

// Metrics encapsulates all Prometheus collectors registered for Meridian.
// It allows both isolated instances for testing (avoiding global collision) and
// a shared default instance integrated into the controller-runtime manager metrics endpoint.
type Metrics struct {
	// Workload Admission
	WorkloadAdmissionLatencySeconds *prometheus.HistogramVec
	WorkloadAdmissionTotal          *prometheus.CounterVec
	WorkloadPreemptionsTotal        *prometheus.CounterVec
	WorkloadPhaseTransitionsTotal   *prometheus.CounterVec
	QueueDepth                      *prometheus.GaugeVec
	QueueGPUAllocated               *prometheus.GaugeVec

	// Gang Scheduling
	GangStartLatencySeconds *prometheus.HistogramVec
	GangPartialStartsTotal  *prometheus.CounterVec
	GangPermitWaitSeconds   *prometheus.HistogramVec

	// Inference Router
	RouterRequestTotal        *prometheus.CounterVec
	RouterTTFTSeconds         *prometheus.HistogramVec
	RouterTokensGeneratedTotal *prometheus.CounterVec
	RouterActiveRequests      *prometheus.GaugeVec
	RouterKVCacheUsage        *prometheus.GaugeVec
	RouterPrefixCacheHitsTotal *prometheus.CounterVec

	// Node Health & GPU
	GPUXIDEventsTotal           *prometheus.CounterVec
	GPUECCDBETotal              *prometheus.CounterVec
	GPUTemperatureCelsius       *prometheus.GaugeVec
	GPUHealthy                  *prometheus.GaugeVec
	NodeRecoveryDurationSeconds *prometheus.HistogramVec

	// Checkpoint & Preemption
	CheckpointRequestTotal    *prometheus.CounterVec
	CheckpointAckLatencySeconds *prometheus.HistogramVec
	CheckpointTimeoutTotal    *prometheus.CounterVec
}

// NewMetrics initializes and registers a new Metrics collector bundle against the provided Prometheus registerer.
// If reg is nil, collectors are created without registration (useful for headless testing).
func NewMetrics(reg prometheus.Registerer) *Metrics {
	m := &Metrics{
		// Workload Admission
		WorkloadAdmissionLatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "meridian",
				Subsystem: "workload",
				Name:      "admission_latency_seconds",
				Help:      "Latency in seconds from workload creation/submission to scheduling gate release.",
				Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0},
			},
			[]string{"queue", "priority"},
		),
		WorkloadAdmissionTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "workload",
				Name:      "admission_total",
				Help:      "Total count of workload admission decisions partitioned by queue, priority class, and outcome result.",
			},
			[]string{"queue", "priority", "result"},
		),
		WorkloadPreemptionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "workload",
				Name:      "preemptions_total",
				Help:      "Total count of workload preemptions partitioned by queue and reason.",
			},
			[]string{"queue", "reason"},
		),
		WorkloadPhaseTransitionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "workload",
				Name:      "phase_transitions_total",
				Help:      "Total count of workload FSM phase transitions.",
			},
			[]string{"from_phase", "to_phase"},
		),
		QueueDepth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "meridian",
				Subsystem: "queue",
				Name:      "depth",
				Help:      "Current number of pending workloads awaiting admission per queue and priority class.",
			},
			[]string{"queue", "priority"},
		),
		QueueGPUAllocated: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "meridian",
				Subsystem: "queue",
				Name:      "gpu_allocated",
				Help:      "Current number of physical GPUs allocated to running workloads per queue and GPU model.",
			},
			[]string{"queue", "gpu_type"},
		),

		// Gang Scheduling
		GangStartLatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "meridian",
				Subsystem: "gang",
				Name:      "start_latency_seconds",
				Help:      "Latency in seconds from gate release until all pods in the gang reach Running state.",
				Buckets:   []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 15.0, 20.0, 30.0, 45.0, 60.0, 120.0},
			},
			[]string{"queue", "gang_size"},
		),
		GangPartialStartsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "gang",
				Name:      "partial_starts_total",
				Help:      "Critical safety invariant: count of partially-started gangs. Must always remain 0 in production.",
			},
			[]string{"queue"},
		),
		GangPermitWaitSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "meridian",
				Subsystem: "gang",
				Name:      "permit_wait_seconds",
				Help:      "Time spent waiting in the scheduler Permit plugin phase before all gang members are ready.",
				Buckets:   []float64{0.05, 0.1, 0.25, 0.5, 1.0, 2.0, 5.0, 10.0, 30.0},
			},
			[]string{"queue"},
		),

		// Inference Router
		RouterRequestTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "router",
				Name:      "request_total",
				Help:      "Total count of incoming inference requests partitioned by dispatch strategy, priority, and outcome.",
			},
			[]string{"strategy", "priority", "result"},
		),
		RouterTTFTSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "meridian",
				Subsystem: "router",
				Name:      "ttft_seconds",
				Help:      "Time-To-First-Token (TTFT) latency in seconds measured from request arrival to first streamed chunk.",
				Buckets:   []float64{0.005, 0.01, 0.025, 0.05, 0.075, 0.1, 0.15, 0.2, 0.3, 0.5, 1.0, 2.0},
			},
			[]string{"strategy", "replica"},
		),
		RouterTokensGeneratedTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "router",
				Name:      "tokens_generated_total",
				Help:      "Total number of output tokens streamed to clients per strategy and upstream replica.",
			},
			[]string{"strategy", "replica"},
		),
		RouterActiveRequests: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "meridian",
				Subsystem: "router",
				Name:      "active_requests",
				Help:      "Current count of active in-flight requests processed by the router per strategy.",
			},
			[]string{"strategy"},
		),
		RouterKVCacheUsage: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "meridian",
				Subsystem: "router",
				Name:      "kv_cache_usage",
				Help:      "Scraped KV cache utilization fraction (0.0 to 1.0) reported by upstream vLLM inference replicas.",
			},
			[]string{"replica"},
		),
		RouterPrefixCacheHitsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "router",
				Name:      "prefix_cache_hits_total",
				Help:      "Count of requests routed with a high prefix-affinity cache hit.",
			},
			[]string{"strategy", "replica"},
		),

		// Node Health & GPU
		GPUXIDEventsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "gpu",
				Name:      "xid_events_total",
				Help:      "Count of NVIDIA GPU XID driver/hardware error events detected by node health monitoring.",
			},
			[]string{"node", "gpu", "xid", "severity"},
		),
		GPUECCDBETotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "gpu",
				Name:      "ecc_dbe_total",
				Help:      "Count of double-bit uncorrectable ECC memory errors detected on GPU dies.",
			},
			[]string{"node", "gpu"},
		),
		GPUTemperatureCelsius: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "meridian",
				Subsystem: "gpu",
				Name:      "temperature_celsius",
				Help:      "Current GPU die temperature in degrees Celsius.",
			},
			[]string{"node", "gpu"},
		),
		GPUHealthy: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Namespace: "meridian",
				Subsystem: "gpu",
				Name:      "healthy",
				Help:      "GPU health indicator: 1 if healthy, 0 if tainted or degraded.",
			},
			[]string{"node", "gpu"},
		),
		NodeRecoveryDurationSeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "meridian",
				Subsystem: "node",
				Name:      "recovery_duration_seconds",
				Help:      "Time in seconds from XID/ECC fault detection until workload is rescheduled and evacuated.",
				Buckets:   []float64{1.0, 5.0, 10.0, 30.0, 60.0, 120.0, 300.0, 600.0},
			},
			[]string{"node"},
		),

		// Checkpoint & Preemption
		CheckpointRequestTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "checkpoint",
				Name:      "request_total",
				Help:      "Total checkpoint snapshot requests sent to training gangs prior to preemption.",
			},
			[]string{"queue"},
		),
		CheckpointAckLatencySeconds: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Namespace: "meridian",
				Subsystem: "checkpoint",
				Name:      "ack_latency_seconds",
				Help:      "Time in seconds from checkpoint dispatch until all gang ranks acknowledge snapshot completion.",
				Buckets:   []float64{0.1, 0.5, 1.0, 2.0, 5.0, 10.0, 20.0, 30.0, 60.0},
			},
			[]string{"queue"},
		),
		CheckpointTimeoutTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Namespace: "meridian",
				Subsystem: "checkpoint",
				Name:      "timeout_total",
				Help:      "Count of checkpoint acknowledgment timeouts forcing involuntary pod termination.",
			},
			[]string{"queue"},
		),
	}

	if reg != nil {
		reg.MustRegister(
			m.WorkloadAdmissionLatencySeconds,
			m.WorkloadAdmissionTotal,
			m.WorkloadPreemptionsTotal,
			m.WorkloadPhaseTransitionsTotal,
			m.QueueDepth,
			m.QueueGPUAllocated,

			m.GangStartLatencySeconds,
			m.GangPartialStartsTotal,
			m.GangPermitWaitSeconds,

			m.RouterRequestTotal,
			m.RouterTTFTSeconds,
			m.RouterTokensGeneratedTotal,
			m.RouterActiveRequests,
			m.RouterKVCacheUsage,
			m.RouterPrefixCacheHitsTotal,

			m.GPUXIDEventsTotal,
			m.GPUECCDBETotal,
			m.GPUTemperatureCelsius,
			m.GPUHealthy,
			m.NodeRecoveryDurationSeconds,

			m.CheckpointRequestTotal,
			m.CheckpointAckLatencySeconds,
			m.CheckpointTimeoutTotal,
		)
	}

	return m
}

// Default returns the process-wide Metrics singleton registered with controller-runtime's
// global metrics registry, ensuring all Meridian controllers automatically expose metrics
// on the manager's /metrics HTTP endpoint.
func Default() *Metrics {
	defaultOnce.Do(func() {
		defaultMetrics = NewMetrics(crmetrics.Registry)
	})
	return defaultMetrics
}
