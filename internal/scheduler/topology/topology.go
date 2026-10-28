/*
Package topology provides utilities for reading and comparing Meridian node topology labels.

Topology label contract (all labels are set by the cluster bootstrap Makefile):

  | Label                             | Example values          | Semantics                                         |
  |-----------------------------------|-------------------------|---------------------------------------------------|
  | meridian.dev/nvlink-domain        | "nvlink-0", "nvlink-1"  | NVLink-connected GPU peer group (highest BW)      |
  | meridian.dev/rack                 | "rack-0", "rack-1"      | ToR switch boundary; uniform bisectional BW       |
  | topology.kubernetes.io/zone       | "us-east-1a"            | Failure domain / AZ boundary                      |
  | meridian.dev/gpu-type             | "nvidia-h100"           | GPU model; must match GPUWorkload.Spec.GPUType    |
  | meridian.dev/health-score         | "100", "50"             | Integer 0-100; 100 = fully healthy                |

Distance levels (lowest = tightest proximity):
  0 - Same NVLink domain
  1 - Same rack, different NVLink domain
  2 - Same zone, different rack
  3 - Different zone (cross-AZ)
*/
package topology

const (
	// LabelNVLinkDomain identifies the NVLink peer domain for GPU interconnect affinity.
	LabelNVLinkDomain = "meridian.dev/nvlink-domain"

	// LabelRack identifies the top-of-rack switch boundary.
	LabelRack = "meridian.dev/rack"

	// LabelZone identifies the availability zone (matches topology.kubernetes.io/zone).
	LabelZone = "topology.kubernetes.io/zone"

	// LabelGPUType is the GPU model label matched against GPUWorkload.Spec.GPUType.
	LabelGPUType = "meridian.dev/gpu-type"

	// LabelHealthScore is an integer string [0,100] reflecting aggregate GPU node health.
	LabelHealthScore = "meridian.dev/health-score"

	// ResourceNvidiaGPU is the extended resource name used by the NVIDIA device plugin.
	ResourceNvidiaGPU = "nvidia.com/gpu"

	// MaxHealthScore is the score value for a fully healthy node with no GPU anomalies.
	MaxHealthScore = int64(100)
)

// Distance computes the topology distance level between two nodes identified by their labels.
//
// Returns:
//
//	0 if same NVLink domain (best placement for NVLink-speed interconnect)
//	1 if same rack but different NVLink domain
//	2 if same zone but different rack
//	3 if different zone (worst — cross-AZ latency penalty)
func Distance(aLabels, bLabels map[string]string) int {
	if aLabels[LabelNVLinkDomain] == bLabels[LabelNVLinkDomain] &&
		aLabels[LabelNVLinkDomain] != "" {
		return 0
	}
	if aLabels[LabelRack] == bLabels[LabelRack] && aLabels[LabelRack] != "" {
		return 1
	}
	if aLabels[LabelZone] == bLabels[LabelZone] && aLabels[LabelZone] != "" {
		return 2
	}
	return 3
}

// TopoScore converts a topology distance level into a [0, 100] score component.
//
// The scoring weights are aligned with the formulas in docs/design/scoring.md §3.2:
//
//	Distance 0 (same NVLink domain) → 100
//	Distance 1 (same rack)          → 70
//	Distance 2 (same zone)          → 30
//	Distance 3 (cross-zone)         → 0
func TopoScore(distance int) int64 {
	switch distance {
	case 0:
		return 100
	case 1:
		return 70
	case 2:
		return 30
	default:
		return 0
	}
}
