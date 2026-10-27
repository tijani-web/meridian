/*
Package envtest contains integration tests for the Meridian controllers.
This file tests the GPUQueue controller's quota accounting, DRF-lite admission,
scheduling gate release, and reclaim preemption using real apiserver admission.
*/
package envtest

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/controller/queue"
	"github.com/tijani-web/meridian/internal/controller/workload"
)

var _ = Describe("Queue Controller", func() {
	ctx := context.Background()
	ns := "default"

	newQueue := func(name string, nominalH100, borrowH100 int32) *meridianv1alpha1.GPUQueue {
		return &meridianv1alpha1.GPUQueue{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: meridianv1alpha1.GPUQueueSpec{
				Priority: 100,
				NominalQuota: map[string]int32{
					"nvidia-h100": nominalH100,
				},
				BorrowingLimit: map[string]int32{
					"nvidia-h100": borrowH100,
				},
				LendingLimit: map[string]int32{
					"nvidia-h100": nominalH100,
				},
			},
		}
	}

	newWorkloadForQueue := func(name, queueName string, gangSize, gpusPerPod int32) *meridianv1alpha1.GPUWorkload {
		return &meridianv1alpha1.GPUWorkload{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: name,
				Namespace:    ns,
			},
			Spec: meridianv1alpha1.GPUWorkloadSpec{
				QueueName:  queueName,
				Kind:       meridianv1alpha1.WorkloadKindTraining,
				Gang:       meridianv1alpha1.GangSpec{Size: gangSize, MinAvailable: gangSize},
				GPUsPerPod: gpusPerPod,
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
	// 1. Quota accounting & atomic admission under nominal quota
	// ─────────────────────────────────────────────────────────────────────────
	Context("When a workload is submitted within queue nominal quota", func() {
		It("admits the workload and clears scheduling gates from all gang pods", func() {
			qName := "team-alpha"
			q := newQueue(qName, 32, 16)
			Expect(k8sClient.Create(ctx, q)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, q) })

			// Submit workload demanding 2 pods * 8 GPUs = 16 GPUs (within 32 nominal)
			w := newWorkloadForQueue("gw-alpha-", qName, 2, 8)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Workload should transition to Queued then Admitted by Queue controller
			Eventually(func(g Gomega) {
				var fetched meridianv1alpha1.GPUWorkload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)).To(Succeed())
				
				// Verify Admitted condition exists and is True
				var admittedCond *metav1.Condition
				for i := range fetched.Status.Conditions {
					if fetched.Status.Conditions[i].Type == "Admitted" {
						admittedCond = &fetched.Status.Conditions[i]
						break
					}
				}
				g.Expect(admittedCond).NotTo(BeNil())
				g.Expect(admittedCond.Status).To(Equal(metav1.ConditionTrue))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Workload controller will then transition it to Scheduling and create gang pods
			Eventually(func(g Gomega) {
				var fetched meridianv1alpha1.GPUWorkload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)).To(Succeed())
				g.Expect(fetched.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseScheduling))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Verify gang pods are created and the admission gate is removed
			Eventually(func(g Gomega) {
				var podList corev1.PodList
				g.Expect(k8sClient.List(ctx, &podList,
					client.InNamespace(ns),
					client.MatchingLabels{workload.LabelWorkload: w.Name},
				)).To(Succeed())
				g.Expect(podList.Items).To(HaveLen(2))

				for _, p := range podList.Items {
					for _, gate := range p.Spec.SchedulingGates {
						g.Expect(gate.Name).NotTo(Equal(queue.AdmissionSchedulingGate))
					}
				}
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Verify GPUQueue status usage is tracked
			Eventually(func(g Gomega) {
				var fetchedQ meridianv1alpha1.GPUQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: qName}, &fetchedQ)).To(Succeed())
				g.Expect(fetchedQ.Status.Usage).NotTo(BeNil())
				usage := fetchedQ.Status.Usage["nvidia-h100"]
				g.Expect(usage.Used).To(Equal(int32(16)))
				g.Expect(usage.Borrowed).To(Equal(int32(0)))
				g.Expect(fetchedQ.Status.AdmittedWorkloads).To(Equal(int32(1)))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// 2. Borrowing limit enforcement
	// ─────────────────────────────────────────────────────────────────────────
	Context("When a workload exceeds nominal quota but is within borrowing limit", func() {
		It("admits the workload and flags it as borrowed", func() {
			qName := "team-borrower"
			q := newQueue(qName, 8, 16)
			Expect(k8sClient.Create(ctx, q)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, q) })

			// Workload demanding 2 pods * 8 GPUs = 16 GPUs (nominal is 8, so 8 borrowed)
			w := newWorkloadForQueue("gw-borrow-", qName, 2, 8)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			Eventually(func(g Gomega) {
				var fetched meridianv1alpha1.GPUWorkload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)).To(Succeed())

				var admittedCond *metav1.Condition
				for i := range fetched.Status.Conditions {
					if fetched.Status.Conditions[i].Type == "Admitted" {
						admittedCond = &fetched.Status.Conditions[i]
						break
					}
				}
				g.Expect(admittedCond).NotTo(BeNil())
				g.Expect(admittedCond.Status).To(Equal(metav1.ConditionTrue))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Verify Queue usage reflects borrowed GPUs
			Eventually(func(g Gomega) {
				var fetchedQ meridianv1alpha1.GPUQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: qName}, &fetchedQ)).To(Succeed())
				usage := fetchedQ.Status.Usage["nvidia-h100"]
				g.Expect(usage.Used).To(Equal(int32(16)))
				g.Expect(usage.Borrowed).To(Equal(int32(8)))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// 3. Admission blocking when exceeding borrowing limit
	// ─────────────────────────────────────────────────────────────────────────
	Context("When a workload exceeds both nominal quota and borrowing limit", func() {
		It("keeps the workload in Queued phase without admitting it", func() {
			qName := "team-capped"
			q := newQueue(qName, 8, 8) // max total = 16
			Expect(k8sClient.Create(ctx, q)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, q) })

			// Workload demanding 4 pods * 8 GPUs = 32 GPUs (exceeds 16 max)
			w := newWorkloadForQueue("gw-capped-", qName, 4, 8)
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Workload will become Queued by Workload controller
			Eventually(func(g Gomega) {
				var fetched meridianv1alpha1.GPUWorkload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)).To(Succeed())
				g.Expect(fetched.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Queue should show pending workloads = 1
			Eventually(func(g Gomega) {
				var fetchedQ meridianv1alpha1.GPUQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: qName}, &fetchedQ)).To(Succeed())
				g.Expect(fetchedQ.Status.PendingWorkloads).To(Equal(int32(1)))
			}, 15*time.Second, 200*time.Millisecond).Should(Succeed())

			// Confirm it is not admitted
			Consistently(func(g Gomega) {
				var fetched meridianv1alpha1.GPUWorkload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), &fetched)).To(Succeed())
				for _, c := range fetched.Status.Conditions {
					if c.Type == "Admitted" {
						g.Expect(c.Status).NotTo(Equal(metav1.ConditionTrue))
					}
				}
			}, 3*time.Second, 500*time.Millisecond).Should(Succeed())
		})
	})
})
