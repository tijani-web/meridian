package envtest

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

var _ = Describe("GPUQueue Webhook", func() {
	ctx := context.Background()

	// ─────────────────────────────────────────────────────────────────────────
	// Defaulting
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Defaulting", func() {
		It("applies preemption defaults on create", func() {
			q := &meridianv1alpha1.GPUQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gq-default-",
				},
				Spec: meridianv1alpha1.GPUQueueSpec{
					NominalQuota: map[string]int32{"nvidia-h100": 8},
				},
			}
			Expect(k8sClient.Create(ctx, q)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, q) })

			// Defaulting webhook must have set ReclaimWithinQueue and ReclaimBorrowed to true.
			Expect(q.Spec.Preemption.ReclaimWithinQueue).To(BeTrue())
			Expect(q.Spec.Preemption.ReclaimBorrowed).To(BeTrue())
			// CheckpointGracePeriod must be defaulted to 60s.
			Expect(q.Spec.Preemption.CheckpointGracePeriod.Duration.Seconds()).To(BeNumerically("==", 60))
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Validation — create
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Validation on Create", func() {
		It("accepts a fully valid GPUQueue", func() {
			q := &meridianv1alpha1.GPUQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gq-valid-",
				},
				Spec: meridianv1alpha1.GPUQueueSpec{
					NominalQuota:   map[string]int32{"nvidia-h100": 16},
					BorrowingLimit: map[string]int32{"nvidia-h100": 8},
					LendingLimit:   map[string]int32{"nvidia-h100": 4},
				},
			}
			Expect(k8sClient.Create(ctx, q)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, q) })
		})

		It("rejects a GPUQueue with empty NominalQuota", func() {
			q := &meridianv1alpha1.GPUQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gq-empty-quota-",
				},
				Spec: meridianv1alpha1.GPUQueueSpec{
					NominalQuota: map[string]int32{},
				},
			}
			err := k8sClient.Create(ctx, q)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("nominalQuota"))
		})

		It("rejects BorrowingLimit referencing unknown GPU type", func() {
			q := &meridianv1alpha1.GPUQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gq-bad-borrow-",
				},
				Spec: meridianv1alpha1.GPUQueueSpec{
					NominalQuota:   map[string]int32{"nvidia-h100": 16},
					BorrowingLimit: map[string]int32{"nvidia-a100": 4}, // a100 not in nominal
				},
			}
			err := k8sClient.Create(ctx, q)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("borrowingLimit"))
		})

		It("rejects LendingLimit exceeding NominalQuota", func() {
			q := &meridianv1alpha1.GPUQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gq-lend-exceed-",
				},
				Spec: meridianv1alpha1.GPUQueueSpec{
					NominalQuota: map[string]int32{"nvidia-h100": 8},
					LendingLimit: map[string]int32{"nvidia-h100": 16}, // 16 > 8
				},
			}
			err := k8sClient.Create(ctx, q)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("lendingLimit"))
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Validation — delete
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Validation on Delete", func() {
		// We cannot easily inject .Status.AdmittedWorkloads > 0 through the
		// webhook path without a status subresource update, so this test verifies
		// that queues with zero admitted workloads can be deleted cleanly.
		It("allows deletion when no workloads are admitted", func() {
			q := &meridianv1alpha1.GPUQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gq-delete-ok-",
				},
				Spec: meridianv1alpha1.GPUQueueSpec{
					NominalQuota: map[string]int32{"nvidia-h100": 4},
				},
			}
			Expect(k8sClient.Create(ctx, q)).To(Succeed())
			Expect(k8sClient.Delete(ctx, q)).To(Succeed())
		})
	})
})
