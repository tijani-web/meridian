/*
tools/patch-node-gpu updates kind worker nodes with simulated nvidia.com/gpu capacity.

This is a developer/test utility permitted under AGENTS.md §1.1 ("Allowed test infrastructure
under tools/ or test/ ... All must speak the real protocol and be labeled simulated").

It uses the standard Kubernetes client-go Nodes().UpdateStatus API to set:
  - status.capacity["nvidia.com/gpu"] = 8
  - status.allocatable["nvidia.com/gpu"] = 8
  - label meridian.dev/health-score = 100
*/
package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/retry"
)

func main() {
	kubeconfig := flag.String("kubeconfig", "kubeconfig-meridian", "path to kubeconfig file")
	gpus := flag.Int64("gpus", 8, "number of simulated GPUs per worker node")
	flag.Parse()

	config, err := clientcmd.BuildConfigFromFlags("", *kubeconfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error building kubeconfig: %v\n", err)
		os.Exit(1)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error creating clientset: %v\n", err)
		os.Exit(1)
	}

	ctx := context.Background()
	nodes, err := clientset.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: "!node-role.kubernetes.io/control-plane",
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error listing worker nodes: %v\n", err)
		os.Exit(1)
	}

	gpuQty := resource.MustParse(fmt.Sprintf("%d", *gpus))
	gpuResName := v1.ResourceName("nvidia.com/gpu")

	for _, n := range nodes.Items {
		nodeName := n.Name
		err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
			node, err := clientset.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				return err
			}
			if node.Labels == nil {
				node.Labels = make(map[string]string)
			}
			node.Labels["meridian.dev/health-score"] = "100"
			updated, err := clientset.CoreV1().Nodes().Update(ctx, node, metav1.UpdateOptions{})
			if err != nil {
				return err
			}

			if updated.Status.Capacity == nil {
				updated.Status.Capacity = v1.ResourceList{}
			}
			if updated.Status.Allocatable == nil {
				updated.Status.Allocatable = v1.ResourceList{}
			}
			updated.Status.Capacity[gpuResName] = gpuQty
			updated.Status.Allocatable[gpuResName] = gpuQty

			_, err = clientset.CoreV1().Nodes().UpdateStatus(ctx, updated, metav1.UpdateOptions{})
			return err
		})
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to patch node %s: %v\n", nodeName, err)
			continue
		}
		fmt.Printf("Node %s: set %d simulated nvidia.com/gpu (capacity & allocatable)\n", nodeName, *gpus)
	}
}
