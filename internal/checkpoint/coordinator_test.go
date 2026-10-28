package checkpoint_test

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/checkpoint"
	"github.com/tijani-web/meridian/pkg/ckptclient"
)

func setupTest() *checkpoint.Coordinator {
	recorder := record.NewFakeRecorder(10)
	return checkpoint.NewCoordinator(nil, recorder)
}

func TestEvaluateAcks_Complete(t *testing.T) {
	coord := setupTest()

	w := &meridianv1alpha1.GPUWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "train-job",
			Namespace: "default",
			Annotations: map[string]string{
				checkpoint.AnnotationCheckpointSeq: "2",
			},
		},
		Status: meridianv1alpha1.GPUWorkloadStatus{
			Generation: 1,
		},
	}

	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "train-job-0",
				Annotations: map[string]string{
					ckptclient.AnnotationCheckpointAck:  "1:2:/shared/checkpoints/step-100",
					ckptclient.AnnotationCheckpointStep: "100",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "train-job-1",
				Annotations: map[string]string{
					ckptclient.AnnotationCheckpointAck:  "1:2:/shared/checkpoints/step-100",
					ckptclient.AnnotationCheckpointStep: "100",
				},
			},
		},
	}

	status, summary, err := coord.EvaluateAcks(w, pods)
	if err != nil {
		t.Fatalf("EvaluateAcks error: %v", err)
	}
	if status != checkpoint.AckStatusComplete {
		t.Errorf("expected AckStatusComplete, got %s", status)
	}
	if summary.Path != "/shared/checkpoints/step-100" {
		t.Errorf("expected path /shared/checkpoints/step-100, got %s", summary.Path)
	}
	if summary.Step != 100 {
		t.Errorf("expected step 100, got %d", summary.Step)
	}
}

func TestEvaluateAcks_PendingAndTimeout(t *testing.T) {
	coord := setupTest()

	w := &meridianv1alpha1.GPUWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "train-job",
			Namespace: "default",
			Annotations: map[string]string{
				checkpoint.AnnotationCheckpointSeq:         "1",
				checkpoint.AnnotationCheckpointRequestedAt: time.Now().UTC().Format(time.RFC3339),
			},
		},
		Spec: meridianv1alpha1.GPUWorkloadSpec{
			Checkpoint: meridianv1alpha1.CheckpointSpec{
				AckTimeout: metav1.Duration{Duration: 10 * time.Second},
			},
		},
		Status: meridianv1alpha1.GPUWorkloadStatus{
			Generation: 1,
		},
	}

	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "train-job-0",
				Annotations: map[string]string{
					ckptclient.AnnotationCheckpointAck: "1:1:/shared/checkpoints/step-50",
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "train-job-1",
				// No ack annotation yet
			},
		},
	}

	// 1. Should be pending while under timeout
	status, summary, err := coord.EvaluateAcks(w, pods)
	if err != nil {
		t.Fatalf("EvaluateAcks error: %v", err)
	}
	if status != checkpoint.AckStatusPending {
		t.Errorf("expected AckStatusPending, got %s", status)
	}
	if len(summary.PendingPods) != 1 || summary.PendingPods[0] != "train-job-1" {
		t.Errorf("expected train-job-1 pending, got %v", summary.PendingPods)
	}

	// 2. Set requestedAt to 30s ago -> should be Timeout
	w.Annotations[checkpoint.AnnotationCheckpointRequestedAt] = time.Now().UTC().Add(-30 * time.Second).Format(time.RFC3339)
	status, _, err = coord.EvaluateAcks(w, pods)
	if err != nil {
		t.Fatalf("EvaluateAcks error: %v", err)
	}
	if status != checkpoint.AckStatusTimeout {
		t.Errorf("expected AckStatusTimeout, got %s", status)
	}
}

func TestCompleteCheckpointAndTimeout(t *testing.T) {
	coord := setupTest()

	w := &meridianv1alpha1.GPUWorkload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "train-job",
			Namespace: "default",
		},
	}

	summary := &checkpoint.AckSummary{
		Generation: 1,
		Seq:        1,
		Path:       "/mnt/ckpt/step-200.pt",
		Step:       200,
		AckedPods:  []string{"train-job-0", "train-job-1"},
	}

	err := coord.CompleteCheckpoint(context.Background(), w, summary)
	if err != nil {
		t.Fatalf("CompleteCheckpoint error: %v", err)
	}

	if w.Status.LastCheckpointAt == nil {
		t.Errorf("expected LastCheckpointAt to be set")
	}
	if w.Annotations[checkpoint.AnnotationLastCheckpointPath] != "/mnt/ckpt/step-200.pt" {
		t.Errorf("expected last path /mnt/ckpt/step-200.pt, got %q", w.Annotations[checkpoint.AnnotationLastCheckpointPath])
	}
	if w.Annotations[checkpoint.AnnotationLastCheckpointStep] != "200" {
		t.Errorf("expected last step 200, got %q", w.Annotations[checkpoint.AnnotationLastCheckpointStep])
	}

	// Test HandleTimeout
	err = coord.HandleTimeout(context.Background(), w, &checkpoint.AckSummary{
		Seq:         2,
		PendingPods: []string{"train-job-1"},
	})
	if err != nil {
		t.Fatalf("HandleTimeout error: %v", err)
	}

	var foundCond *metav1.Condition
	for i := range w.Status.Conditions {
		if w.Status.Conditions[i].Type == checkpoint.ConditionCheckpointHealthy {
			foundCond = &w.Status.Conditions[i]
			break
		}
	}
	if foundCond == nil || foundCond.Status != metav1.ConditionFalse {
		t.Errorf("expected CheckpointHealthy=False condition, got %v", foundCond)
	}
}

func TestInjectResumeEnv(t *testing.T) {
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Name: "trainer",
				Env: []corev1.EnvVar{
					{Name: "EXISTING_VAR", Value: "existing_val"},
				},
			},
		},
	}

	checkpoint.InjectResumeEnv(podSpec, "/mnt/ckpt/step-500.pt", 500)

	c := podSpec.Containers[0]
	resumeFromFound := false
	resumeStepFound := false

	for _, e := range c.Env {
		if e.Name == ckptclient.EnvResumeFrom {
			resumeFromFound = true
			if e.Value != "/mnt/ckpt/step-500.pt" {
				t.Errorf("expected /mnt/ckpt/step-500.pt, got %s", e.Value)
			}
		}
		if e.Name == ckptclient.EnvResumeStep {
			resumeStepFound = true
			if e.Value != "500" {
				t.Errorf("expected 500, got %s", e.Value)
			}
		}
	}

	if !resumeFromFound {
		t.Errorf("MERIDIAN_RESUME_FROM not found in container env")
	}
	if !resumeStepFound {
		t.Errorf("MERIDIAN_RESUME_STEP not found in container env")
	}
}

func TestDetectStragglers(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-5 * time.Second).Format(time.RFC3339)
	lagging := now.Add(-90 * time.Second).Format(time.RFC3339)

	pods := []corev1.Pod{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "healthy-pod",
				Annotations: map[string]string{
					ckptclient.AnnotationCheckpointHeartbeat: recent,
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "straggler-pod",
				Annotations: map[string]string{
					ckptclient.AnnotationCheckpointHeartbeat: lagging,
				},
			},
		},
	}

	stragglers := checkpoint.DetectStragglers(pods, 30*time.Second)
	if len(stragglers) != 1 || stragglers[0] != "straggler-pod" {
		t.Errorf("expected [straggler-pod], got %v", stragglers)
	}
}
