package envtest

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

var _ = Describe("GPUWorkload Webhook", func() {
	ctx := context.Background()
	ns := "default"

	// validWorkload returns a complete, valid GPUWorkload for mutation in individual test cases.
	validWorkload := func(name string) *meridianv1alpha1.GPUWorkload {
		return &meridianv1alpha1.GPUWorkload{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: name,
				Namespace:    ns,
			},
			Spec: meridianv1alpha1.GPUWorkloadSpec{
				QueueName:  "default-queue",
				Kind:       meridianv1alpha1.WorkloadKindTraining,
				Gang:       meridianv1alpha1.GangSpec{Size: 4, MinAvailable: 4},
				GPUsPerPod: 8,
				GPUType:    "nvidia-h100",
				PodTemplate: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: "trainer", Image: "nvcr.io/nvidia/pytorch:24.01-py3"},
						},
					},
				},
			},
		}
	}

	// ─────────────────────────────────────────────────────────────────────────
	// Defaulting
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Defaulting", func() {
		It("sets MinAvailable=Size when omitted", func() {
			w := &meridianv1alpha1.GPUWorkload{
				ObjectMeta: metav1.ObjectMeta{GenerateName: "gw-default-gang-", Namespace: ns},
				Spec: meridianv1alpha1.GPUWorkloadSpec{
					QueueName:  "default-queue",
					Gang:       meridianv1alpha1.GangSpec{Size: 4},
					GPUsPerPod: 8,
					GPUType:    "nvidia-h100",
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "trainer", Image: "nvcr.io/nvidia/pytorch:24.01-py3"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// MinAvailable must be defaulted to Size.
			Expect(w.Spec.Gang.MinAvailable).To(Equal(w.Spec.Gang.Size))
		})

		It("defaults topology preference to None", func() {
			w := validWorkload("gw-default-topo-")
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			Expect(w.Spec.Topology.Preference).To(Equal(meridianv1alpha1.TopologyPreferenceNone))
		})

		It("defaults Kind to Training", func() {
			w := validWorkload("gw-default-kind-")
			w.Spec.Kind = ""
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			Expect(w.Spec.Kind).To(Equal(meridianv1alpha1.WorkloadKindTraining))
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Validation — create
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Validation on Create", func() {
		It("accepts a fully valid GPUWorkload", func() {
			w := validWorkload("gw-valid-")
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })
		})

		It("rejects missing QueueName", func() {
			w := validWorkload("gw-no-queue-")
			w.Spec.QueueName = ""
			err := k8sClient.Create(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("queueName"))
		})

		It("rejects Gang.MinAvailable > Gang.Size", func() {
			w := validWorkload("gw-bad-min-")
			w.Spec.Gang = meridianv1alpha1.GangSpec{Size: 2, MinAvailable: 4}
			err := k8sClient.Create(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("minAvailable"))
		})

		It("rejects zero GPUsPerPod", func() {
			w := validWorkload("gw-zero-gpu-")
			w.Spec.GPUsPerPod = 0
			err := k8sClient.Create(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("gpusPerPod"))
		})

		It("rejects Topology.Required=true with Preference=None", func() {
			w := validWorkload("gw-bad-topo-")
			w.Spec.Topology = meridianv1alpha1.TopologySpec{
				Preference: meridianv1alpha1.TopologyPreferenceNone,
				Required:   true,
			}
			err := k8sClient.Create(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("topology"))
		})

		It("rejects Checkpoint.Enabled=true with empty Path", func() {
			w := validWorkload("gw-ckpt-no-path-")
			w.Spec.Checkpoint = meridianv1alpha1.CheckpointSpec{
				Enabled: true,
				// Path intentionally omitted
			}
			err := k8sClient.Create(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("checkpoint.path"))
		})

		It("rejects ElasticSpec.MaxSize < MinSize", func() {
			elastic := &meridianv1alpha1.ElasticSpec{MinSize: 4, MaxSize: 2}
			w := validWorkload("gw-elastic-invert-")
			w.Spec.RestartPolicy.Elastic = elastic
			err := k8sClient.Create(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("elastic.maxSize"))
		})
	})

	// ─────────────────────────────────────────────────────────────────────────
	// Validation — update (immutability)
	// ─────────────────────────────────────────────────────────────────────────
	Describe("Validation on Update", func() {
		It("rejects QueueName change after creation", func() {
			w := validWorkload("gw-immut-queue-")
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			w.Spec.QueueName = "another-queue"
			err := k8sClient.Update(ctx, w)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("immutable"))
		})
	})
})
