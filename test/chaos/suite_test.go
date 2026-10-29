/*
Package chaos implements chaos engineering tests that validate Meridian's HA
properties against a live kind cluster.

Prerequisites:
  - kind cluster running: make kind-up
  - Meridian deployed: helm install meridian charts/meridian -n meridian-system --create-namespace
  - KUBECONFIG set to the kind cluster kubeconfig

Run all chaos tests:

	KUBECONFIG=$(kind get kubeconfig --name meridian-ci) \
	  go test -v -timeout 15m ./test/chaos/...
*/
package chaos

import (
	"context"
	"os"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"sigs.k8s.io/controller-runtime/pkg/client"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
)

var (
	k8sClient  client.Client
	kubeClient kubernetes.Interface
	ctx        context.Context
	cancel     context.CancelFunc
)

const (
	// meridianNS is the namespace where Meridian control plane components run.
	meridianNS = "meridian-system"
	// testNS is the namespace for chaos test workloads.
	testNS = "chaos-test"
	// leaderLeaseWindow is the maximum time expected for lease handover after
	// killing the active leader pod. Derived from leaseDuration(15s) + margin.
	leaderLeaseWindow = 30 * time.Second
)

func TestChaos(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Meridian Chaos Suite")
}

var _ = BeforeSuite(func() {
	ctx, cancel = context.WithCancel(context.Background())

	kubeconfig := os.Getenv("KUBECONFIG")
	Expect(kubeconfig).NotTo(BeEmpty(), "KUBECONFIG must be set to run chaos tests")

	restCfg, err := clientcmd.BuildConfigFromFlags("", kubeconfig)
	Expect(err).NotTo(HaveOccurred())

	scheme := meridianv1alpha1.SchemeBuilder.GroupVersion.String()
	_ = scheme // suppress unused warning; types registered via init()

	k8sClient, err = client.New(restCfg, client.Options{})
	Expect(err).NotTo(HaveOccurred())

	kubeClient, err = kubernetes.NewForConfig(restCfg)
	Expect(err).NotTo(HaveOccurred())

	// Ensure test namespace exists.
	ns := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: testNS}}
	_ = k8sClient.Create(ctx, ns) // idempotent: ignore AlreadyExists
})

var _ = AfterSuite(func() {
	cancel()
	// Clean up test namespace after the suite.
	ns := &corev1.Namespace{}
	if err := k8sClient.Get(ctx, types.NamespacedName{Name: testNS}, ns); err == nil {
		_ = k8sClient.Delete(ctx, ns)
	}
})
