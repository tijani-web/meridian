/*
Package agent implements the Meridian GPU node health monitoring agent daemon.

The agent runs as a DaemonSet pod on every GPU worker node. It continuously polls
hardware telemetry sources (NVML and DCGM exporter), applies flap damping and hysteresis,
and reconciles node conditions and taints on the local Kubernetes Node resource.
*/
package agent

import (
	"context"
	"fmt"
	"sync"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/klog/v2"

	"github.com/tijani-web/meridian/internal/agent/health"
	"github.com/tijani-web/meridian/internal/metrics"
)

// Config parameters for the Node Health Agent daemon.
type Config struct {
	NodeName     string
	PollInterval time.Duration
	Damping      health.FlapDampingConfig
}

// Agent coordinates GPU telemetry polling, flap damping, and Node status patching.
type Agent struct {
	config  Config
	kube    kubernetes.Interface
	sources []health.HealthSource
	tracker *health.DampingTracker

	mu             sync.Mutex
	lastCondition  *corev1.NodeCondition
	lastTaintAdded string
}

// NewAgent constructs a new node health agent.
func NewAgent(kube kubernetes.Interface, cfg Config, sources ...health.HealthSource) *Agent {
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = 5 * time.Second
	}

	return &Agent{
		config:  cfg,
		kube:    kube,
		sources: sources,
		tracker: health.NewDampingTracker(cfg.Damping),
	}
}

// Run executes the evaluation loop until the context is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	klog.InfoS("Starting Meridian GPU health agent", "node", a.config.NodeName, "pollInterval", a.config.PollInterval)

	ticker := time.NewTicker(a.config.PollInterval)
	defer ticker.Stop()

	// Initial evaluation
	if err := a.evaluate(ctx); err != nil {
		klog.ErrorS(err, "Initial node health evaluation failed", "node", a.config.NodeName)
	}

	for {
		select {
		case <-ctx.Done():
			klog.InfoS("Stopping Meridian GPU health agent", "node", a.config.NodeName)
			return nil
		case <-ticker.C:
			if err := a.evaluate(ctx); err != nil {
				klog.ErrorS(err, "Health evaluation error", "node", a.config.NodeName)
			}
		}
	}
}

// evaluate queries all sources, aggregates fault status, applies damping, and updates node.
func (a *Agent) evaluate(ctx context.Context) error {
	now := time.Now().UTC()
	var allStatuses []health.GPUStatus

	for _, src := range a.sources {
		statuses, err := src.CheckHealth(ctx)
		if err != nil {
			klog.V(2).InfoS("Telemetry source reported error", "source", src.Name(), "error", err)
			continue
		}
		allStatuses = append(allStatuses, statuses...)
	}

	// Determine worst-case observation
	observedHealthy := true
	worstSeverity := health.SeverityHealthy
	worstReason := "AllGPUsHealthy"
	worstMsg := "All on-node GPU devices are operating normally"

	for _, s := range allStatuses {
		gpuID := fmt.Sprintf("gpu-%d", s.Index)
		if s.UUID != "" {
			gpuID = s.UUID
		}
		metrics.Default().SetGPUHealthy(a.config.NodeName, gpuID, s.Healthy)
		if s.TemperatureCelsius > 0 {
			metrics.Default().SetGPUTemperature(a.config.NodeName, gpuID, s.TemperatureCelsius)
		}
		if s.XID > 0 {
			metrics.Default().RecordXIDEvent(a.config.NodeName, gpuID, fmt.Sprintf("%d", s.XID), string(s.Severity))
		}
		if s.ECCUncorrectableErrors > 0 {
			metrics.Default().RecordECCDBE(a.config.NodeName, gpuID)
		}

		if !s.Healthy {
			observedHealthy = false
			if s.Severity == health.SeverityFatal {
				worstSeverity = health.SeverityFatal
				worstReason = s.ErrorReason
				worstMsg = s.ErrorMessage
				break // Fatal takes precedence
			} else if s.Severity == health.SeverityRecoverable && worstSeverity != health.SeverityFatal {
				worstSeverity = health.SeverityRecoverable
				worstReason = s.ErrorReason
				worstMsg = s.ErrorMessage
			}
		}
	}

	// Apply hysteresis and flap damping
	effectiveHealthy, quarantined := a.tracker.RecordEvaluation(observedHealthy, now)

	if quarantined {
		worstSeverity = health.SeverityFatal
		worstReason = "NodeFlapQuarantine"
		worstMsg = "Node exceeded maximum health transition flap threshold; placed in quarantine"
	}

	return a.reconcileNode(ctx, effectiveHealthy, worstSeverity, worstReason, worstMsg, now)
}

// reconcileNode patches the condition and taints on the local Node object.
func (a *Agent) reconcileNode(
	ctx context.Context,
	healthy bool,
	severity health.Severity,
	reason, msg string,
	now time.Time,
) error {
	if a.kube == nil || a.config.NodeName == "" {
		return nil // Standalone test mode without active kube cluster
	}

	node, err := a.kube.CoreV1().Nodes().Get(ctx, a.config.NodeName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get node %s: %w", a.config.NodeName, err)
	}

	needsUpdate := false

	// 1. Reconcile Condition: meridian.dev/GPUHealthy
	condStatus := corev1.ConditionTrue
	if !healthy {
		condStatus = corev1.ConditionFalse
	}

	condIdx := -1
	for i, c := range node.Status.Conditions {
		if c.Type == corev1.NodeConditionType(health.ConditionGPUHealthy) {
			condIdx = i
			break
		}
	}

	if condIdx == -1 {
		node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{
			Type:               corev1.NodeConditionType(health.ConditionGPUHealthy),
			Status:             condStatus,
			LastHeartbeatTime:  metav1.NewTime(now),
			LastTransitionTime: metav1.NewTime(now),
			Reason:             reason,
			Message:            msg,
		})
		needsUpdate = true
	} else {
		c := &node.Status.Conditions[condIdx]
		if c.Status != condStatus || c.Reason != reason {
			c.Status = condStatus
			c.LastTransitionTime = metav1.NewTime(now)
			c.Reason = reason
			c.Message = msg
			needsUpdate = true
		}
		c.LastHeartbeatTime = metav1.NewTime(now)
	}

	// 2. Reconcile Taint: meridian.dev/gpu-unhealthy
	var desiredEffect corev1.TaintEffect
	if !healthy {
		if severity == health.SeverityFatal {
			desiredEffect = corev1.TaintEffectNoExecute
		} else {
			desiredEffect = corev1.TaintEffectNoSchedule
		}
	}

	hasTaint := false
	taintIdx := -1
	for i, t := range node.Spec.Taints {
		if t.Key == health.TaintGPUUnhealthy {
			hasTaint = true
			taintIdx = i
			break
		}
	}

	if !healthy {
		if !hasTaint {
			node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
				Key:       health.TaintGPUUnhealthy,
				Value:     string(severity),
				Effect:    desiredEffect,
				TimeAdded: &metav1.Time{Time: now},
			})
			needsUpdate = true
			klog.InfoS("Applied GPU unhealthy taint to node", "node", a.config.NodeName, "effect", desiredEffect, "reason", reason)
		} else if node.Spec.Taints[taintIdx].Effect != desiredEffect {
			node.Spec.Taints[taintIdx].Effect = desiredEffect
			node.Spec.Taints[taintIdx].Value = string(severity)
			needsUpdate = true
			klog.InfoS("Updated GPU unhealthy taint effect on node", "node", a.config.NodeName, "effect", desiredEffect)
		}
	} else {
		if hasTaint {
			// Remove taint
			newTaints := make([]corev1.Taint, 0, len(node.Spec.Taints)-1)
			for i, t := range node.Spec.Taints {
				if i != taintIdx {
					newTaints = append(newTaints, t)
				}
			}
			node.Spec.Taints = newTaints
			needsUpdate = true
			klog.InfoS("Removed GPU unhealthy taint from node", "node", a.config.NodeName)
		}
	}

	if needsUpdate {
		// Update node spec (taints) and status (conditions)
		if _, err := a.kube.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update node %s spec/taints: %w", a.config.NodeName, err)
		}
		if _, err := a.kube.CoreV1().Nodes().UpdateStatus(ctx, node, metav1.UpdateOptions{}); err != nil {
			return fmt.Errorf("update node %s status/conditions: %w", a.config.NodeName, err)
		}
	}

	return nil
}
