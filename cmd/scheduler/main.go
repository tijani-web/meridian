/*
cmd/scheduler is the Meridian out-of-tree GPU-aware Kubernetes scheduler binary.

This binary uses a separate Go module (cmd/scheduler/go.mod) to isolate the
k8s.io/kubernetes dependency tree (with its monorepo staging replace directives)
from the main controller-manager module.

Pods opt in by setting: spec.schedulerName: meridian-scheduler

Build:
  cd cmd/scheduler && go mod tidy && GOOS=linux go build -o ../../bin/meridian-scheduler .

Run on kind:
  kubectl apply -f ../../deploy/scheduler/deployment.yaml
*/
package main

import (
	"os"

	"k8s.io/component-base/cli"
	"k8s.io/kubernetes/cmd/kube-scheduler/app"

	"github.com/tijani-web/meridian/scheduler/plugin"
)

func main() {
	command := app.NewSchedulerCommand(
		app.WithPlugin(plugin.Name, plugin.New),
	)
	code := cli.Run(command)
	os.Exit(code)
}
