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
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/scheduling/pd"
)

func main() {
	setupLog := ctrl.Log.WithName("setup")
	ctx := ctrl.SetupSignalHandler()

	pdConfig := config.LoadConfig(setupLog)

	// always initialize prefix scorer, which is used in the decision making of whether PD should be called or not.
	prefixConfig := scorer.DefaultPrefixStoreConfig()
	prefixConfig.CacheBlockSize = pdConfig.PrefixCacheBlockSize
	prefixConfig.CacheCapacity = pdConfig.PrefixCacheCapacity
	prefixScorer := scorer.NewPrefixAwareScorer(ctx, prefixConfig)

	requestControlConfig := requestcontrol.NewConfig()
	if pdConfig.PDEnabled { // if PD is enabled, use the prefill header pre-request plugin to populate prefill endpoint in a header.
		requestControlConfig.WithPreRequestPlugins(prerequest.NewPrefillHeaderHandler())
	}
	// if PD is enabled we always use prefix scorer (even if not configured on Prefill/Decode scheduling profiles)
	// if PD is disabled, only decode profile runs. if prefix is configured in decode use its post response extension point.
	if _, exist := pdConfig.DecodeSchedulerPlugins[config.PrefixScorerName]; exist || pdConfig.PDEnabled {
		requestControlConfig.WithPostResponsePlugins(prefixScorer)
	}

	schedulerConfig, err := pd.CreatePDSchedulerConfig(ctx, pdConfig, prefixScorer)
	if err != nil {
		setupLog.Error(err, "failed to create scheduler config")
		os.Exit(1)
	}

	if err := runner.NewRunner().
		WithRequestControlConfig(requestControlConfig).
		WithSchedulerConfig(schedulerConfig).
		Run(ctx); err != nil {
		setupLog.Error(err, "failed to run llm-d-scheduler")
		os.Exit(1)
	}
}
