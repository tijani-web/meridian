/*
Package ckptclient is the Meridian trainer-side checkpoint library.

Training binaries import this package to participate in the Meridian cooperative
checkpoint protocol. The library watches the running pod's own annotations for a
checkpoint-request from the Meridian controller, invokes a user-supplied handler
to write the checkpoint, then acks by patching the checkpoint-ack annotation.

Usage:

	func main() {
	    ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	    defer cancel()

	    client, err := ckptclient.New(ctx, os.Getenv("POD_NAMESPACE"), os.Getenv("POD_NAME"))
	    if err != nil {
	        log.Fatal(err)
	    }
	    defer client.Stop()

	    client.Watch(ctx, func(ctx context.Context, req ckptclient.CheckpointRequest) (string, error) {
	        path := filepath.Join(req.Path, fmt.Sprintf("step-%d.ckpt", currentStep))
	        if err := writeCheckpoint(path); err != nil {
	            return "", err
	        }
	        return path, nil
	    })

	    // Resume from prior checkpoint if available.
	    if from := ckptclient.ResumeFrom(); from != "" {
	        loadCheckpoint(from)
	    }

	    for step := ckptclient.ResumeStep(); ; step++ {
	        train(step)
	        client.Heartbeat(ctx, step) // update straggler detection annotation
	    }
	}

Security note: The library patches only two specific annotations on the pod it is
running in. It does NOT have write access to any other resource.
*/
package ckptclient

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"sync"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

const (
	// AnnotationCheckpointRequest is set by the Meridian controller on the pod
	// to initiate a checkpoint. Format: "<generation>:<seq>".
	AnnotationCheckpointRequest = "meridian.dev/checkpoint-request"

	// AnnotationCheckpointAck is set by ckptclient on the pod after the
	// checkpoint handler completes. Format: "<generation>:<seq>:<path>".
	AnnotationCheckpointAck = "meridian.dev/checkpoint-ack"

	// AnnotationCheckpointHeartbeat is updated by ckptclient on every call to
	// Heartbeat. Format: RFC3339 timestamp. The controller uses this to detect
	// stragglers (pods with no recent progress).
	AnnotationCheckpointHeartbeat = "meridian.dev/checkpoint-heartbeat"

	// AnnotationCheckpointStep is updated by ckptclient on every call to
	// Heartbeat. Format: decimal integer string.
	AnnotationCheckpointStep = "meridian.dev/checkpoint-step"

	// EnvResumeFrom is the environment variable set by the Meridian controller
	// on new-generation pods, pointing to the last acked checkpoint path.
	EnvResumeFrom = "MERIDIAN_RESUME_FROM"

	// EnvResumeStep is the environment variable set by the Meridian controller
	// on new-generation pods with the last acked training step.
	EnvResumeStep = "MERIDIAN_RESUME_STEP"

	// defaultPollInterval is how often the client polls its own annotations.
	// A short interval keeps checkpoint latency low; 2s is a reasonable default.
	defaultPollInterval = 2 * time.Second

	// maxRetries is the maximum number of times the handler is retried before
	// the client gives up and logs a permanent failure (letting the controller
	// timeout-evict the pod).
	maxRetries = 3
)

// CheckpointRequest carries the parameters of a checkpoint request from the controller.
type CheckpointRequest struct {
	// Generation is the GPUWorkload generation at the time of the request.
	// It must match the generation on ack, or the controller ignores the ack.
	Generation int64

	// Seq is the monotonically increasing checkpoint sequence number.
	Seq int64

	// Path is the suggested base directory for checkpoint files.
	// Taken from GPUWorkload.Spec.Checkpoint.Path.
	Path string
}

// CheckpointHandler is the function the trainer supplies to Client.Watch.
// It must write a checkpoint synchronously and return the actual path written.
// If it returns an error, the library retries up to maxRetries times.
// The context is cancelled when the pod is being shut down.
type CheckpointHandler func(ctx context.Context, req CheckpointRequest) (actualPath string, err error)

// Client is the trainer-side Meridian checkpoint client.
// Owner: the training process. Lifecycle: Create at startup, Stop at shutdown.
// All goroutines started by this struct are shut down via the Stop method or
// when the context passed to Watch is cancelled.
type Client struct {
	clientset    kubernetes.Interface
	namespace    string
	podName      string
	pollInterval time.Duration

	mu          sync.Mutex
	lastAcked   string // "<generation>:<seq>" of the last successfully acked request
	stopCh      chan struct{}
	stopOnce    sync.Once
}

// New creates a Client using in-cluster configuration.
// podNamespace and podName should be injected via the Downward API:
//
//	env:
//	  - name: POD_NAMESPACE
//	    valueFrom: { fieldRef: { fieldPath: metadata.namespace } }
//	  - name: POD_NAME
//	    valueFrom: { fieldRef: { fieldPath: metadata.name } }
func New(ctx context.Context, podNamespace, podName string) (*Client, error) {
	cfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("ckptclient: in-cluster config: %w", err)
	}
	cs, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		return nil, fmt.Errorf("ckptclient: build clientset: %w", err)
	}
	return newWithClientset(cs, podNamespace, podName, defaultPollInterval), nil
}

// newWithClientset is the internal constructor used by New and tests.
// It accepts an explicit kubernetes.Interface so tests can inject a fake client.
// All injected clients in tests must speak the real annotation protocol.
func newWithClientset(cs kubernetes.Interface, ns, name string, poll time.Duration) *Client {
	return &Client{
		clientset:    cs,
		namespace:    ns,
		podName:      name,
		pollInterval: poll,
		stopCh:       make(chan struct{}),
	}
}

// NewWithClientsetForTest is exported for unit tests so they can inject a
// fake kubernetes.Interface without requiring an actual cluster. This must only
// be called from _test.go files.
func NewWithClientsetForTest(cs kubernetes.Interface, ns, name string, poll time.Duration) *Client {
	return newWithClientset(cs, ns, name, poll)
}

// Watch starts a background goroutine that polls the pod's own annotations.
// When a new checkpoint-request annotation is detected (generation:seq not yet
// acked), handler is called with the request. On success the ack annotation is
// patched. Watch returns immediately. Call Stop() to shut it down.
func (c *Client) Watch(ctx context.Context, handler CheckpointHandler) {
	go c.watchLoop(ctx, handler)
}

func (c *Client) watchLoop(ctx context.Context, handler CheckpointHandler) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.stopCh:
			return
		case <-ticker.C:
			if err := c.poll(ctx, handler); err != nil {
				klog.ErrorS(err, "ckptclient: poll error", "pod", c.podName)
			}
		}
	}
}

func (c *Client) poll(ctx context.Context, handler CheckpointHandler) error {
	pod, err := c.clientset.CoreV1().Pods(c.namespace).Get(ctx, c.podName, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get pod: %w", err)
	}

	reqVal := pod.Annotations[AnnotationCheckpointRequest]
	if reqVal == "" {
		return nil // no pending request
	}

	// The checkpoint path comes from the pod's own checkpoint annotation path label
	// (set by the controller as meridian.dev/checkpoint-path).
	// Fall back to empty string if absent; the handler may use a default.
	ckptPath := pod.Annotations["meridian.dev/checkpoint-path"]
	req, err := parseRequest(reqVal, ckptPath)
	if err != nil {
		return fmt.Errorf("parse request annotation %q: %w", reqVal, err)
	}

	requestKey := fmt.Sprintf("%d:%d", req.Generation, req.Seq)
	c.mu.Lock()
	alreadyAcked := c.lastAcked == requestKey
	c.mu.Unlock()
	if alreadyAcked {
		return nil
	}

	// Check if we've already patched the ack (e.g. after a poll-retry crash).
	ackVal := pod.Annotations[AnnotationCheckpointAck]
	if ackPrefix(ackVal) == requestKey {
		c.mu.Lock()
		c.lastAcked = requestKey
		c.mu.Unlock()
		return nil
	}

	klog.InfoS("ckptclient: checkpoint request received", "generation", req.Generation, "seq", req.Seq, "path", req.Path)

	var actualPath string
	var handlerErr error
	for attempt := 1; attempt <= maxRetries; attempt++ {
		actualPath, handlerErr = handler(ctx, req)
		if handlerErr == nil {
			break
		}
		klog.ErrorS(handlerErr, "ckptclient: handler failed", "attempt", attempt, "maxRetries", maxRetries)
	}
	if handlerErr != nil {
		return fmt.Errorf("checkpoint handler failed after %d retries: %w", maxRetries, handlerErr)
	}

	if err := c.Ack(ctx, req, actualPath); err != nil {
		return fmt.Errorf("ack: %w", err)
	}
	c.mu.Lock()
	c.lastAcked = requestKey
	c.mu.Unlock()
	klog.InfoS("ckptclient: checkpoint acked", "generation", req.Generation, "seq", req.Seq, "path", actualPath)
	return nil
}

// Ack patches the checkpoint-ack annotation onto the pod.
// It is called automatically by Watch after the handler returns.
// Users who need manual control may call it directly.
func (c *Client) Ack(ctx context.Context, req CheckpointRequest, actualPath string) error {
	ackVal := fmt.Sprintf("%d:%d:%s", req.Generation, req.Seq, actualPath)
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q}}}`,
		AnnotationCheckpointAck, ackVal,
	))
	_, err := c.clientset.CoreV1().Pods(c.namespace).Patch(
		ctx, c.podName, types.MergePatchType, patch, metav1.PatchOptions{},
	)
	if err != nil {
		return fmt.Errorf("patch ack annotation: %w", err)
	}
	return nil
}

// Heartbeat patches the heartbeat and step annotations onto the pod.
// Call this on every training step to prevent straggler detection from
// triggering on a healthy but slow pod.
func (c *Client) Heartbeat(ctx context.Context, step int64) {
	patch := []byte(fmt.Sprintf(
		`{"metadata":{"annotations":{%q:%q,%q:%q}}}`,
		AnnotationCheckpointHeartbeat, time.Now().UTC().Format(time.RFC3339),
		AnnotationCheckpointStep, strconv.FormatInt(step, 10),
	))
	if _, err := c.clientset.CoreV1().Pods(c.namespace).Patch(
		ctx, c.podName, types.MergePatchType, patch, metav1.PatchOptions{},
	); err != nil {
		klog.V(4).ErrorS(err, "ckptclient: heartbeat patch failed", "step", step)
	}
}

// Stop cancels the Watch goroutine. Idempotent.
func (c *Client) Stop() {
	c.stopOnce.Do(func() { close(c.stopCh) })
}

// ResumeFrom returns the value of the MERIDIAN_RESUME_FROM environment variable.
// If absent, returns an empty string, meaning no prior checkpoint is available.
func ResumeFrom() string {
	return os.Getenv(EnvResumeFrom)
}

// ResumeStep returns the value of MERIDIAN_RESUME_STEP, parsed as an int64.
// Returns 0 if the variable is absent or not a valid integer.
func ResumeStep() int64 {
	v := os.Getenv(EnvResumeStep)
	if v == "" {
		return 0
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// ─────────────────────────────────────────────────────────────────────────────
// Helpers
// ─────────────────────────────────────────────────────────────────────────────

// parseRequest decodes a checkpoint-request annotation value.
// Format: "<generation>:<seq>" with an optional :<path> suffix (ignored here).
func parseRequest(val, checkpointPath string) (CheckpointRequest, error) {
	var gen, seq int64
	if _, err := fmt.Sscanf(val, "%d:%d", &gen, &seq); err != nil {
		return CheckpointRequest{}, fmt.Errorf("expected \"<gen>:<seq>\", got %q: %w", val, err)
	}
	return CheckpointRequest{Generation: gen, Seq: seq, Path: checkpointPath}, nil
}

// ackPrefix returns the "<generation>:<seq>" prefix of an ack annotation value.
func ackPrefix(val string) string {
	// Format: "<generation>:<seq>:<path>"
	var gen, seq int64
	if n, _ := fmt.Sscanf(val, "%d:%d", &gen, &seq); n == 2 {
		return fmt.Sprintf("%d:%d", gen, seq)
	}
	return ""
}
