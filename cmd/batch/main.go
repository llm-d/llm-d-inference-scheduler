package main

import (
	"os"

	batchrunner "github.com/llm-d/llm-d-inference-scheduler/cmd/batch/runner"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {

	if err := batchrunner.NewBatchRunner().Run(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}
