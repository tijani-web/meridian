/*
cmd/trace-gen is a CLI tool for generating synthetic GPU workload traces
and replaying existing traces for Meridian scheduler/admission benchmarks (§8.1).

Usage:
  # Generate a 100-job trace to JSON
  trace-gen --jobs 100 --seed 42 --out trace.json

  # Generate Kubernetes GPUWorkload YAML manifests
  trace-gen --jobs 100 --seed 42 --format yaml --out workloads.yaml

  # Replay a trace file into GPUWorkload manifests
  trace-gen --replay trace.json --format yaml --out workloads.yaml
*/
package main

import (
	"flag"
	"fmt"
	"os"

	"sigs.k8s.io/yaml"

	"github.com/tijani-web/meridian/pkg/tracegen"
)

func main() {
	jobsCount := flag.Int("jobs", 100, "Number of synthetic jobs to generate")
	seed := flag.Int64("seed", 42, "Fixed pseudo-random seed")
	rate := flag.Float64("rate", 2.0, "Average arrival rate (jobs/sec)")
	namespace := flag.String("namespace", "default", "Kubernetes namespace for workloads")
	outPath := flag.String("out", "", "Output file path (default stdout)")
	format := flag.String("format", "json", "Output format: json or yaml")
	replayPath := flag.String("replay", "", "Path to user-supplied JSON trace to replay")
	flag.Parse()

	var jobs []tracegen.JobSpec
	var err error

	if *replayPath != "" {
		jobs, err = tracegen.LoadTraceFile(*replayPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error loading replay trace: %v\n", err)
			os.Exit(1)
		}
	} else {
		cfg := tracegen.DefaultConfig()
		cfg.JobCount = *jobsCount
		cfg.Seed = *seed
		cfg.ArrivalRate = *rate
		cfg.Namespace = *namespace

		gen := tracegen.NewGenerator(cfg)
		jobs = gen.Generate()
	}

	var outWriter = os.Stdout
	if *outPath != "" {
		f, err := os.Create(*outPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output file %s: %v\n", *outPath, err)
			os.Exit(1)
		}
		defer f.Close()
		outWriter = f
	}

	switch *format {
	case "yaml":
		for i, j := range jobs {
			wl := j.ToGPUWorkload()
			data, err := yaml.Marshal(wl)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Error marshaling workload %s: %v\n", j.Name, err)
				os.Exit(1)
			}
			if i > 0 {
				fmt.Fprintln(outWriter, "---")
			}
			fmt.Fprint(outWriter, string(data))
		}
	case "json":
		if err := tracegen.SaveTrace(outWriter, jobs); err != nil {
			fmt.Fprintf(os.Stderr, "Error writing trace JSON: %v\n", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown format %q (choose json or yaml)\n", *format)
		os.Exit(1)
	}
}
