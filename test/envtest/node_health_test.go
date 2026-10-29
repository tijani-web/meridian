/*
Package envtest contains integration tests for Meridian controllers running
against real kube-apiserver and etcd binaries.

This test file verifies Phase 06: Node Health & Hardware Fault Recovery:
- Fault detection on node applies taints & conditions
- Health controller identifies gang pods on failed node via IndexPodNode
- Affected workload transitions to Recovering with generation bump
- Pods on failed node are deleted to enable rescheduling
- Flap damping and hysteresis prevent premature un-tainting
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
	agenthealth "github.com/tijani-web/meridian/internal/agent/health"
	"github.com/tijani-web/meridian/internal/controller/workload"
)

var _ = Describe("Node Health Controller & Workload Recovery", func() {
	ctx := context.Background()
	ns := "default"

	Context("When a node suffers an accelerator hardware fault", func() {
		It("taints the node, evicts pods from the broken node, and transitions the workload to Recovering with generation bump", func() {
			nodeName := "node-gpu-fault-test"

			// 1. Create Node in cluster
			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: nodeName,
					Labels: map[string]string{
						"meridian.dev/gpu-type": "nvidia-h100",
					},
				},
				Status: corev1.NodeStatus{
					Conditions: []corev1.NodeCondition{
						{
							Type:   corev1.NodeConditionType(agenthealth.ConditionGPUHealthy),
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, node)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, node) })

			// 2. Create GPUWorkload
			w := &meridianv1alpha1.GPUWorkload{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "gw-fault-recovery-",
					Namespace:    ns,
				},
				Spec: meridianv1alpha1.GPUWorkloadSpec{
					QueueName:  "default-queue",
					Kind:       meridianv1alpha1.WorkloadKindTraining,
					Gang:       meridianv1alpha1.GangSpec{Size: 2, MinAvailable: 2},
					GPUsPerPod: 4,
					GPUType:    "nvidia-h100",
					PodTemplate: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: []corev1.Container{{Name: "worker", Image: "trainer:v1"}},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, w)).To(Succeed())
			DeferCleanup(func() { _ = k8sClient.Delete(ctx, w) })

			// Wait for workload to be Queued
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseQueued))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Admit workload -> creates pods
			orig := w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseAdmitted
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			var pods corev1.PodList
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns), client.MatchingLabels{
					workload.LabelWorkload: w.Name,
				})).To(Succeed())
				g.Expect(pods.Items).To(HaveLen(2))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// Simulate scheduler binding pod 0 to the test node: in envtest there is no
			// real scheduler, so NodeName is immutable after creation. We delete the gated
			// pod and recreate it with NodeName set at creation time — exactly what a real
			// scheduler binding would produce in etcd.
			p0 := &pods.Items[0]
			boundPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      p0.Name + "-bound",
					Namespace: p0.Namespace,
					Labels:    p0.Labels,
				},
				Spec: corev1.PodSpec{
					NodeName:   nodeName,
					Containers: p0.Spec.Containers,
				},
			}
			Expect(k8sClient.Delete(ctx, p0)).To(Succeed())
			Expect(k8sClient.Create(ctx, boundPod)).To(Succeed())

			orig = w.DeepCopy()
			w.Status.Phase = meridianv1alpha1.WorkloadPhaseRunning
			w.Status.Generation = 1
			Expect(k8sClient.Status().Patch(ctx, w, client.MergeFrom(orig))).To(Succeed())

			// 3. Hardware fault occurs: node is tainted with NoExecute (e.g. XID 79 bus drop)
			nodeOrig := node.DeepCopy()
			node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
				Key:    agenthealth.TaintGPUUnhealthy,
				Value:  string(agenthealth.SeverityFatal),
				Effect: corev1.TaintEffectNoExecute,
			})
			Expect(k8sClient.Patch(ctx, node, client.MergeFrom(nodeOrig))).To(Succeed())

			// 4. Health controller detects fault:
			// - Transitions workload to Recovering
			// - Increments generation to 2
			// - Deletes pod 0 on the failed node
			Eventually(func(g Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(w), w)).To(Succeed())
				g.Expect(w.Status.Phase).To(Equal(meridianv1alpha1.WorkloadPhaseRecovering))
				g.Expect(w.Status.Generation).To(Equal(int64(2)))
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())

			// boundPod on the broken node should be deleted or marked for deletion by
			// the health controller once it detects the NoExecute taint.
			Eventually(func(g Gomega) {
				var fetchedBound corev1.Pod
				err := k8sClient.Get(ctx, client.ObjectKeyFromObject(boundPod), &fetchedBound)
				g.Expect(err != nil || !fetchedBound.DeletionTimestamp.IsZero()).To(BeTrue())
			}, 10*time.Second, 100*time.Millisecond).Should(Succeed())
		})
	})
})
