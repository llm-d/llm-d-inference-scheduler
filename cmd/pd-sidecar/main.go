/*
Copyright 2025 The llm-d Authors.

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
package main

import (
	"net/url"

	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/version"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

func main() {
	// Initialize options with defaults
	opts := proxy.NewOptions()

	// Add options flags (including logging flags)
	opts.AddFlags(pflag.CommandLine)
	pflag.Parse()

	logger := zap.New(zap.UseFlagOptions(&opts.LoggingOptions))
	log.SetLogger(logger)

	ctx := ctrl.SetupSignalHandler()
	log.IntoContext(ctx, logger)

	// Initialize tracing before creating any spans
	shutdownTracing, err := telemetry.InitTracing(ctx)
	if err != nil {
		// Log error but don't fail - tracing is optional
		logger.Error(err, "Failed to initialize tracing")
	}
	if shutdownTracing != nil {
		defer func() {
			if err := shutdownTracing(ctx); err != nil {
				logger.Error(err, "Failed to shutdown tracing")
			}
		}()
	}

	// Complete options (handles migration from deprecated flags)
	if err := opts.Complete(); err != nil {
		logger.Error(err, "Failed to complete configuration")
		return
	}

	// Validate options
	if err := opts.Validate(); err != nil {
		logger.Error(err, "Invalid configuration")
		return
	}

	logger.Info("Proxy starting", "Built on", version.BuildRef, "From Git SHA", version.CommitSHA)

	// Parse target URL
	targetURL, err := url.Parse(opts.TargetURL)
	if err != nil {
		logger.Error(err, "failed to parse targetURL")
		return
	}

	config := proxy.Config{
		KVConnector:                 opts.KVConnector,
		ECConnector:                 opts.ECConnector,
		PrefillerUseTLS:             opts.UseTLSForPrefiller,
		EncoderUseTLS:               opts.UseTLSForEncoder,
		PrefillerInsecureSkipVerify: opts.InsecureSkipVerifyForPrefiller,
		EncoderInsecureSkipVerify:   opts.InsecureSkipVerifyForEncoder,
		DecoderInsecureSkipVerify:   opts.InsecureSkipVerifyForDecoder,
		DataParallelSize:            opts.DataParallelSize,
		EnablePrefillerSampling:     opts.EnablePrefillerSampling,
		SecureServing:               opts.SecureProxy,
		CertPath:                    opts.CertPath,
	}

	logger.Info("Proxy configuration",
		"port", opts.Port,
		"targetURL", opts.TargetURL,
		"kvConnector", config.KVConnector,
		"ecConnector", config.ECConnector,
		"dataParallelSize", config.DataParallelSize,
		"prefillerUseTLS", config.PrefillerUseTLS,
		"prefillerInsecureSkipVerify", config.PrefillerInsecureSkipVerify,
		"decoderInsecureSkipVerify", config.DecoderInsecureSkipVerify,
		"enablePrefillerSampling", config.EnablePrefillerSampling,
		"secureServing", config.SecureServing,
		"certPath", config.CertPath,
		"enableSSRFProtection", opts.EnableSSRFProtection,
		"inferencePoolNamespace", opts.InferencePoolNamespace,
		"inferencePoolName", opts.InferencePoolName,
		"poolGroup", opts.PoolGroup,
	)

	// Create SSRF protection validator
	validator, err := proxy.NewAllowlistValidator(opts.EnableSSRFProtection, opts.PoolGroup, opts.InferencePoolNamespace, opts.InferencePoolName)
	if err != nil {
		logger.Error(err, "failed to create SSRF protection validator")
		return
	}

	proxyServer := proxy.NewProxy(opts.Port, targetURL, config)

	if err := proxyServer.Start(ctx, validator); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
