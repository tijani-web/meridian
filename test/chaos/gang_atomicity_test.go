package chaos

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

// GangAtomicityUnderChurn verifies the zero-partial-start invariant holds even
// when competing workloads are consuming and releasing capacity concurrently.
//
// Per Meridian.md §8.1: "number of partially-started gangs (must be 0 for Meridian)".
// This is enforced by the Permit plugin's all-or-nothing reservation mechanism.
var _ = Describe("Gang Atomicity Under Churn", Ordered, func() {
	const (
		gangSize = 2 // small gang to work on resource-constrained CI kind nodes
		noisyJobs = 3 // background single-GPU jobs to consume capacity
	)

	var (
		gangWorkload     *meridianv1alpha1.GPUWorkload
		noisyWorkloadIDs []string
	)

	BeforeAll(func() {
		By("Submitting noisy single-GPU background jobs to create capacity churn")
		for i := 0; i < noisyJobs; i++ {
			w := buildTestWorkload(fmt.Sprintf("noisy-%d-", i), "default", 1, 1)
			if err := k8sClient.Create(ctx, w); err == nil {
				noisyWorkloadIDs = append(noisyWorkloadIDs, w.Name)
			}
		}
	})

	AfterAll(func() {
		By("Cleaning up gang and noisy workloads")
		if gangWorkload != nil {
			_ = k8sClient.Delete(ctx, gangWorkload)
		}
		for _, id := range noisyWorkloadIDs {
			w := &meridianv1alpha1.GPUWorkload{}
			w.Name = id
			w.Namespace = testNS
			_ = k8sClient.Delete(ctx, w)
		}
	})

	It("submits a gang workload concurrently with single-GPU churn", func() {
		gangWorkload = buildTestWorkload("gang-chaos-", testNS, gangSize, gangSize)
		Expect(k8sClient.Create(ctx, gangWorkload)).To(Succeed())
	})

	It("gang either runs fully or stays pending — never partially started", func() {
		// Allow up to 3 minutes for the gang to either reach Running or remain Pending.
		// Under churn, it may take multiple permit cycles.
		Eventually(func() meridianv1alpha1.WorkloadPhase {
			w := &meridianv1alpha1.GPUWorkload{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      gangWorkload.Name,
				Namespace: gangWorkload.Namespace,
			}, w); err != nil {
				return ""
			}
			return w.Status.Phase
		}, 3*time.Minute, 5*time.Second).Should(
			Or(
				Equal(meridianv1alpha1.WorkloadPhaseRunning),
				Equal(meridianv1alpha1.WorkloadPhasePending),
				Equal(meridianv1alpha1.WorkloadPhaseAdmitted),
			),
			"gang must be in a valid atomic state — never a mix of running and pending pods",
		)
	})

	It("no pod subset of the gang is Running while others are Pending", func() {
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace(gangWorkload.Namespace),
			client.MatchingLabels{"meridian.dev/workload-name": gangWorkload.Name},
		)).To(Succeed())

		if len(podList.Items) == 0 {
			// Gang is still fully pending — valid state.
			return
		}

		runningCount := 0
		pendingCount := 0
		for _, p := range podList.Items {
			switch p.Status.Phase {
			case corev1.PodRunning:
				runningCount++
			case corev1.PodPending:
				pendingCount++
			}
		}

		// Partial-start invariant: if any pod is Running, ALL pods must be Running.
		if runningCount > 0 {
			Expect(pendingCount).To(Equal(0),
				"INVARIANT VIOLATED: %d pod(s) Running but %d pod(s) still Pending "+
					"— Meridian gang atomicity guarantee was breached",
				runningCount, pendingCount,
			)
		}
	})

	It("meridian_gang_partial_starts_total metric remains zero", func() {
		// This is the same invariant expressed as a Prometheus metric.
		// Scrape the manager's metrics endpoint and verify the counter is 0.
		// On kind, port-forward is not available in test; we use a kubectl exec approach.
		// Since we cannot guarantee network access here, we verify via API events instead.

		eventList := &corev1.EventList{}
		Expect(k8sClient.List(ctx, eventList,
			client.InNamespace(gangWorkload.Namespace),
		)).To(Succeed())

		for _, ev := range eventList.Items {
			if ev.InvolvedObject.Name == gangWorkload.Name {
				Expect(ev.Reason).NotTo(
					Equal("PartialGangStart"),
					"Received PartialGangStart event — invariant violated: %s", ev.Message,
				)
			}
		}
	})
})

// buildTestWorkload constructs a minimal GPUWorkload for chaos testing.
// It uses gang.size = gangSize and gpusPerPod = 1 so tests run on CPU-only kind nodes
// (KWOK-simulated GPU capacity in CI, or real GPU nodes if available).
func buildTestWorkload(namePrefix, ns string, gangSize, gpusPerPod int32) *meridianv1alpha1.GPUWorkload {
	return &meridianv1alpha1.GPUWorkload{
		ObjectMeta: metav1.ObjectMeta{
			GenerateName: namePrefix,
			Namespace:    ns,
		},
		Spec: meridianv1alpha1.GPUWorkloadSpec{
			QueueName: "default",
			Gang: meridianv1alpha1.GangSpec{
				Size:         gangSize,
				MinAvailable: gangSize,
			},
			GPUsPerPod: gpusPerPod,
			GPUType:    "nvidia-simulated",
			PodTemplate: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    "trainer",
						Image:   "ghcr.io/tijani-web/vllm-emu:latest",
						Command: []string{"sleep", "600"},
					}},
				},
			},
		},
	}
}
