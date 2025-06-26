/*
Copyright 2025 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

/**
 * This file is adapted from Gateway API Inference Extension
 * Original source: https://github.com/kubernetes-sigs/gateway-api-inference-extension/blob/main/cmd/epp/main.go
 * Licensed under the Apache License, Version 2.0
 */

// Package main contains the "Endpoint Picker (EPP)" program for scheduling
// inference requests.
package main

import (
	"os"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/gateway-api-inference-extension/cmd/epp/runner"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins"
	"github.com/llm-d/llm-d-kv-cache-manager/pkg/tracing"
)

func main() {
	setupLog := ctrl.Log.WithName("setup")
	ctx := ctrl.SetupSignalHandler()

	// Register GIE plugins
	runner.RegisterAllPlugins()

	// Initialize distributed tracing
	tracingConfig := tracing.NewConfigFromEnv()
	if tracingShutdown, err := tracing.Initialize(ctx, tracingConfig); err != nil {
		setupLog.Error(err, "failed to setup distributed tracing")
		os.Exit(1)
	} else {
		defer func() {
			if err := tracingShutdown(ctx); err != nil {
				setupLog.Error(err, "failed to shutdown tracing")
			}
		}()
		if tracingConfig.Enabled {
			setupLog.Info("tracing enabled", "endpoint", tracingConfig.ExporterEndpoint, "sampling", tracingConfig.SamplingRate)
		} else {
			setupLog.Info("tracing disabled, context propagation enabled")
		}
	}

	// Register llm-d-inference-scheduler plugins
	plugins.RegisterAllPlugins()

	if err := runner.NewRunner().Run(ctx); err != nil {
		setupLog.Error(err, "failed to run llm-d-scheduler")
		os.Exit(1)
	}
}
