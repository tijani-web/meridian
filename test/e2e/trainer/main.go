/*
Package main implements the Meridian test trainer workload.

This is a real Go training workload used for end-to-end verification of Phase 05:
1. Initializes ckptclient to cooperate with the Meridian control plane.
2. Checks for MERIDIAN_RESUME_FROM and MERIDIAN_RESUME_STEP, verifying continuity.
3. Watches pod annotations for checkpoint requests, writes real structured checkpoint
   artifacts to disk, and acknowledges back to the controller.
4. Updates checkpoint-heartbeat on every training step for straggler tracking.
*/
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tijani-web/meridian/pkg/ckptclient"
)

// CheckpointArtifact models the serialized state of a training step.
type CheckpointArtifact struct {
	PodName    string    `json:"podName"`
	Namespace  string    `json:"namespace"`
	Generation int64     `json:"generation"`
	Seq        int64     `json:"seq"`
	Step       int64     `json:"step"`
	Timestamp  time.Time `json:"timestamp"`
	Loss       float64   `json:"loss"`
}

func main() {
	targetSteps := flag.Int64("target-steps", 1000, "Maximum training steps before clean termination")
	stepDelay := flag.Duration("step-delay", 100*time.Millisecond, "Simulated compute time per training step")
	ckptDirFlag := flag.String("checkpoint-dir", "", "Fallback directory to write checkpoints")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	podNamespace := os.Getenv("POD_NAMESPACE")
	if podNamespace == "" {
		podNamespace = "default"
	}
	podName := os.Getenv("POD_NAME")
	if podName == "" {
		var err error
		podName, err = os.Hostname()
		if err != nil {
			podName = "trainer-0"
		}
	}

	log.Printf("Starting Meridian trainer: namespace=%s, pod=%s, targetSteps=%d", podNamespace, podName, *targetSteps)

	var currentStep int64
	resumePath := ckptclient.ResumeFrom()
	resumeStep := ckptclient.ResumeStep()

	if resumePath != "" {
		log.Printf("Resuming from checkpoint path: %s (suggested step: %d)", resumePath, resumeStep)
		data, err := os.ReadFile(resumePath)
		if err != nil {
			log.Fatalf("FATAL: failed to read resume checkpoint file %s: %v", resumePath, err)
		}

		var artifact CheckpointArtifact
		if err := json.Unmarshal(data, &artifact); err != nil {
			log.Fatalf("FATAL: failed to deserialize resume checkpoint: %v", err)
		}

		if artifact.Step < resumeStep {
			log.Fatalf("FATAL: step regression! Read step %d < resumed step %d", artifact.Step, resumeStep)
		}

		currentStep = artifact.Step
		log.Printf("Successfully restored checkpoint: step=%d, timestamp=%s, loss=%.4f",
			currentStep, artifact.Timestamp.Format(time.RFC3339), artifact.Loss)
	}

	client, err := ckptclient.New(ctx, podNamespace, podName)
	if err != nil {
		log.Printf("Warning: failed to initialize in-cluster ckptclient (standalone or local mode): %v", err)
	}

	if client != nil {
		defer client.Stop()

		// Register cooperative checkpoint handler
		client.Watch(ctx, func(hCtx context.Context, req ckptclient.CheckpointRequest) (string, error) {
			step := atomic.LoadInt64(&currentStep)
			dir := req.Path
			if dir == "" {
				dir = *ckptDirFlag
			}
			if dir == "" {
				dir = filepath.Join(os.TempDir(), "meridian-checkpoints")
			}
			if err := os.MkdirAll(dir, 0755); err != nil {
				return "", fmt.Errorf("mkdir checkpoint dir %s: %w", dir, err)
			}

			ckptFile := filepath.Join(dir, fmt.Sprintf("checkpoint-%s-step-%d.json", podName, step))
			artifact := CheckpointArtifact{
				PodName:    podName,
				Namespace:  podNamespace,
				Generation: req.Generation,
				Seq:        req.Seq,
				Step:       step,
				Timestamp:  time.Now().UTC(),
				Loss:       1.0 / float64(step+1),
			}

			raw, err := json.MarshalIndent(artifact, "", "  ")
			if err != nil {
				return "", fmt.Errorf("marshal artifact: %w", err)
			}

			if err := os.WriteFile(ckptFile, raw, 0644); err != nil {
				return "", fmt.Errorf("write checkpoint artifact %s: %w", ckptFile, err)
			}

			log.Printf("Saved checkpoint: seq=%d step=%d path=%s", req.Seq, step, ckptFile)
			return ckptFile, nil
		})
	}

	log.Printf("Entering training loop from step %d...", currentStep)
	ticker := time.NewTicker(*stepDelay)
	defer ticker.Stop()

	for currentStep < *targetSteps {
		select {
		case <-ctx.Done():
			log.Printf("Trainer received termination signal at step %d", currentStep)
			return
		case <-ticker.C:
			step := atomic.AddInt64(&currentStep, 1)
			if client != nil {
				client.Heartbeat(ctx, step)
			}
			if step%100 == 0 {
				log.Printf("Step %d / %d completed", step, *targetSteps)
			}
		}
	}

	log.Printf("Trainer completed all %d steps successfully.", *targetSteps)
}
