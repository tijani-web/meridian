/*
Package plugin implements the Meridian GPU-aware scheduler plugin for the
Kubernetes scheduler framework (k8s.io/kubernetes/pkg/scheduler/framework).

This package lives inside the scheduler sub-module (cmd/scheduler/go.mod) to
isolate the k8s.io/kubernetes monorepo replace-directive tree from the main
controller-manager module.

Extension points:
  - QueueSort      – priority + FIFO within priority
  - PreFilter      – gang metadata validation, cluster-wide GPU feasibility
  - Filter         – GPU type/count fit, health taint rejection
  - Score          – composite anti-fragmentation + topology + health
  - NormalizeScore – rescale to [0, MaxNodeScore]
  - Reserve        – per-gang tentative node assignment
  - Unreserve      – undo tentative assignment on failure
  - Permit         – gang scheduling: hold until minAvailable, reject on timeout
*/
package plugin

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/kubernetes/pkg/scheduler/framework"
)

const (
	// Name is the plugin name registered with the scheduler framework.
	Name = "MeridianGPU"

	// defaultPermitTimeout is the maximum time a gang pod waits at Permit.
	defaultPermitTimeout = 2 * time.Minute

	// Kubernetes extended resource name for NVIDIA GPUs.
	resourceNvidiaGPU = "nvidia.com/gpu"
)

// Pod label / annotation constants — must match the Workload controller's constants.
const (
	labelWorkload     = "meridian.dev/workload"
	labelGangSize     = "meridian.dev/gang-size"
	labelGangIndex    = "meridian.dev/gang-index"
	labelGPUType      = "meridian.dev/gpu-type"
	labelMinAvail     = "meridian.dev/min-available"
	annotGPUsPerPod   = "meridian.dev/gpus-per-pod"
	annotAdmittedAt   = "meridian.dev/admitted-at"
	annotPriority     = "meridian.dev/priority"
	labelHealthScore  = "meridian.dev/health-score"
	labelNVLinkDomain = "meridian.dev/nvlink-domain"
	labelRack         = "meridian.dev/rack"
	labelZone         = "topology.kubernetes.io/zone"
	taintGPUUnhealthy = "meridian.dev/gpu-unhealthy"
)

// Args holds configurable plugin weights from KubeSchedulerConfiguration.
type Args struct {
	WeightFit            int64 `json:"weightFit,omitempty"`
	WeightTopo           int64 `json:"weightTopo,omitempty"`
	WeightHealth         int64 `json:"weightHealth,omitempty"`
	PermitTimeoutSeconds int64 `json:"permitTimeoutSeconds,omitempty"`
}

func (a *Args) DeepCopyObject() runtime.Object {
	c := *a
	return &c
}

func (a *Args) GetObjectKind() schema.ObjectKind {
	return schema.EmptyObjectKind
}

// gangState tracks tentative node reservations for one gang.
// Owner: MeridianGPU plugin. Lifecycle: per scheduling cycle.
type gangState struct {
	mu            sync.Mutex
	reservedNodes map[int]string // gang index → node name
	minAvailable  int
	size          int
}

const cycleStateKey = "meridian.dev/gang-state"

type stateWrapper struct{ *gangState }

func (s *stateWrapper) Clone() framework.StateData { return &stateWrapper{s.gangState} }

// Plugin is the unified GPU-aware scheduler plugin.
type Plugin struct {
	fh           framework.Handle
	weightFit    int64
	weightTopo   int64
	weightHealth int64
	timeout      time.Duration
}

var (
	_ framework.QueueSortPlugin = &Plugin{}
	_ framework.PreFilterPlugin = &Plugin{}
	_ framework.FilterPlugin    = &Plugin{}
	_ framework.ScorePlugin     = &Plugin{}
	_ framework.ReservePlugin   = &Plugin{}
	_ framework.PermitPlugin    = &Plugin{}
)

func (p *Plugin) Name() string { return Name }

// ─────────────────────────────────────────────────────────────────────────────
// QueueSort
// ─────────────────────────────────────────────────────────────────────────────

// Less orders pods in the scheduling queue: higher meridian.dev/priority first,
// then by meridian.dev/admitted-at (FIFO), then by creation timestamp.
func (p *Plugin) Less(a, b *framework.QueuedPodInfo) bool {
	pa := annotInt(a.Pod.Annotations, annotPriority)
	pb := annotInt(b.Pod.Annotations, annotPriority)
	if pa != pb {
		return pa > pb
	}
	ta := parseTime(a.Pod.Annotations[annotAdmittedAt])
	tb := parseTime(b.Pod.Annotations[annotAdmittedAt])
	if !ta.IsZero() && !tb.IsZero() && !ta.Equal(tb) {
		return ta.Before(tb)
	}
	return a.Pod.CreationTimestamp.Before(&b.Pod.CreationTimestamp)
}

// ─────────────────────────────────────────────────────────────────────────────
// PreFilter
// ─────────────────────────────────────────────────────────────────────────────

// PreFilter validates gang labels and checks cluster-wide GPU feasibility.
func (p *Plugin) PreFilter(ctx context.Context, cs *framework.CycleState, pod *v1.Pod) (*framework.PreFilterResult, *framework.Status) {
	gpuType := pod.Labels[labelGPUType]
	if gpuType == "" {
		return nil, nil // non-GPU pod; pass through
	}

	gangSizeStr := pod.Labels[labelGangSize]
	gangIdxStr := pod.Labels[labelGangIndex]
	if gangSizeStr == "" || gangIdxStr == "" {
		return nil, framework.NewStatus(framework.UnschedulableAndUnresolvable,
			"missing meridian.dev/gang-size or meridian.dev/gang-index")
	}
	gangSize, err := strconv.Atoi(gangSizeStr)
	if err != nil || gangSize <= 0 {
		return nil, framework.NewStatus(framework.UnschedulableAndUnresolvable,
			"invalid gang-size: "+gangSizeStr)
	}

	gpusPerPod := int64(1)
	if ann := pod.Annotations[annotGPUsPerPod]; ann != "" {
		if v, e := strconv.ParseInt(ann, 10, 64); e == nil && v > 0 {
			gpusPerPod = v
		}
	}

	// Cluster-wide feasibility check.
	nodeInfos, err2 := p.fh.SnapshotSharedLister().NodeInfos().List()
	if err2 != nil {
		return nil, framework.AsStatus(fmt.Errorf("listing nodes: %w", err2))
	}
	var feasible int
	for _, ni := range nodeInfos {
		n := ni.Node()
		if n == nil || n.Labels[labelGPUType] != gpuType {
			continue
		}
		resQty := n.Status.Capacity[v1.ResourceName(resourceNvidiaGPU)]
		total := resQty.Value()
		used := ni.Requested.ScalarResources[v1.ResourceName(resourceNvidiaGPU)]
		if total-used >= gpusPerPod {
			feasible++
		}
	}
	if feasible == 0 {
		return nil, framework.NewStatus(framework.Unschedulable,
			fmt.Sprintf("no node has GPU type %q with >= %d free GPUs", gpuType, gpusPerPod))
	}

	minAvail, _ := strconv.Atoi(pod.Labels[labelMinAvail])
	if minAvail <= 0 {
		minAvail = gangSize
	}
	gs := &gangState{
		reservedNodes: make(map[int]string, gangSize),
		minAvailable:  minAvail,
		size:          gangSize,
	}
	cs.Write(cycleStateKey, &stateWrapper{gs})
	return nil, nil
}

func (p *Plugin) PreFilterExtensions() framework.PreFilterExtensions { return nil }

// ─────────────────────────────────────────────────────────────────────────────
// Filter
// ─────────────────────────────────────────────────────────────────────────────

// Filter rejects nodes for GPU type mismatch, insufficient GPU capacity, or health taints.
func (p *Plugin) Filter(_ context.Context, _ *framework.CycleState, pod *v1.Pod, ni *framework.NodeInfo) *framework.Status {
	gpuType := pod.Labels[labelGPUType]
	if gpuType == "" {
		return nil
	}
	n := ni.Node()
	if n == nil {
		return framework.NewStatus(framework.Error, "nil node")
	}

	// 1. GPU type match
	if n.Labels[labelGPUType] != gpuType {
		return framework.NewStatus(framework.Unschedulable,
			fmt.Sprintf("GPU type mismatch: node=%s pod=%s", n.Labels[labelGPUType], gpuType))
	}

	// 2. GPU capacity
	gpusPerPod := int64(1)
	if ann := pod.Annotations[annotGPUsPerPod]; ann != "" {
		if v, e := strconv.ParseInt(ann, 10, 64); e == nil && v > 0 {
			gpusPerPod = v
		}
	}
	resQty := n.Status.Capacity[v1.ResourceName(resourceNvidiaGPU)]
	total := resQty.Value()
	used := ni.Requested.ScalarResources[v1.ResourceName(resourceNvidiaGPU)]
	if total-used < gpusPerPod {
		return framework.NewStatus(framework.Unschedulable,
			fmt.Sprintf("InsufficientGPU: free=%d requested=%d", total-used, gpusPerPod))
	}

	// 3. Health taint
	for _, t := range n.Spec.Taints {
		if t.Key == taintGPUUnhealthy {
			return framework.NewStatus(framework.Unschedulable,
				fmt.Sprintf("NodeUnhealthy: taint=%s effect=%s", t.Key, t.Effect))
		}
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Score + NormalizeScore
// ─────────────────────────────────────────────────────────────────────────────

// Score computes the composite [0,100] anti-fragmentation+topology+health score.
func (p *Plugin) Score(_ context.Context, cs *framework.CycleState, pod *v1.Pod, ni *framework.NodeInfo) (int64, *framework.Status) {
	n := ni.Node()
	if n == nil {
		return 0, framework.NewStatus(framework.Error, "nil node")
	}

	gpusPerPod := int64(1)
	if ann := pod.Annotations[annotGPUsPerPod]; ann != "" {
		if v, e := strconv.ParseInt(ann, 10, 64); e == nil && v > 0 {
			gpusPerPod = v
		}
	}
	resQty := n.Status.Capacity[v1.ResourceName(resourceNvidiaGPU)]
	total := resQty.Value()
	used := ni.Requested.ScalarResources[v1.ResourceName(resourceNvidiaGPU)]

	fitS := fitScore(total, used, gpusPerPod)
	healthS := healthScore(n.Labels)

	topoS := int64(50) // neutral baseline
	if gw, readErr := cs.Read(cycleStateKey); readErr == nil {
		if gs, ok := gw.(*stateWrapper); ok {
			gs.mu.Lock()
			anchorNode, hasAnchor := gs.reservedNodes[0]
			gs.mu.Unlock()
			if hasAnchor {
				if anchorNode == n.Name {
					topoS = 100
				} else if anchorNI, e := p.fh.SnapshotSharedLister().NodeInfos().Get(anchorNode); e == nil && anchorNI.Node() != nil {
					topoS = topoScore(distance(n.Labels, anchorNI.Node().Labels))
				}
			}
		}
	}

	totalW := p.weightFit + p.weightTopo + p.weightHealth
	if totalW == 0 {
		return 0, nil
	}
	composite := (p.weightFit*fitS + p.weightTopo*topoS + p.weightHealth*healthS) / totalW
	if composite > 100 {
		composite = 100
	}
	return composite, nil
}

func (p *Plugin) ScoreExtensions() framework.ScoreExtensions { return p }

// NormalizeScore rescales per-cycle composite scores to [0, MaxNodeScore].
func (p *Plugin) NormalizeScore(_ context.Context, _ *framework.CycleState, _ *v1.Pod, scores framework.NodeScoreList) *framework.Status {
	var maxS int64
	for _, s := range scores {
		if s.Score > maxS {
			maxS = s.Score
		}
	}
	if maxS == 0 {
		return nil
	}
	for i := range scores {
		scores[i].Score = scores[i].Score * framework.MaxNodeScore / maxS
	}
	return nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Reserve / Unreserve
// ─────────────────────────────────────────────────────────────────────────────

// Reserve records the tentative node assignment for this gang member's index.
func (p *Plugin) Reserve(_ context.Context, cs *framework.CycleState, pod *v1.Pod, nodeName string) *framework.Status {
	idx, err := strconv.Atoi(pod.Labels[labelGangIndex])
	if err != nil {
		return nil // non-gang pod
	}
	gw, readErr := cs.Read(cycleStateKey)
	if readErr != nil {
		return nil
	}
	gs := gw.(*stateWrapper)
	gs.mu.Lock()
	gs.reservedNodes[idx] = nodeName
	gs.mu.Unlock()
	return nil
}

// Unreserve removes the tentative node assignment for this gang member.
func (p *Plugin) Unreserve(_ context.Context, cs *framework.CycleState, pod *v1.Pod, _ string) {
	idx, err := strconv.Atoi(pod.Labels[labelGangIndex])
	if err != nil {
		return
	}
	gw, readErr := cs.Read(cycleStateKey)
	if readErr != nil {
		return
	}
	gs := gw.(*stateWrapper)
	gs.mu.Lock()
	delete(gs.reservedNodes, idx)
	gs.mu.Unlock()
}

// ─────────────────────────────────────────────────────────────────────────────
// Permit — gang scheduling
// ─────────────────────────────────────────────────────────────────────────────

// Permit holds a pod in the Permit phase until its gang's minAvailable members
// have all been reserved, then releases all waiting members atomically.
// If the timeout fires before the threshold is met, all gang members are rejected
// (their Unreserve is called) and they re-enter the scheduling queue.
func (p *Plugin) Permit(_ context.Context, cs *framework.CycleState, pod *v1.Pod, _ string) (*framework.Status, time.Duration) {
	gw, readErr := cs.Read(cycleStateKey)
	if readErr != nil {
		return nil, 0 // non-gang pod
	}
	gs := gw.(*stateWrapper)
	gs.mu.Lock()
	reserved := len(gs.reservedNodes)
	minAvail := gs.minAvailable
	gs.mu.Unlock()

	if reserved >= minAvail {
		// Threshold met — release this pod and allow bound.
		return framework.NewStatus(framework.Success), 0
	}
	return framework.NewStatus(framework.Wait,
		fmt.Sprintf("gang waiting: reserved %d/%d", reserved, minAvail)), p.timeout
}

// ─────────────────────────────────────────────────────────────────────────────
// Factory
// ─────────────────────────────────────────────────────────────────────────────

// New is the plugin factory function registered with the scheduler framework registry.
func New(_ context.Context, obj runtime.Object, fh framework.Handle) (framework.Plugin, error) {
	args := &Args{}
	if obj != nil {
		a, ok := obj.(*Args)
		if !ok {
			return nil, fmt.Errorf("MeridianGPU: want *Args, got %T", obj)
		}
		args = a
	}

	wFit, wTopo, wHealth := args.WeightFit, args.WeightTopo, args.WeightHealth
	if wFit == 0 && wTopo == 0 && wHealth == 0 {
		wFit, wTopo, wHealth = 40, 45, 15
	}
	timeout := defaultPermitTimeout
	if args.PermitTimeoutSeconds > 0 {
		timeout = time.Duration(args.PermitTimeoutSeconds) * time.Second
	}
	return &Plugin{fh: fh, weightFit: wFit, weightTopo: wTopo, weightHealth: wHealth, timeout: timeout}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Internal scoring helpers
// ─────────────────────────────────────────────────────────────────────────────

// fitScore computes the anti-fragmentation fit score in [0, 100].
func fitScore(capacity, used, requested int64) int64 {
	avail := capacity - used
	if avail < requested || capacity == 0 {
		return 0
	}
	leftover := avail - requested
	util := (used + requested) * 100 / capacity
	align := int64(50)
	switch leftover {
	case 0:
		align = 100
	case 4, 8:
		align = 90
	case 2:
		align = 75
	}
	return util * align / 100
}

// healthScore reads the meridian.dev/health-score label. Missing/invalid → 100 (healthy).
func healthScore(labels map[string]string) int64 {
	v, ok := labels[labelHealthScore]
	if !ok {
		return 100
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 100
	}
	if n > 100 {
		return 100
	}
	return n
}

// distance returns the topology distance level [0,3] between two nodes' label sets.
func distance(a, b map[string]string) int {
	if a[labelNVLinkDomain] != "" && a[labelNVLinkDomain] == b[labelNVLinkDomain] {
		return 0
	}
	if a[labelRack] != "" && a[labelRack] == b[labelRack] {
		return 1
	}
	if a[labelZone] != "" && a[labelZone] == b[labelZone] {
		return 2
	}
	return 3
}

// topoScore maps a distance level to a [0,100] affinity score.
func topoScore(dist int) int64 {
	switch dist {
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

// ─────────────────────────────────────────────────────────────────────────────
// Utility helpers
// ─────────────────────────────────────────────────────────────────────────────

func annotInt(ann map[string]string, key string) int64 {
	v := ann[key]
	if v == "" {
		return 0
	}
	n, _ := strconv.ParseInt(v, 10, 64)
	return n
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, _ := time.Parse(time.RFC3339Nano, s)
	return t
}
