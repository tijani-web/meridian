/*
Package tracegen implements synthetic GPU workload trace generation and trace replay
for Meridian scheduler and admission benchmarks (§8.1).

It supports:
  - Fixed-seed pseudo-random generation for deterministic, reproducible benchmarks.
  - Realistic GPU demand distributions: 1, 2, 4, 8 GPUs per pod, and gang sizes from 1 to 16 pods (total 1 to 64 GPUs).
  - Multi-queue assignment and multi-priority distributions (Batch, Normal, Interactive, High).
  - Trace replay mode reading user-supplied JSON or CSV traces.
  - Output as Kubernetes GPUWorkload YAML/JSON manifests or raw benchmark trace records.
*/
package tracegen

import (
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"os"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

// JobSpec represents an individual job in a synthetic or replayed benchmark trace.
type JobSpec struct {
	ID             string        `json:"id"`
	Name           string        `json:"name"`
	Namespace      string        `json:"namespace"`
	Queue          string        `json:"queue"`
	Priority       int32         `json:"priority"`
	Replicas       int32         `json:"replicas"`
	MinAvailable   int32         `json:"minAvailable"`
	GPUsPerReplica int64         `json:"gpusPerReplica"`
	TotalGPUs      int64         `json:"totalGPUs"`
	Duration       time.Duration `json:"duration"`
	ArrivalOffset  time.Duration `json:"arrivalOffset"`
	Topology       string        `json:"topology,omitempty"`
}

// Config defines the parameters for synthetic trace generation.
type Config struct {
	Seed          int64         `json:"seed"`
	JobCount      int           `json:"jobCount"`
	ArrivalRate   float64       `json:"arrivalRate"` // jobs per second (Poisson parameter lambda)
	Queues        []string      `json:"queues"`
	Namespace     string        `json:"namespace"`
	MaxGangPods   int32         `json:"maxGangPods"`
	IncludeCanary bool          `json:"includeCanary"`
}

// DefaultConfig returns recommended defaults per Meridian §8.1.
func DefaultConfig() Config {
	return Config{
		Seed:        42,
		JobCount:    100,
		ArrivalRate: 2.0, // 2 jobs/sec average arrival
		Queues:      []string{"team-nlp", "team-vision", "team-rl"},
		Namespace:   "default",
		MaxGangPods: 16,
	}
}

// Generator generates deterministic benchmark traces.
type Generator struct {
	cfg Config
	rng *rand.Rand
}

// NewGenerator constructs a generator with the given configuration.
func NewGenerator(cfg Config) *Generator {
	if cfg.Seed == 0 {
		cfg.Seed = 42
	}
	if len(cfg.Queues) == 0 {
		cfg.Queues = []string{"team-nlp", "team-vision", "team-rl"}
	}
	if cfg.Namespace == "" {
		cfg.Namespace = "default"
	}
	if cfg.MaxGangPods <= 0 {
		cfg.MaxGangPods = 16
	}

	return &Generator{
		cfg: cfg,
		rng: rand.New(rand.NewSource(cfg.Seed)),
	}
}

// Generate creates a list of JobSpecs according to the configured distributions.
func (g *Generator) Generate() []JobSpec {
	jobs := make([]JobSpec, g.cfg.JobCount)
	var currentOffset time.Duration

	for i := 0; i < g.cfg.JobCount; i++ {
		interArrival := time.Duration(g.rng.ExpFloat64() / g.cfg.ArrivalRate * float64(time.Second))
		currentOffset += interArrival

		gpus := g.sampleGPUsPerReplica()
		replicas := g.sampleReplicas()
		priority := g.samplePriority()
		queue := g.cfg.Queues[g.rng.Intn(len(g.cfg.Queues))]

		durationSec := 15.0 + g.rng.ExpFloat64()*45.0
		if durationSec > 600 {
			durationSec = 600
		}
		duration := time.Duration(durationSec) * time.Second

		totalGPUs := int64(replicas) * gpus
		var topology string
		if totalGPUs >= 8 && g.rng.Float64() < 0.60 {
			topology = "SameNVLinkDomain"
		}

		jobs[i] = JobSpec{
			ID:             fmt.Sprintf("job-%04d", i+1),
			Name:           fmt.Sprintf("trace-%s-g%d-r%d-%04d", queue, gpus, replicas, i+1),
			Namespace:      g.cfg.Namespace,
			Queue:          queue,
			Priority:       priority,
			Replicas:       replicas,
			MinAvailable:   replicas,
			GPUsPerReplica: gpus,
			TotalGPUs:      totalGPUs,
			Duration:       duration,
			ArrivalOffset:  currentOffset,
			Topology:       topology,
		}
	}

	return jobs
}

func (g *Generator) sampleGPUsPerReplica() int64 {
	r := g.rng.Float64()
	switch {
	case r < 0.40:
		return 1
	case r < 0.60:
		return 2
	case r < 0.85:
		return 4
	default:
		return 8
	}
}

func (g *Generator) sampleReplicas() int32 {
	r := g.rng.Float64()
	switch {
	case r < 0.45:
		return 1
	case r < 0.75:
		return int32(2 + g.rng.Intn(3))
	case r < 0.90:
		return 8
	default:
		return g.cfg.MaxGangPods
	}
}

func (g *Generator) samplePriority() int32 {
	r := g.rng.Float64()
	switch {
	case r < 0.25:
		return 10
	case r < 0.75:
		return 50
	default:
		return 100
	}
}

// ToGPUWorkload converts a JobSpec into a real Kubernetes GPUWorkload custom resource.
func (j *JobSpec) ToGPUWorkload() *meridianv1alpha1.GPUWorkload {
	gpuQty := resource.MustParse(fmt.Sprintf("%d", j.GPUsPerReplica))

	labels := map[string]string{
		"meridian.dev/queue":     j.Queue,
		"meridian.dev/job-id":    j.ID,
		"meridian.dev/benchmark": "simulated",
	}

	annotations := map[string]string{
		"meridian.dev/expected-duration": j.Duration.String(),
	}

	topo := meridianv1alpha1.TopologySpec{
		Preference: meridianv1alpha1.TopologyPreferenceNone,
	}
	if j.Topology != "" {
		topo.Preference = meridianv1alpha1.TopologyPreference(j.Topology)
		topo.Required = true
	}

	return &meridianv1alpha1.GPUWorkload{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "meridian.dev/v1alpha1",
			Kind:       "GPUWorkload",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:        j.Name,
			Namespace:   j.Namespace,
			Labels:      labels,
			Annotations: annotations,
		},
		Spec: meridianv1alpha1.GPUWorkloadSpec{
			QueueName:  j.Queue,
			Kind:       meridianv1alpha1.WorkloadKindTraining,
			GPUType:    "nvidia-h100",
			GPUsPerPod: int32(j.GPUsPerReplica),
			Gang: meridianv1alpha1.GangSpec{
				Size:         j.Replicas,
				MinAvailable: j.MinAvailable,
			},
			Topology: topo,
			PodTemplate: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{
					Labels: labels,
				},
				Spec: corev1.PodSpec{
					SchedulerName: "meridian-scheduler",
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{
						{
							Name:  "trainer",
							Image: "ghcr.io/tijani-web/meridian-trainer:v0.1.0",
							Resources: corev1.ResourceRequirements{
								Limits: corev1.ResourceList{
									corev1.ResourceName("nvidia.com/gpu"): gpuQty,
								},
								Requests: corev1.ResourceList{
									corev1.ResourceName("nvidia.com/gpu"): gpuQty,
								},
							},
						},
					},
				},
			},
		},
	}
}

// LoadTrace reads a user-supplied trace file (JSON format) for replay mode.
func LoadTrace(r io.Reader) ([]JobSpec, error) {
	var jobs []JobSpec
	dec := json.NewDecoder(r)
	if err := dec.Decode(&jobs); err != nil {
		return nil, fmt.Errorf("failed to decode trace JSON: %w", err)
	}
	return jobs, nil
}

// SaveTrace writes jobs to a file or stream in JSON format.
func SaveTrace(w io.Writer, jobs []JobSpec) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(jobs)
}

// LoadTraceFile opens and reads a JSON trace from path.
func LoadTraceFile(path string) ([]JobSpec, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open trace file: %w", err)
	}
	defer f.Close()
	return LoadTrace(f)
}
