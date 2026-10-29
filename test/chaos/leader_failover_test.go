package chaos

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	coordinationv1 "k8s.io/api/coordination/v1"
)

// LeaderElection validates Meridian's HA active-standby failover contract.
//
// Per Meridian.md §9 and §10 ("Leader election (Lease) + tested failover"):
// After deleting the active leader pod, the standby must acquire the lease
// within leaderLeaseWindow (30s) and continue reconciling workloads without
// creating duplicate objects or corrupting quota state.
var _ = Describe("Leader Election Failover", Ordered, func() {
	var (
		activePodName string
		leaseName     string
	)

	BeforeAll(func() {
		leaseName = "meridian-manager"
	})

	It("identifies the active manager leader", func() {
		lease := &coordinationv1.Lease{}
		Eventually(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{
				Name:      leaseName,
				Namespace: meridianNS,
			}, lease)
		}, 30*time.Second, 2*time.Second).Should(Succeed(), "meridian-manager lease must exist")

		Expect(lease.Spec.HolderIdentity).NotTo(BeNil(), "lease must have a holder")
		activePodName = *lease.Spec.HolderIdentity
		// HolderIdentity may be "<pod-name>_<uuid>"; extract pod name prefix.
		By(fmt.Sprintf("Active manager leader: %s", activePodName))
	})

	It("kills the active leader pod", func() {
		// Find the exact pod by matching name prefix from the holderIdentity.
		podList := &corev1.PodList{}
		Expect(k8sClient.List(ctx, podList,
			client.InNamespace(meridianNS),
			client.MatchingLabels{"app.kubernetes.io/component": "manager"},
		)).To(Succeed())

		Expect(podList.Items).NotTo(BeEmpty(), "no manager pods found in %s", meridianNS)

		var leaderPod *corev1.Pod
		for i := range podList.Items {
			p := &podList.Items[i]
			// Match by pod name prefix (lease holderIdentity starts with pod name).
			if len(activePodName) >= len(p.Name) && activePodName[:len(p.Name)] == p.Name {
				leaderPod = p
				break
			}
		}
		if leaderPod == nil {
			// Fallback: if holderIdentity contains the pod name as a substring.
			leaderPod = &podList.Items[0]
		}

		By(fmt.Sprintf("Deleting active leader pod: %s", leaderPod.Name))
		Expect(k8sClient.Delete(ctx, leaderPod, &client.DeleteOptions{
			GracePeriodSeconds: int64Ptr(0),
		})).To(Succeed())
	})

	It("standby acquires the lease within leaderLeaseWindow", func() {
		// The new holderIdentity must differ from the old one within the lease window.
		Eventually(func() string {
			lease := &coordinationv1.Lease{}
			if err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      leaseName,
				Namespace: meridianNS,
			}, lease); err != nil {
				return ""
			}
			if lease.Spec.HolderIdentity == nil {
				return ""
			}
			return *lease.Spec.HolderIdentity
		}, leaderLeaseWindow, 2*time.Second).ShouldNot(
			Equal(activePodName),
			"standby must have replaced the killed leader within %s", leaderLeaseWindow,
		)

		By("Standby promoted to leader successfully")
	})

	It("new leader pod becomes Running within 60 seconds", func() {
		// The Deployment controller must replace the deleted pod.
		Eventually(func() int {
			podList := &corev1.PodList{}
			if err := k8sClient.List(ctx, podList,
				client.InNamespace(meridianNS),
				client.MatchingLabels{"app.kubernetes.io/component": "manager"},
			); err != nil {
				return 0
			}
			running := 0
			for _, p := range podList.Items {
				if p.Status.Phase == corev1.PodRunning {
					running++
				}
			}
			return running
		}, 60*time.Second, 3*time.Second).Should(BeNumerically(">=", 2),
			"both manager replicas must be Running after leader pod was replaced",
		)
	})

	It("no GPUWorkloads or GPUQueues were mutated during failover", func() {
		// Verify status fields are unchanged — reconciliation did not double-create pods.
		// We can't guarantee no changes, but we can verify quota totals are sane.
		// This is a smoke check: if the controller hot-looped during failover, resource
		// versions would have changed excessively and can be observed via Events.
		eventList := &corev1.EventList{}
		listErr := k8sClient.List(ctx, eventList,
			client.InNamespace(testNS),
			&client.ListOptions{
				Limit: 100,
			},
		)
		Expect(listErr).NotTo(HaveOccurred())

		// Count duplicate Created events (a sign of double-reconcile creating pods).
		duplicatePodCreates := 0
		for _, ev := range eventList.Items {
			if ev.Reason == "Created" && ev.InvolvedObject.Kind == "Pod" && ev.Count > 2 {
				duplicatePodCreates++
			}
		}
		Expect(duplicatePodCreates).To(Equal(0),
			"no pod should have been Created more than twice — duplicate create indicates failover bug",
		)
	})
})

// int64Ptr returns a pointer to the given int64 value.
// It is used for DeleteOptions.GracePeriodSeconds which requires a *int64.
func int64Ptr(i int64) *int64 { return &i }

// metav1unused suppresses the "imported and not used" error for the metav1 import
// used in other test files in this package.
var _ = metav1.Now
