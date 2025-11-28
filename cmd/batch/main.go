package main

import (
	"os"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-inference-scheduler/cmd/batch/runner"
)

func main() {

	if err := runner.NewRunner().Run(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}
