/*
Package explain implements the diagnostic explainability engine for Meridian.

It inspects live Kubernetes resources (GPUWorkload, GPUQueue, Nodes, Pods) to
provide actionable, structured, human-readable breakdowns for:
  - `meridianctl explain workload <name> -n <ns>`
  - `meridianctl explain inference <name> -n <ns>`
  - `meridianctl status cluster`
*/
package explain

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

const (
	// GateAdmission matches the admission scheduling gate name.
	GateAdmission = "meridian.dev/admission"
	// LabelWorkload matches the workload label applied to gang pods.
	LabelWorkload = "meridian.dev/workload"
	// TaintGPUUnhealthy matches the hardware fault node taint.
	TaintGPUUnhealthy = "meridian.dev/gpu-unhealthy"
	// ResourceGPU is the extended resource name for GPUs.
	ResourceGPU = "nvidia.com/gpu"
)

// Explainer gathers live cluster state and generates structured explanations.
type Explainer struct {
	client client.Client
}

// NewExplainer creates a fresh Explainer backed by a live controller-runtime client.
func NewExplainer(c client.Client) *Explainer {
	return &Explainer{client: c}
}

// ExplainWorkload diagnoses a GPUWorkload according to §6.8 — in strict diagnostic order:
// 1. Phase & Duration, 2. Queue Status, 3. Scheduling Gates, 4. Filter Rejections & Scoring,
// 5. Permit Gang Status, 6. Preemption / Recovery History.
func (e *Explainer) ExplainWorkload(ctx context.Context, namespace, name string) (*WorkloadExplanation, error) {
	workload := &meridianv1alpha1.GPUWorkload{}
	if err := e.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, workload); err != nil {
		return nil, fmt.Errorf("failed to get GPUWorkload %s/%s: %w", namespace, name, err)
	}

	exp := &WorkloadExplanation{
		Name:      workload.Name,
		Namespace: workload.Namespace,
		Phase:     string(workload.Status.Phase),
	}

	// 1. Phase Duration
	exp.PhaseDuration = calculatePhaseDuration(workload)

	// 2. Queue Status
	e.populateQueueStatus(ctx, workload, exp)

	// 3. Scheduling Gate Status & Pods
	pods := &corev1.PodList{}
	sel := labels.SelectorFromSet(map[string]string{LabelWorkload: workload.Name})
	if err := e.client.List(ctx, pods, client.InNamespace(namespace), client.MatchingLabelsSelector{Selector: sel}); err == nil {
		e.populateGateAndPods(pods.Items, exp)
	}

	// 4. Scheduling Rejections & Scoring Breakdown
	e.populateSchedulingAnalysis(ctx, workload, exp)

	// 5. Permit Gang Status
	e.populatePermitStatus(workload, exp)

	// 6. Preemption & Recovery History
	e.populateHistory(workload, exp)

	return exp, nil
}

// calculatePhaseDuration computes how long the workload has been in its current phase.
func calculatePhaseDuration(w *meridianv1alpha1.GPUWorkload) time.Duration {
	// Use the most recent transition record
	if len(w.Status.History) > 0 {
		last := w.Status.History[len(w.Status.History)-1]
		if !last.Time.IsZero() {
			return time.Since(last.Time.Time).Round(time.Second)
		}
	}
	if !w.CreationTimestamp.IsZero() {
		return time.Since(w.CreationTimestamp.Time).Round(time.Second)
	}
	return 0
}

// populateQueueStatus gathers queue quota and identifies blocking workloads.
func (e *Explainer) populateQueueStatus(ctx context.Context, w *meridianv1alpha1.GPUWorkload, exp *WorkloadExplanation) {
	exp.Queue.QueueName = w.Spec.QueueName
	if w.Spec.QueueName == "" {
		exp.Queue.BlockReason = "No queue specified (spec.queueName is empty)"
		exp.Queue.IsBlocked = true
		return
	}

	queue := &meridianv1alpha1.GPUQueue{}
	if err := e.client.Get(ctx, types.NamespacedName{Name: w.Spec.QueueName}, queue); err != nil {
		if errors.IsNotFound(err) {
			exp.Queue.IsBlocked = true
			exp.Queue.BlockReason = fmt.Sprintf("GPUQueue %q does not exist", w.Spec.QueueName)
		}
		return
	}

	exp.Queue.QuotaGuaranteed = totalNominal(queue.Spec.NominalQuota)
	exp.Queue.QuotaCeiling = totalCeiling(queue.Spec.NominalQuota, queue.Spec.BorrowingLimit)

	// Compute allocated from Usage breakdown
	allocated := int64(0)
	for _, usage := range queue.Status.Usage {
		allocated += int64(usage.Used)
	}
	exp.Queue.QuotaAllocated = allocated

	exp.Queue.Position = fmt.Sprintf("%d workloads pending in queue", queue.Status.PendingWorkloads)

	// Find workloads holding quota
	allWorkloads := &meridianv1alpha1.GPUWorkloadList{}
	if err := e.client.List(ctx, allWorkloads); err == nil {
		for _, other := range allWorkloads.Items {
			if other.Spec.QueueName == queue.Name && other.Name != w.Name {
				if other.Status.Phase == meridianv1alpha1.WorkloadPhaseRunning ||
					other.Status.Phase == meridianv1alpha1.WorkloadPhaseScheduling ||
					other.Status.Phase == meridianv1alpha1.WorkloadPhaseAdmitted {
					gpus := int64(other.Spec.Gang.Size) * int64(other.Spec.GPUsPerPod)
					exp.Queue.HoldingWorkloads = append(exp.Queue.HoldingWorkloads,
						fmt.Sprintf("%s/%s (%d GPUs, phase=%s)", other.Namespace, other.Name, gpus, other.Status.Phase))
				}
			}
		}
	}

	// Check if admission is quota-blocked
	requestedGPUs := int64(w.Spec.Gang.Size) * int64(w.Spec.GPUsPerPod)
	if w.Status.Phase == meridianv1alpha1.WorkloadPhaseQueued || w.Status.Phase == meridianv1alpha1.WorkloadPhasePending {
		if allocated+requestedGPUs > exp.Queue.QuotaCeiling {
			exp.Queue.IsBlocked = true
			exp.Queue.BlockReason = fmt.Sprintf("Requested %d GPUs exceeds ceiling quota (%d/%d currently allocated)",
				requestedGPUs, allocated, exp.Queue.QuotaCeiling)
		}
	}
}

// totalNominal sums all GPU type guaranteed quotas.
func totalNominal(nominal map[string]int32) int64 {
	total := int64(0)
	for _, v := range nominal {
		total += int64(v)
	}
	return total
}

// totalCeiling sums nominal + borrowing limit for an effective ceiling.
func totalCeiling(nominal, borrowing map[string]int32) int64 {
	total := totalNominal(nominal)
	for _, v := range borrowing {
		total += int64(v)
	}
	return total
}

// populateGateAndPods checks for admission gates on member pods and records placement.
func (e *Explainer) populateGateAndPods(pods []corev1.Pod, exp *WorkloadExplanation) {
	for i, pod := range pods {
		status := string(pod.Status.Phase)
		for _, gate := range pod.Spec.SchedulingGates {
			if gate.Name == GateAdmission {
				exp.Gated.IsGated = true
				exp.Gated.Gate = GateAdmission
				exp.Gated.Reason = "Workload waiting for queue quota admission before scheduling"
				status = "Gated(Admission)"
			}
		}

		exp.Pods = append(exp.Pods, PodPlacement{
			Name:      pod.Name,
			GangIndex: i,
			Node:      pod.Spec.NodeName,
			Status:    status,
		})
	}
}

// populateSchedulingAnalysis walks cluster nodes against workload requirements,
// grouping filter rejections and ranking candidate scores.
func (e *Explainer) populateSchedulingAnalysis(ctx context.Context, w *meridianv1alpha1.GPUWorkload, exp *WorkloadExplanation) {
	if w.Status.Phase != meridianv1alpha1.WorkloadPhaseScheduling &&
		w.Status.Phase != meridianv1alpha1.WorkloadPhaseQueued &&
		w.Status.Phase != meridianv1alpha1.WorkloadPhasePending {
		return
	}

	nodes := &corev1.NodeList{}
	if err := e.client.List(ctx, nodes); err != nil {
		return
	}

	exp.Scheduling.IsScheduling = true
	exp.Scheduling.RejectionsByNode = make(map[string]int)

	reqGPUType := w.Spec.GPUType
	reqGPUsPerPod := int64(w.Spec.GPUsPerPod)

	for _, node := range nodes.Items {
		// Skip control-plane nodes
		if _, isCP := node.Labels["node-role.kubernetes.io/control-plane"]; isCP {
			continue
		}

		// Check hardware health taint
		hasUnhealthyTaint := false
		for _, t := range node.Spec.Taints {
			if t.Key == TaintGPUUnhealthy {
				hasUnhealthyTaint = true
				break
			}
		}
		if hasUnhealthyTaint {
			exp.Scheduling.RejectionsByNode["unhealthy taint (meridian.dev/gpu-unhealthy)"]++
			continue
		}

		// Check GPU capacity
		allocatableGPUs := int64(0)
		if qty, ok := node.Status.Allocatable[corev1.ResourceName(ResourceGPU)]; ok {
			allocatableGPUs = qty.Value()
		}
		if allocatableGPUs < reqGPUsPerPod {
			exp.Scheduling.RejectionsByNode["insufficient nvidia.com/gpu"]++
			continue
		}

		// Check GPU type label
		nodeGPUType := node.Labels["meridian.dev/gpu-type"]
		if reqGPUType != "" && nodeGPUType != "" && nodeGPUType != reqGPUType {
			exp.Scheduling.RejectionsByNode[fmt.Sprintf("GPU type mismatch (want %s, node has %s)", reqGPUType, nodeGPUType)]++
			continue
		}

		// Surviving nodes are candidates — compute approximate composite score
		healthScore := int64(100)
		if hs, ok := node.Labels["meridian.dev/health-score"]; ok {
			fmt.Sscanf(hs, "%d", &healthScore) //nolint:errcheck
		}
		topoScore := int64(75)
		if w.Spec.Topology.Preference == meridianv1alpha1.TopologyPreferenceSameNVLinkDomain {
			topoScore = 90
		}
		fitScore := int64(80)
		if allocatableGPUs >= reqGPUsPerPod*2 {
			fitScore = 60 // anti-fragmentation: prefer tighter packing
		}
		composite := (fitScore + topoScore + healthScore) / 3

		exp.Scheduling.CandidateScores = append(exp.Scheduling.CandidateScores, CandidateScore{
			Node:           node.Name,
			FitScore:       fitScore,
			TopoScore:      topoScore,
			HealthScore:    healthScore,
			CompositeScore: composite,
		})
	}

	// Top 3 candidates by composite score descending
	sort.Slice(exp.Scheduling.CandidateScores, func(i, j int) bool {
		return exp.Scheduling.CandidateScores[i].CompositeScore > exp.Scheduling.CandidateScores[j].CompositeScore
	})
	if len(exp.Scheduling.CandidateScores) > 3 {
		exp.Scheduling.CandidateScores = exp.Scheduling.CandidateScores[:3]
	}

	if len(exp.Scheduling.CandidateScores) == 0 && len(exp.Scheduling.RejectionsByNode) > 0 {
		exp.Scheduling.FailureSummary = "All cluster worker nodes rejected during scheduler Filter phase"
	}
}

// populatePermitStatus evaluates gang reservation progress vs. required minimum.
func (e *Explainer) populatePermitStatus(w *meridianv1alpha1.GPUWorkload, exp *WorkloadExplanation) {
	required := int(w.Spec.Gang.MinAvailable)
	if required == 0 {
		required = int(w.Spec.Gang.Size)
	}
	exp.Permit.Required = required

	boundCount := 0
	for _, p := range exp.Pods {
		if p.Node != "" {
			boundCount++
		}
	}
	exp.Permit.Reserved = boundCount

	if w.Status.Phase == meridianv1alpha1.WorkloadPhaseScheduling && boundCount > 0 && boundCount < required {
		exp.Permit.IsPermitWaiting = true
		exp.Permit.RemainingTime = 90 * time.Second
	}
}

// populateHistory extracts ordered causal transition records from workload status.
func (e *Explainer) populateHistory(w *meridianv1alpha1.GPUWorkload, exp *WorkloadExplanation) {
	for _, h := range w.Status.History {
		exp.History = append(exp.History, HistoryExplanation{
			Seq:       h.Seq,
			Phase:     h.To,
			Reason:    h.Reason,
			Message:   h.Message,
			Timestamp: h.Time.Time,
		})
	}
}

// ExplainCluster diagnoses cluster-wide GPU utilization, fragmentation, and queues.
func (e *Explainer) ExplainCluster(ctx context.Context) (*ClusterStatus, error) {
	nodes := &corev1.NodeList{}
	if err := e.client.List(ctx, nodes); err != nil {
		return nil, fmt.Errorf("failed to list nodes: %w", err)
	}

	status := &ClusterStatus{}
	totalGPUs := int64(0)
	allocatedGPUs := int64(0)

	for _, n := range nodes.Items {
		if _, isCP := n.Labels["node-role.kubernetes.io/control-plane"]; isCP {
			continue
		}

		isUnhealthy := false
		for _, t := range n.Spec.Taints {
			if t.Key == TaintGPUUnhealthy {
				isUnhealthy = true
				break
			}
		}
		if isUnhealthy {
			status.UnhealthyNodes++
		} else {
			status.HealthyNodes++
		}

		if qty, ok := n.Status.Allocatable[corev1.ResourceName(ResourceGPU)]; ok {
			totalGPUs += qty.Value()
		}
	}

	// Fetch queue states
	queues := &meridianv1alpha1.GPUQueueList{}
	if err := e.client.List(ctx, queues); err == nil {
		for _, q := range queues.Items {
			qAllocated := int64(0)
			for _, usage := range q.Status.Usage {
				qAllocated += int64(usage.Used)
			}
			allocatedGPUs += qAllocated

			status.Queues = append(status.Queues, QueueStatusEntry{
				Name:       q.Name,
				Priority:   q.Spec.Priority,
				Allocated:  qAllocated,
				Guaranteed: totalNominal(q.Spec.NominalQuota),
				Ceiling:    totalCeiling(q.Spec.NominalQuota, q.Spec.BorrowingLimit),
				Pending:    int(q.Status.PendingWorkloads),
			})
		}
	}

	status.TotalGPUs = totalGPUs
	status.AllocatedGPUs = allocatedGPUs
	status.AvailableGPUs = totalGPUs - allocatedGPUs
	if status.AvailableGPUs < 0 {
		status.AvailableGPUs = 0
	}
	if totalGPUs > 0 {
		status.GPUUtilizationPct = (float64(allocatedGPUs) / float64(totalGPUs)) * 100.0
	}
	status.FragmentationRatio = 0.0 // TODO(phase-11): compute from node allocation data

	return status, nil
}

// ExplainInference diagnoses an InferenceDeployment.
func (e *Explainer) ExplainInference(ctx context.Context, namespace, name string) (*InferenceExplanation, error) {
	inf := &meridianv1alpha1.InferenceDeployment{}
	if err := e.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, inf); err != nil {
		return nil, fmt.Errorf("failed to get InferenceDeployment %s/%s: %w", namespace, name, err)
	}

	warmupGating := "Passed"
	for _, cond := range inf.Status.Conditions {
		if cond.Type == "Warming" && cond.Status == "True" {
			warmupGating = "In Progress (replicas warming up)"
		}
	}

	explanation := &InferenceExplanation{
		Name:            inf.Name,
		Namespace:       inf.Namespace,
		Model:           inf.Spec.Model.Name,
		TotalReplicas:   inf.Spec.Replicas,
		WarmupGating:    warmupGating,
	}

	// Compute healthy replicas and canary state from revision status
	for _, rev := range inf.Status.Revisions {
		explanation.HealthyReplicas += rev.ReadyReplicas
		explanation.Revisions = append(explanation.Revisions, InferenceRevision{
			Revision: rev.Name,
			Weight:   rev.Weight,
			Replicas: inf.Spec.Replicas,
			Ready:    rev.ReadyReplicas,
		})
		// Detect if a canary revision is active (non-100% weight and second revision)
		if rev.Weight > 0 && rev.Weight < 100 && rev.Name != inf.Name {
			explanation.CanaryActive = true
			explanation.CanaryWeight = rev.Weight
		}
	}

	if len(explanation.Revisions) == 0 {
		explanation.Revisions = []InferenceRevision{{
			Revision: inf.Name + "-stable",
			Weight:   100,
			Replicas: inf.Spec.Replicas,
			Ready:    explanation.HealthyReplicas,
		}}
	}

	explanation.Endpoints = []string{
		fmt.Sprintf("http://%s.%s.svc.cluster.local:%d/v1/chat/completions",
			inf.Name, inf.Namespace, inf.Spec.Runtime.Port),
	}

	return explanation, nil
}

// FormatWorkload formats a WorkloadExplanation as terminal-ready diagnostic text.
func FormatWorkload(w *WorkloadExplanation) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("=== Workload: %s (namespace: %s) ===\n", w.Name, w.Namespace))
	sb.WriteString(fmt.Sprintf("Phase: %s  (duration: %s)\n\n", w.Phase, w.PhaseDuration))

	// 1. Queue
	sb.WriteString("1. Queue & Quota:\n")
	sb.WriteString(fmt.Sprintf("   Queue:      %s\n", w.Queue.QueueName))
	if w.Queue.Position != "" {
		sb.WriteString(fmt.Sprintf("   Position:   %s\n", w.Queue.Position))
	}
	sb.WriteString(fmt.Sprintf("   Quota:      %d allocated / %d guaranteed (ceiling %d)\n",
		w.Queue.QuotaAllocated, w.Queue.QuotaGuaranteed, w.Queue.QuotaCeiling))
	if w.Queue.IsBlocked {
		sb.WriteString(fmt.Sprintf("   BLOCKED:    %s\n", w.Queue.BlockReason))
	} else {
		sb.WriteString("   Admitted:   Quota available\n")
	}
	if len(w.Queue.HoldingWorkloads) > 0 {
		sb.WriteString("   Holding Quota:\n")
		for _, hw := range w.Queue.HoldingWorkloads {
			sb.WriteString(fmt.Sprintf("     - %s\n", hw))
		}
	}
	sb.WriteString("\n")

	// 2. Gate
	sb.WriteString("2. Scheduling Gate:\n")
	if w.Gated.IsGated {
		sb.WriteString(fmt.Sprintf("   Gated: YES  gate=%s\n", w.Gated.Gate))
		sb.WriteString(fmt.Sprintf("   Reason: %s\n", w.Gated.Reason))
	} else {
		sb.WriteString("   Gated: NO  (gate released or no gate present)\n")
	}
	sb.WriteString("\n")

	// 3. Scheduling
	sb.WriteString("3. Scheduler Filter & Score:\n")
	if len(w.Scheduling.RejectionsByNode) > 0 {
		sb.WriteString("   Rejections:\n")
		for reason, count := range w.Scheduling.RejectionsByNode {
			sb.WriteString(fmt.Sprintf("     %d nodes: %s\n", count, reason))
		}
	} else if !w.Scheduling.IsScheduling {
		sb.WriteString("   (not in scheduling phase)\n")
	}
	if len(w.Scheduling.CandidateScores) > 0 {
		sb.WriteString("   Top Candidates:\n")
		for _, c := range w.Scheduling.CandidateScores {
			sb.WriteString(fmt.Sprintf("     %s: composite=%d (fit=%d, topo=%d, health=%d)\n",
				c.Node, c.CompositeScore, c.FitScore, c.TopoScore, c.HealthScore))
		}
	}
	if w.Scheduling.FailureSummary != "" {
		sb.WriteString(fmt.Sprintf("   FAILURE: %s\n", w.Scheduling.FailureSummary))
	}
	sb.WriteString("\n")

	// 4. Permit
	sb.WriteString("4. Gang Permit Status:\n")
	if w.Permit.IsPermitWaiting {
		sb.WriteString(fmt.Sprintf("   Waiting at Permit: %d/%d reserved\n", w.Permit.Reserved, w.Permit.Required))
		sb.WriteString(fmt.Sprintf("   Timeout Remaining: %s\n", w.Permit.RemainingTime))
	} else {
		sb.WriteString(fmt.Sprintf("   Placement: %d/%d placed\n", w.Permit.Reserved, w.Permit.Required))
	}
	sb.WriteString("\n")

	// 5. History
	sb.WriteString("5. Preemption & Recovery History:\n")
	if len(w.History) == 0 {
		sb.WriteString("   (no preemption or recovery events)\n")
	} else {
		for _, h := range w.History {
			sb.WriteString(fmt.Sprintf("   [seq=%d] %s  %s -> %s  %s: %s\n",
				h.Seq, h.Timestamp.Format("15:04:05"), "(prev)", h.Phase, h.Reason, h.Message))
		}
	}

	return sb.String()
}

// FormatCluster formats ClusterStatus into a terminal dashboard.
func FormatCluster(c *ClusterStatus) string {
	var sb strings.Builder

	sb.WriteString("=== Meridian Cluster GPU Status ===\n")
	sb.WriteString(fmt.Sprintf("Total GPUs:          %d\n", c.TotalGPUs))
	sb.WriteString(fmt.Sprintf("Allocated GPUs:      %d (%.1f%% utilization)\n", c.AllocatedGPUs, c.GPUUtilizationPct))
	sb.WriteString(fmt.Sprintf("Available GPUs:      %d\n", c.AvailableGPUs))
	sb.WriteString(fmt.Sprintf("Fragmentation Ratio: %.2f\n", c.FragmentationRatio))
	sb.WriteString(fmt.Sprintf("Node Health:         %d healthy / %d unhealthy\n\n", c.HealthyNodes, c.UnhealthyNodes))

	sb.WriteString("Queue Summary:\n")
	sb.WriteString(fmt.Sprintf("%-20s %-8s %-10s %-10s %-10s %-8s\n",
		"NAME", "PRIORITY", "ALLOCATED", "GUARANTEED", "CEILING", "PENDING"))
	sb.WriteString(strings.Repeat("-", 70) + "\n")
	for _, q := range c.Queues {
		sb.WriteString(fmt.Sprintf("%-20s %-8d %-10d %-10d %-10d %-8d\n",
			q.Name, q.Priority, q.Allocated, q.Guaranteed, q.Ceiling, q.Pending))
	}

	return sb.String()
}

// FormatInference formats InferenceExplanation into human-readable text.
func FormatInference(inf *InferenceExplanation) string {
	var sb strings.Builder

	sb.WriteString(fmt.Sprintf("=== Inference: %s (namespace: %s) ===\n", inf.Name, inf.Namespace))
	sb.WriteString(fmt.Sprintf("Model:           %s\n", inf.Model))
	sb.WriteString(fmt.Sprintf("Replicas:        %d/%d ready\n", inf.HealthyReplicas, inf.TotalReplicas))
	sb.WriteString(fmt.Sprintf("Warm-up Gating:  %s\n", inf.WarmupGating))
	if inf.CanaryActive {
		sb.WriteString(fmt.Sprintf("Canary:          ACTIVE (weight=%d%%)\n", inf.CanaryWeight))
	} else {
		sb.WriteString("Canary:          Inactive (100% stable)\n")
	}

	if len(inf.Revisions) > 0 {
		sb.WriteString("\nRevisions:\n")
		for _, rev := range inf.Revisions {
			sb.WriteString(fmt.Sprintf("  %-30s weight=%-4d%% ready=%d/%d\n",
				rev.Revision, rev.Weight, rev.Ready, rev.Replicas))
		}
	}

	if len(inf.Endpoints) > 0 {
		sb.WriteString("\nEndpoints:\n")
		for _, ep := range inf.Endpoints {
			sb.WriteString(fmt.Sprintf("  %s\n", ep))
		}
	}

	return sb.String()
}
