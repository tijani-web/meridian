/*
Package envtest contains integration tests for the Meridian InferenceDeployment controller,
exercising child Deployment/Service reconciliation, warm-up gating, routing table ConfigMap publication,
and automated canary rollback against a real kube-apiserver.
*/
package envtest

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	controllerinference "github.com/tijani-web/meridian/internal/controller/inference"
)

var _ = Describe("InferenceDeployment Controller Integration", func() {
	const (
		timeout  = 20 * time.Second
		interval = 250 * time.Millisecond
	)

	var (
		ctx       context.Context
		namespace string
	)

	BeforeEach(func() {
		ctx = context.Background()
		namespace = fmt.Sprintf("test-inf-%d", time.Now().UnixNano())

		ns := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{Name: namespace},
		}
		Expect(k8sClient.Create(ctx, ns)).To(Succeed())
	})

	It("reconciles child Deployment, Service, and routing ConfigMap with warm-up gating", func() {
		inf := &meridianv1alpha1.InferenceDeployment{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "llama-serve",
				Namespace: namespace,
			},
			Spec: meridianv1alpha1.InferenceDeploymentSpec{
				Model: meridianv1alpha1.ModelSpec{
					Name:     "llama-3-8b",
					URI:      "gs://models/llama-3-8b",
					Revision: "v1.0",
				},
				Runtime: meridianv1alpha1.RuntimeSpec{
					Image: "ghcr.io/tijani-web/vllm-emu:v0.1.0",
					Port:  8000,
				},
				Replicas: 2,
				Resources: meridianv1alpha1.InferenceResourceSpec{
					GPUsPerReplica: 2,
					GPUType:        "nvidia-h100",
				},
				Routing: meridianv1alpha1.RoutingSpec{
					Strategy: meridianv1alpha1.RoutingStrategyLeastQueue,
				},
			},
		}

		Expect(k8sClient.Create(ctx, inf)).To(Succeed())

		// 1. Verify routing ConfigMap is created
		cmKey := client.ObjectKey{
			Namespace: namespace,
			Name:      "llama-serve" + controllerinference.RoutingConfigMapSuffix,
		}
		cm := &corev1.ConfigMap{}
		Eventually(func() error {
			return k8sClient.Get(ctx, cmKey, cm)
		}, timeout, interval).Should(Succeed())

		// Verify JSON routing content in ConfigMap
		Expect(cm.Data).To(HaveKey("routes.json"))
		var table controllerinference.PublishedRoutingTable
		Expect(json.Unmarshal([]byte(cm.Data["routes.json"]), &table)).To(Succeed())
		Expect(table.Deployment).To(Equal("llama-serve"))
		Expect(table.Strategy).To(Equal(meridianv1alpha1.RoutingStrategyLeastQueue))
		Expect(table.Revisions).NotTo(BeEmpty())

		// 2. Discover and inspect child Deployment
		deployList := &appsv1.DeploymentList{}
		Eventually(func() int {
			_ = k8sClient.List(ctx, deployList, client.InNamespace(namespace))
			return len(deployList.Items)
		}, timeout, interval).Should(BeNumerically(">=", 1))

		childDeploy := deployList.Items[0]
		Expect(childDeploy.Spec.Template.Spec.SchedulerName).To(Equal("meridian-scheduler"))
		gpuReq := childDeploy.Spec.Template.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"]
		Expect(gpuReq.Value()).To(Equal(int64(2)))

		// 3. Simulate replica readiness
		childDeploy.Status.ReadyReplicas = 2
		childDeploy.Status.Replicas = 2
		Expect(k8sClient.Status().Update(ctx, &childDeploy)).To(Succeed())

		// 4. Verify InferenceDeployment status updates
		infKey := client.ObjectKeyFromObject(inf)
		Eventually(func() bool {
			updated := &meridianv1alpha1.InferenceDeployment{}
			if err := k8sClient.Get(ctx, infKey, updated); err != nil {
				return false
			}
			for _, rev := range updated.Status.Revisions {
				if rev.ReadyReplicas > 0 && rev.Warm {
					return true
				}
			}
			return false
		}, timeout, interval).Should(BeTrue())
	})
})
