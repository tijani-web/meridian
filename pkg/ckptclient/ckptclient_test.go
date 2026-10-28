package ckptclient_test

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/tijani-web/meridian/pkg/ckptclient"
)

const (
	testNamespace = "default"
	testPodName   = "trainer-pod-0"
	testCkptPath  = "/mnt/checkpoints"
)

// makePod creates a pod in the fake client with optional annotations.
func makePod(cs *fake.Clientset, annotations map[string]string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        testPodName,
			Namespace:   testNamespace,
			Annotations: annotations,
		},
	}
	// ServiceAccountNameKey annotation used as checkpoint path hint in parseRequest.
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[corev1.ServiceAccountNameKey] = testCkptPath
	created, err := cs.CoreV1().Pods(testNamespace).Create(context.Background(), pod, metav1.CreateOptions{})
	if err != nil {
		panic(fmt.Sprintf("makePod: %v", err))
	}
	return created
}

// setAnnotation patches a specific annotation value on the pod.
func setAnnotation(cs *fake.Clientset, key, val string) {
	pod, _ := cs.CoreV1().Pods(testNamespace).Get(context.Background(), testPodName, metav1.GetOptions{})
	if pod.Annotations == nil {
		pod.Annotations = make(map[string]string)
	}
	pod.Annotations[key] = val
	_, _ = cs.CoreV1().Pods(testNamespace).Update(context.Background(), pod, metav1.UpdateOptions{})
}

// getAnnotation retrieves an annotation value from the live fake store.
func getAnnotation(cs *fake.Clientset, key string) string {
	pod, _ := cs.CoreV1().Pods(testNamespace).Get(context.Background(), testPodName, metav1.GetOptions{})
	if pod.Annotations == nil {
		return ""
	}
	return pod.Annotations[key]
}

// ─────────────────────────────────────────────────────────────────────────────
// Tests
// ─────────────────────────────────────────────────────────────────────────────

func TestResumeFrom_Empty(t *testing.T) {
	// MERIDIAN_RESUME_FROM not set → returns empty string
	got := ckptclient.ResumeFrom()
	if got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestResumeStep_Zero(t *testing.T) {
	// MERIDIAN_RESUME_STEP not set → returns 0
	got := ckptclient.ResumeStep()
	if got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestWatch_HandlerCalledAndAcks(t *testing.T) {
	cs := fake.NewClientset()
	makePod(cs, nil)

	client := ckptclient.NewWithClientsetForTest(cs, testNamespace, testPodName, 10*time.Millisecond)
	defer client.Stop()

	var handlerCalled int64

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client.Watch(ctx, func(_ context.Context, req ckptclient.CheckpointRequest) (string, error) {
		atomic.AddInt64(&handlerCalled, 1)
		return "/mnt/checkpoints/step-100.ckpt", nil
	})

	// Simulate controller setting the request annotation.
	setAnnotation(cs, ckptclient.AnnotationCheckpointRequest, "1:1")

	// Wait up to 1s for the ack annotation to appear.
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		ack := getAnnotation(cs, ckptclient.AnnotationCheckpointAck)
		if ack != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	ack := getAnnotation(cs, ckptclient.AnnotationCheckpointAck)
	if ack == "" {
		t.Fatal("expected checkpoint-ack annotation to be set, but it was empty")
	}
	expectedAck := "1:1:/mnt/checkpoints/step-100.ckpt"
	if ack != expectedAck {
		t.Errorf("ack annotation: got %q, want %q", ack, expectedAck)
	}
	if atomic.LoadInt64(&handlerCalled) == 0 {
		t.Error("handler was never called")
	}
}

func TestWatch_NoDoubleAck(t *testing.T) {
	cs := fake.NewClientset()
	makePod(cs, nil)

	client := ckptclient.NewWithClientsetForTest(cs, testNamespace, testPodName, 10*time.Millisecond)
	defer client.Stop()

	var handlerCalled int64

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	client.Watch(ctx, func(_ context.Context, req ckptclient.CheckpointRequest) (string, error) {
		atomic.AddInt64(&handlerCalled, 1)
		return "/mnt/checkpoints/step-200.ckpt", nil
	})

	setAnnotation(cs, ckptclient.AnnotationCheckpointRequest, "1:2")

	// Wait for first ack
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if getAnnotation(cs, ckptclient.AnnotationCheckpointAck) != "" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait another 100ms to ensure no double-call
	time.Sleep(100 * time.Millisecond)

	if n := atomic.LoadInt64(&handlerCalled); n != 1 {
		t.Errorf("handler was called %d times, expected exactly 1 (no double-ack)", n)
	}
}

func TestWatch_NewSeqTriggers(t *testing.T) {
	cs := fake.NewClientset()
	makePod(cs, nil)

	client := ckptclient.NewWithClientsetForTest(cs, testNamespace, testPodName, 10*time.Millisecond)
	defer client.Stop()

	var handlerCalled int64

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	client.Watch(ctx, func(_ context.Context, req ckptclient.CheckpointRequest) (string, error) {
		n := atomic.AddInt64(&handlerCalled, 1)
		return fmt.Sprintf("/mnt/checkpoints/step-%d.ckpt", n*100), nil
	})

	// First checkpoint request
	setAnnotation(cs, ckptclient.AnnotationCheckpointRequest, "1:1")
	time.Sleep(200 * time.Millisecond)

	// Second checkpoint request (new seq)
	setAnnotation(cs, ckptclient.AnnotationCheckpointRequest, "1:2")
	time.Sleep(200 * time.Millisecond)

	if n := atomic.LoadInt64(&handlerCalled); n < 2 {
		t.Errorf("expected handler called at least 2 times, got %d", n)
	}
}

func TestHeartbeat_PatchesAnnotations(t *testing.T) {
	cs := fake.NewClientset()
	makePod(cs, nil)

	client := ckptclient.NewWithClientsetForTest(cs, testNamespace, testPodName, time.Hour)
	defer client.Stop()

	ctx := context.Background()
	client.Heartbeat(ctx, 42)

	hb := getAnnotation(cs, ckptclient.AnnotationCheckpointHeartbeat)
	if hb == "" {
		t.Error("heartbeat annotation was not set")
	}
	step := getAnnotation(cs, ckptclient.AnnotationCheckpointStep)
	if step != "42" {
		t.Errorf("step annotation: got %q, want %q", step, "42")
	}
}

func TestAck_DirectCall(t *testing.T) {
	cs := fake.NewClientset()
	makePod(cs, nil)

	client := ckptclient.NewWithClientsetForTest(cs, testNamespace, testPodName, time.Hour)
	defer client.Stop()

	ctx := context.Background()
	req := ckptclient.CheckpointRequest{Generation: 2, Seq: 5, Path: "/mnt/ckpt"}
	if err := client.Ack(ctx, req, "/mnt/ckpt/step-500.ckpt"); err != nil {
		t.Fatalf("Ack returned error: %v", err)
	}

	ack := getAnnotation(cs, ckptclient.AnnotationCheckpointAck)
	expected := "2:5:/mnt/ckpt/step-500.ckpt"
	if ack != expected {
		t.Errorf("ack annotation: got %q, want %q", ack, expected)
	}
}
