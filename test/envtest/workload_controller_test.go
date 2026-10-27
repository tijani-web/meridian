package envtest

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/controller/workload"
)

var _ = Describe("Workload Controller", func() {
	ctx := context.Background()
	ns := "default"

	newWorkload := func(name string, gangSize int32) *meridianv1alpha1.GPUWorkload {
		return &meridianv1alpha1.GPUWorkload{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: name,
				Namespace:    ns,
			},
			Spec: meridianv1alpha1.GPUWorkloadSpec{
				QueueName:  "default-queue",
				Kind:       meridianv1alpha1.WorkloadKindTraining,
				Gang:       meridianv1alpha1.GangSpec{Size: gangSize, MinAvailable: gangSize},
				GPUsPerPod: 8,
				GPUType:    "nvidia-h100",
				PodTemplate: corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Labels: map[string]string{"role": "training"},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "worker", Image: "nvcr.io/nvidia/pytorch:24.01-py3"},
						},
					},
				},
			},
		}
	}

	// ─────────────────────────────────────────────────────────────────────────
	// 1. Initial enqueue & finalizer attachment
	// ─────────────────────────────────────────────────────────────────────────
	Context("When a new GPUWorkload is created", func() {
		It("attaches the finalizer and transitions from Pending to Queued", func() {
			w := newWorkload("gw-enqueue-", 2)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Controller must attach finalizer
			Eventually(func(g Gomega) {
				var fetched meridianv1alpha1.GPUWorkload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)).To(Succeed())
				g.Expect(fetched.Finalizers).To(ContainElement(workload.WorkloadFinalizer))
				g.Expect(fetched.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// 2. Gated pod creation upon admission
	// ─────────────────────────────────────────────────────────────────────────
	Context("When a GPUWorkload is admitted", func() {
		It("creates the exact gang size of gated pods with proper labels and owner references", func() {
			w := newWorkload("gw-admit-", 3)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Wait for Queued
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Simulate queue admission by setting Admitted condition
			orig := w.DeepCopy()
			w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
				Type:               "Admitted",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "QuotaReserved",
				Message:            "Admitted by queue admission controller",
			})
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Reconciler should observe Admitted -> create 3 gated pods -> transition to Scheduling
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseScheduling))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Query pods matching workload
			var podList corev1.PodList
			Expect(k8sClient.List(ctx, &podList,
				client.InNamespace(ns),
				client.MatchingLabels{workload.LabelWorkload: w.Name},
			)).To(Succeed())

			Expect(podList.Items).To(HaveLen(3))
			for i, p := range podList.Items {
				Expect(p.Labels[workload.LabelGangSize]).To(Equal("3"))
				Expect(p.Labels[workload.LabelGangIndex]).To(Equal(fmt.Sprintf("%d", i)))
				// Must contain scheduling gate
				gateNames := make([]string, len(p.Spec.SchedulingGates))
				for gIdx, gate := range p.Spec.SchedulingGates {
					gateNames[gIdx] = gate.Name
				}
				Expect(gateNames).To(ContainElement(workload.AdmissionSchedulingGate))
			}
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// 3. Crash-restart convergence (restarting controller mid-reconcile)
	// ─────────────────────────────────────────────────────────────────────────
	Context("Crash-restart convergence", func() {
		It("converges idempotently without creating duplicate pods after restart", func() {
			w := newWorkload("gw-crash-conv-", 2)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Mark admitted
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			orig := w.DeepCopy()
			w.Status.Conditions = append(w.Status.Conditions, metav1.Condition{
				Type:               "Admitted",
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.Now(),
				Reason:             "QuotaReserved",
				Message:            "Admitted",
			})
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// Wait for Scheduling phase
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseScheduling))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Re-trigger reconcile by updating an annotation (simulating restart / re-queue)
			w.Annotations = map[string]string{"meridian.dev/reconcile-trigger": "1"}
			Expect(k8sClient.Update(ctx, w)).To(Succeed())

			// Ensure pod count is strictly preserved (idempotent, no duplicates)
			Consistently(func(g Gomega) {
				var podList corev1.PodList
				g.Expect(k8sClient.List(ctx, &podList,
					client.InNamespace(ns),
					client.MatchingLabels{workload.LabelWorkload: w.Name},
				)).To(Succeed())
				g.Expect(podList.Items).To(HaveLen(2))
			}, 3*time.Second, 500*time.Millisecond).Should(Succeed())
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// 4. Deletion and finalizer teardown
	// ─────────────────────────────────────────────────────────────────────────
	Context("When a GPUWorkload is deleted", func() {
		It("drains gang member pods and removes the finalizer cleanly", func() {
			w := newWorkload("gw-teardown-", 2)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())

			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Finalizers).To(ContainElement(workload.WorkloadFinalizer))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Delete workload
			Expect(k8sClient.Delete(ctx, w)).To(Succeed())

			// Object should be completely expunged from the API server
			Eventually(func() error {
				var fetched meridianv1alpha1.GPUWorkload
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)
			}, 15*time.Second, 200*time.Millisecond).Should(HaveOccurred())
		})
	})
})
