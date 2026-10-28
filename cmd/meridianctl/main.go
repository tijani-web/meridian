/*
cmd/meridianctl is the Meridian GPU fleet control plane CLI.

Subcommands:
  meridianctl explain workload  <name> -n <namespace>
  meridianctl explain inference <name> -n <namespace>
  meridianctl status cluster

All output is produced from real live cluster data via controller-runtime client.
*/
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/explain"
)

var scheme = runtime.NewScheme()

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(meridianv1alpha1.AddToScheme(scheme))
}

func main() {
	root := &cobra.Command{
		Use:   "meridianctl",
		Short: "Meridian GPU fleet control plane CLI",
		Long: `meridianctl provides real-time diagnostic visibility into Meridian GPU workloads,
inference deployments, and cluster resource state.

Examples:
  meridianctl explain workload  my-training-job -n research
  meridianctl explain inference llama-serve     -n production
  meridianctl status cluster`,
	}

	root.AddCommand(buildExplainCmd())
	root.AddCommand(buildStatusCmd())

	if err := root.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func newClient() (client.Client, error) {
	cfg, err := config.GetConfig()
	if err != nil {
		return nil, fmt.Errorf("failed to load kubeconfig: %w", err)
	}
	c, err := client.New(cfg, client.Options{Scheme: scheme})
	if err != nil {
		return nil, fmt.Errorf("failed to create client: %w", err)
	}
	return c, nil
}

// buildExplainCmd builds the `meridianctl explain` command tree.
func buildExplainCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "explain",
		Short: "Explain a Meridian resource",
	}
	cmd.AddCommand(buildExplainWorkloadCmd())
	cmd.AddCommand(buildExplainInferenceCmd())
	return cmd
}

// buildExplainWorkloadCmd builds `meridianctl explain workload <name> -n <ns>`.
func buildExplainWorkloadCmd() *cobra.Command {
	var namespace string

	cmd := &cobra.Command{
		Use:   "workload <name>",
		Short: "Diagnose a GPUWorkload (phase, queue, scheduling, permit, history)",
		Long: `Explain reports the complete diagnostic state of a GPUWorkload in strict order:
  1. Current phase and duration
  2. Queue quota usage, position, and blocking workloads
  3. Scheduling gate status
  4. Per-node filter rejection summary and top scoring candidates
  5. Gang permit reservation vs. minimum required (and timeout)
  6. Ordered preemption and recovery history`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c, err := newClient()
			if err != nil {
				return err
			}
			exp := explain.NewExplainer(c)
			result, err := exp.ExplainWorkload(cmd.Context(), namespace, name)
			if err != nil {
				return fmt.Errorf("explain workload: %w", err)
			}
			fmt.Print(explain.FormatWorkload(result))
			return nil
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the GPUWorkload")
	return cmd
}

// buildExplainInferenceCmd builds `meridianctl explain inference <name> -n <ns>`.
func buildExplainInferenceCmd() *cobra.Command {
	var namespace string

	cmd := &cobra.Command{
		Use:   "inference <name>",
		Short: "Diagnose an InferenceDeployment (replicas, canary, routing endpoints)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			c, err := newClient()
			if err != nil {
				return err
			}
			exp := explain.NewExplainer(c)
			result, err := exp.ExplainInference(cmd.Context(), namespace, name)
			if err != nil {
				return fmt.Errorf("explain inference: %w", err)
			}
			fmt.Print(explain.FormatInference(result))
			return nil
		},
	}

	cmd.Flags().StringVarP(&namespace, "namespace", "n", "default", "Namespace of the InferenceDeployment")
	return cmd
}

// buildStatusCmd builds `meridianctl status cluster`.
func buildStatusCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show cluster-wide GPU resource status",
	}

	clusterCmd := &cobra.Command{
		Use:   "cluster",
		Short: "Show cluster GPU utilization, fragmentation, and queue summary",
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := newClient()
			if err != nil {
				return err
			}
			exp := explain.NewExplainer(c)
			result, err := exp.ExplainCluster(cmd.Context())
			if err != nil {
				return fmt.Errorf("status cluster: %w", err)
			}
			fmt.Print(explain.FormatCluster(result))
			return nil
		},
	}

	cmd.AddCommand(clusterCmd)
	return cmd
}
