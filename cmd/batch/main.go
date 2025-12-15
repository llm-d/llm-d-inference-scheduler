package main

import (
	"os"

	batchrunner "github.com/llm-d/llm-d-inference-scheduler/cmd/batch/runner"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
	ctrl "sigs.k8s.io/controller-runtime"
)

func main() {

	if err := batchrunner.NewBatchRunner().WithCustomCollectors(metrics.GetBatchCollectors()...).Run(ctrl.SetupSignalHandler()); err != nil {
		os.Exit(1)
	}
}
