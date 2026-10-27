package envtest

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

var _ = Describe("InferenceDeployment Webhook", func() {
	ctx := context.Background()
	ns := "default"

	// validDep returns a minimal, fully valid InferenceDeployment.
	validDep := func(name string) *meridianv1alpha1.InferenceDeployment {
		return &meridianv1alpha1.InferenceDeployment{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: name,
				Namespace:    ns,
			},
			Spec: meridianv1alpha1.InferenceDeploymentSpec{
				Model: meridianv1alpha1.ModelSpec{
					Name: "llama-3-8b-instruct",
					URI:  "gs://meridian-models/llama-3-8b-instruct",
				},
				Runtime: meridianv1alpha1.RuntimeSpec{
					Image: "ghcr.io/tijani-web/vllm-emu:latest",
					Port:  8000,
				},
				Replicas: 1,
			},
		}
	}

	// ─────────────────────────────────────────────────────────────────────────
	// Defaulting
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Defaulting", func() {
		It("sets routing strategy to LeastQueue", func() {
			d := validDep("id-default-routing-")
			Expect(k8sClient.Create(ctx, d)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, d) })

			Expect(d.Spec.Routing.Strategy).To(Equal(meridianv1alpha1.RoutingStrategyLeastQueue))
		})

		It("defaults model revision to main", func() {
			d := validDep("id-default-rev-")
			d.Spec.Model.Revision = ""
			Expect(k8sClient.Create(ctx, d)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, d) })

			Expect(d.Spec.Model.Revision).To(Equal("main"))
		})

		It("defaults rollout strategy to Canary", func() {
			d := validDep("id-default-rollout-")
			Expect(k8sClient.Create(ctx, d)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, d) })

			Expect(d.Spec.Rollout.Strategy).To(Equal(meridianv1alpha1.RolloutStrategyCanary))
		})

		It("defaults replicas to 1 when zero", func() {
			d := validDep("id-default-replicas-")
			d.Spec.Replicas = 0
			Expect(k8sClient.Create(ctx, d)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, d) })

			Expect(d.Spec.Replicas).To(BeNumerically("==", 1))
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Validation — create
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Validation on Create", func() {
		It("accepts a fully valid InferenceDeployment", func() {
			d := validDep("id-valid-")
			Expect(k8sClient.Create(ctx, d)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, d) })
		})

		It("rejects missing model name", func() {
			d := validDep("id-no-model-name-")
			d.Spec.Model.Name = ""
			err := k8sClient.Create(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("model.name"))
		})

		It("rejects missing model URI", func() {
			d := validDep("id-no-uri-")
			d.Spec.Model.URI = ""
			err := k8sClient.Create(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("model.uri"))
		})

		It("rejects missing runtime image", func() {
			d := validDep("id-no-image-")
			d.Spec.Runtime.Image = ""
			err := k8sClient.Create(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("runtime.image"))
		})

		It("rejects invalid port 0", func() {
			d := validDep("id-bad-port-")
			d.Spec.Runtime.Port = 0
			err := k8sClient.Create(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("runtime.port"))
		})

		It("rejects non-monotonic canary steps", func() {
			d := validDep("id-nonmono-canary-")
			d.Spec.Rollout.Strategy = meridianv1alpha1.RolloutStrategyCanary
			d.Spec.Rollout.Steps = []meridianv1alpha1.RolloutStep{
				{Weight: 50},
				{Weight: 25}, // drops — not monotonically increasing
				{Weight: 100},
			}
			err := k8sClient.Create(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("monotonically"))
		})

		It("rejects LoadShedding.MaxQueueDepth=0", func() {
			d := validDep("id-bad-loadshed-")
			d.Spec.Routing.LoadShedding = &meridianv1alpha1.LoadSheddingSpec{
				MaxQueueDepth: 0,
			}
			err := k8sClient.Create(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("maxQueueDepth"))
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Validation — update (immutability)
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Validation on Update", func() {
		It("rejects Model.URI change after creation", func() {
			d := validDep("id-immut-uri-")
			Expect(k8sClient.Create(ctx, d)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, d) })

			d.Spec.Model.URI = "gs://meridian-models/llama-3-70b-instruct"
			err := k8sClient.Update(ctx, d)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable"))
		})
	})
})
