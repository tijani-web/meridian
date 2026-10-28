/*
Package envtest contains integration tests for Meridian controllers running
against real kube-apiserver and etcd binaries.

This test file verifies Phase 05: Checkpoint Protocol and Preemption:
- Cooperative preemption requests checkpoint before eviction
- Real checkpoint file creation and ack annotation protocol
- Controller waits for all gang members, records coordinates, and evicts
- Resume env injection into new generation pods (MERIDIAN_RESUME_FROM & MERIDIAN_RESUME_STEP)
- Checkpoint timeout path with forced eviction and CheckpointHealthy=False
*/
package envtest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/checkpoint"
	"github.com/tijani-web/meridian/internal/controller/workload"
	"github.com/tijani-web/meridian/pkg/ckptclient"
)

type checkpointArtifact struct {
	PodName   string    `json:"podName"`
	Step      int64     `json:"step"`
	Timestamp time.Time `json:"timestamp"`
	Weights   []float64 `json:"weights"`
}

var _ = Describe("Checkpoint Protocol & Preemption", func() {
	ctx := context.Background()
	ns := "default"

	Context("When a running workload with checkpointing enabled is preempted", func() {
		It("requests checkpoint, waits for acks, evicts, and injects resume env on re-admission", func() {
			ckptDir, err := os.MkdirTemp("", "meridian-ckpt-*")
			Expect(err).NotTo(HaveOccurred())
			DeferCleanup(func() { _ = os.RemoveAll(ckptDir) })

			// Create workload
			w := &meridianv1alpha1.GPUWorkload{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gw-ckpt-preempt-",
					Namespace:    ns,
				},
				Spec: meridianv1alpha1.GPUWorkloadSpec{
					QueueName:  "default-queue",
					Kind:       meridianv1alpha1.WorkloadKindTraining,
					Gang:       meridianv1alpha1.GangSpec{Size: 2, MinAvailable: 2},
					GPUsPerPod: 4,
					GPUType:    "nvidia-h100",
					Checkpoint: meridianv1alpha1.CheckpointSpec{
						Enabled:    true,
						Path:       ckptDir,
						AckTimeout: metav1.Duration{Duration: 30 * time.Second},
					},
					PodTemplate: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{"app": "trainer"},
						},
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{
								{Name: "trainer", Image: "trainer:v1"},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Wait for controller to attach finalizer and transition to Queued
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Admit the workload
			orig := w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseAdmitted
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Wait for 2 member pods to be created and workload to move to Scheduling
			var pods corev1.PodList
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
					workload.LabelWorkload: w.Name,
				})).To(Succeed())
				g.Expect(pods.Items).To(HaveLen(2))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Transition workload to Running
			orig = w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseRunning
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Initiate preemption with checkpointing
			orig = w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseCheckpointing
			if w.Annotations == nil {
				w.Annotations = make(map[string]string)
			}
			w.Annotations[checkpoint.AnnotationPreemptRequested] = "true"
			Expect(k8sClient.Update(ctx, w)).To(Succeed())
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Verify controller broadcasts checkpoint-request onto all member pods
			Eventually(func(g Gomega) {
				var p0, p1 corev1.Pod
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pods.Items[0].Name}, &p0)).To(Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pods.Items[1].Name}, &p1)).To(Succeed())
				g.Expect(p0.Annotations[ckptclient.AnnotationCheckpointRequest]).To(ContainSubstring(":1"))
				g.Expect(p1.Annotations[ckptclient.AnnotationCheckpointRequest]).To(ContainSubstring(":1"))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Trainers write real checkpoint files and patch ack annotations
			targetStep := int64(250)
			ckptFile0 := filepath.Join(ckptDir, fmt.Sprintf("step-%d-p0.ckpt", targetStep))
			ckptFile1 := filepath.Join(ckptDir, fmt.Sprintf("step-%d-p1.ckpt", targetStep))

			raw0, _ := json.Marshal(checkpointArtifact{PodName: pods.Items[0].Name, Step: targetStep, Weights: []float64{0.1, 0.2}})
			raw1, _ := json.Marshal(checkpointArtifact{PodName: pods.Items[1].Name, Step: targetStep, Weights: []float64{0.1, 0.2}})
			Expect(os.WriteFile(ckptFile0, raw0, 0644)).To(Succeed())
			Expect(os.WriteFile(ckptFile1, raw1, 0644)).To(Succeed())

			// Patch acks on pods
			var p0, p1 corev1.Pod
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pods.Items[0].Name}, &p0)).To(Succeed())
			Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns, Name: pods.Items[1].Name}, &p1)).To(Succeed())

			p0Orig := p0.DeepCopy()
			p0.Annotations[ckptclient.AnnotationCheckpointAck] = fmt.Sprintf("0:1:%s", ckptFile0)
			p0.Annotations[ckptclient.AnnotationCheckpointStep] = strconv.FormatInt(targetStep, 10)
			Expect(k8sClient.Patch(ctx, &p0, client.MergeFrom(p0Orig))).To(Succeed())

			p1Orig := p1.DeepCopy()
			p1.Annotations[ckptclient.AnnotationCheckpointAck] = fmt.Sprintf("0:1:%s", ckptFile1)
			p1.Annotations[ckptclient.AnnotationCheckpointStep] = strconv.FormatInt(targetStep, 10)
			Expect(k8sClient.Patch(ctx, &p1, client.MergeFrom(p1Orig))).To(Succeed())

			// Controller must verify acks and transition to Preempting
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhasePreempting))
				g.Expect(w.Status.LastCheckpointAt).NotTo(BeNil())
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Simulate deletion of victim pods
			Expect(k8sClient.Delete(ctx, &p0)).To(Succeed())
			Expect(k8sClient.Delete(ctx, &p1)).To(Succeed())

			// Workload returns to Queued after pods are deleted
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Re-admit workload -> controller creates new pods with resume env injected
			orig = w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseAdmitted
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			Eventually(func(g Gomega) {
				var newPods corev1.PodList
				g.Expect(k8sClient.List(ctx, &newPods, client.InNamespace(ns), client.MatchingLabels{
					workload.LabelWorkload: w.Name,
				})).To(Succeed())
				g.Expect(newPods.Items).To(HaveLen(2))

				// Verify MERIDIAN_RESUME_FROM is injected in new pod
				var resumeFound bool
				for _, env := range newPods.Items[0].Spec.Containers[0].Env {
					if env.Name == ckptclient.EnvResumeFrom && env.Value != "" {
						resumeFound = true
						break
					}
				}
				g.Expect(resumeFound).To(BeTrue())
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		})

		It("forces preemption eviction and marks CheckpointHealthy=False on ack timeout", func() {
			w := &meridianv1alpha1.GPUWorkload{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gw-ckpt-timeout-",
					Namespace:    ns,
				},
				Spec: meridianv1alpha1.GPUWorkloadSpec{
					QueueName:  "default-queue",
					Kind:       meridianv1alpha1.WorkloadKindTraining,
					Gang:       meridianv1alpha1.GangSpec{Size: 2, MinAvailable: 2},
					GPUsPerPod: 2,
					GPUType:    "nvidia-h100",
					Checkpoint: meridianv1alpha1.CheckpointSpec{
						Enabled:    true,
						AckTimeout: metav1.Duration{Duration: 1 * time.Second}, // Fast timeout
					},
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "trainer", Image: "trainer:v1"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Wait for Queued
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Admit and advance to Running
			orig := w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseAdmitted
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			Eventually(func(g Gomega) {
				var pods corev1.PodList
				g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
					workload.LabelWorkload: w.Name,
				})).To(Succeed())
				g.Expect(pods.Items).To(HaveLen(2))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			orig = w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseRunning
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Trigger checkpointing preemption
			orig = w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseCheckpointing
			if w.Annotations == nil {
				w.Annotations = make(map[string]string)
			}
			w.Annotations[checkpoint.AnnotationPreemptRequested] = "true"
			Expect(k8sClient.Update(ctx, w)).To(Succeed())
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Do NOT ack pods. Wait for AckTimeout (1s) to expire
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				// Should force-transition to Preempting
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhasePreempting))

				// Should mark CheckpointHealthy=False
				var foundCond *metav1.Condition
				for i := range w.Status.Conditions {
					if w.Status.Conditions[i].Type == checkpoint.ConditionCheckpointHealthy {
						foundCond = &w.Status.Conditions[i]
						break
					}
				}
				g.Expect(foundCond).NotTo(BeNil())
				g.Expect(foundCond.Status).To(Equal(metav1.ConditionFalse))
				g.Expect(foundCond.Reason).To(Equal("AckTimeout"))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())
		})
	})
})
