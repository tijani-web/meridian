package health

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	agenthealth "github.com/tijani-web/meridian/internal/agent/health"
)

func TestEvaluateNodeHealth_Healthy(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-gpu-0"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:   corev1.NodeConditionType(agenthealth.ConditionGPUHealthy),
					Status: corev1.ConditionTrue,
				},
			},
		},
	}

	r := &NodeHealthReconciler{}
	unhealthy, fatal, reason := r.evaluateNodeHealth(node)
	if unhealthy || fatal || reason != "" {
		t.Fatalf("expected healthy node, got unhealthy=%t fatal=%t reason=%s", unhealthy, fatal, reason)
	}
}

func TestEvaluateNodeHealth_TaintNoExecute(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-gpu-1"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    agenthealth.TaintGPUUnhealthy,
					Value:  "Fatal",
					Effect: corev1.TaintEffectNoExecute,
				},
			},
		},
	}

	r := &NodeHealthReconciler{}
	unhealthy, fatal, _ := r.evaluateNodeHealth(node)
	if !unhealthy || !fatal {
		t.Fatalf("expected fatal unhealthy node for NoExecute taint, got unhealthy=%t fatal=%t", unhealthy, fatal)
	}
}

func TestEvaluateNodeHealth_TaintNoSchedule(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-gpu-2"},
		Spec: corev1.NodeSpec{
			Taints: []corev1.Taint{
				{
					Key:    agenthealth.TaintGPUUnhealthy,
					Value:  "Recoverable",
					Effect: corev1.TaintEffectNoSchedule,
				},
			},
		},
	}

	r := &NodeHealthReconciler{}
	unhealthy, fatal, _ := r.evaluateNodeHealth(node)
	if !unhealthy || fatal {
		t.Fatalf("expected recoverable unhealthy node for NoSchedule taint, got unhealthy=%t fatal=%t", unhealthy, fatal)
	}
}

func TestEvaluateNodeHealth_ConditionFalse(t *testing.T) {
	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "worker-gpu-3"},
		Status: corev1.NodeStatus{
			Conditions: []corev1.NodeCondition{
				{
					Type:    corev1.NodeConditionType(agenthealth.ConditionGPUHealthy),
					Status:  corev1.ConditionFalse,
					Reason:  "ThermalShutdown",
					Message: "GPU exceeded critical temp",
				},
			},
		},
	}

	r := &NodeHealthReconciler{}
	unhealthy, _, reason := r.evaluateNodeHealth(node)
	if !unhealthy || reason != "ThermalShutdown" {
		t.Fatalf("expected ThermalShutdown unhealthy node, got unhealthy=%t reason=%s", unhealthy, reason)
	}
}
