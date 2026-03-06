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
	"flag"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/version"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// TLS stages
	prefillStage = "prefiller"
	decodeStage  = "decoder"
)

var (
	// supportedConnectors defines all valid P/D connector types
	supportedConnectors = []string{
		proxy.ConnectorNIXLV2,
		proxy.ConnectorSharedStorage,
		proxy.ConnectorSGLang,
	}

	// supportedTLSStages defines all valid stages for TLS configuration
	supportedTLSStages = map[string]bool{
		prefillStage: true,
		decodeStage:  true,
	}
)

// supportedTLSStagesNames returns a slice of supported TLS stage names
func supportedTLSStagesNames() []string {
	names := make([]string, 0, len(supportedTLSStages))
	for stage := range supportedTLSStages {
		names = append(names, stage)
	}
	return names
}

// containsStage checks if a stage is present in the slice
func containsStage(stages []string, stage string) bool {
	for _, s := range stages {
		if s == stage {
			return true
		}
	}
	return false
}

func main() {
	port := pflag.String("port", "8000", "the port the sidecar is listening on")
	vLLMPort := pflag.String("vllm-port", "8001", "the port vLLM is listening on")
	vLLMDataParallelSize := pflag.Int("data-parallel-size", 1, "the vLLM DATA-PARALLEL-SIZE value")
	connector := pflag.String("connector", proxy.ConnectorNIXLV2, "the P/D connector being used. Supported: "+strings.Join(supportedConnectors, ", "))
	enableTLS := pflag.StringSlice("enable-tls", []string{}, "stages to enable TLS for. Supported: "+strings.Join(supportedTLSStagesNames(), ", ")+". Can be specified multiple times or as comma-separated values.")
	tlsInsecureSkipVerify := pflag.StringSlice("tls-insecure-skip-verify", []string{}, "stages to skip TLS verification for. Supported: "+strings.Join(supportedTLSStagesNames(), ", ")+". Can be specified multiple times or as comma-separated values.")
	secureProxy := pflag.Bool("secure-proxy", true, "Enables secure proxy. Defaults to true.")
	certPath := pflag.String(
		"cert-path", "", "The path to the certificate for secure proxy. The certificate and private key files "+
			"are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, "+
			"then a self-signed certificate is used (for testing).")
	enableSSRFProtection := pflag.Bool("enable-ssrf-protection", false, "enable SSRF protection using InferencePool allowlisting")
	inferencePoolNamespace := pflag.String("inference-pool-namespace", os.Getenv("INFERENCE_POOL_NAMESPACE"), "the Kubernetes namespace to watch for InferencePool resources (defaults to INFERENCE_POOL_NAMESPACE env var)")
	inferencePoolName := pflag.String("inference-pool-name", os.Getenv("INFERENCE_POOL_NAME"), "the specific InferencePool name to watch (defaults to INFERENCE_POOL_NAME env var)")
	enablePrefillerSampling := pflag.Bool("enable-prefiller-sampling", func() bool { b, _ := strconv.ParseBool(os.Getenv("ENABLE_PREFILLER_SAMPLING")); return b }(), "if true, the target prefill instance will be selected randomly from among the provided prefill host values")
	poolGroup := pflag.String("pool-group", proxy.DefaultPoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine) // optional to allow zap logging control via CLI

	// Add Go flags to pflag (for zap options compatibility)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	logger := zap.New(zap.UseFlagOptions(&opts))
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

	logger.Info("Proxy starting", "Built on", version.BuildRef, "From Git SHA", version.CommitSHA)

	// Validate connector
	isValidConnector := false
	for _, validConnector := range supportedConnectors {
		if *connector == validConnector {
			isValidConnector = true
			break
		}
	}
	if !isValidConnector {
		logger.Info("Error: --connector must be one of: " + strings.Join(supportedConnectors, ", "))
		return
	}
	logger.Info("p/d connector validated", "connector", connector)

	// Validate TLS stages
	for _, stage := range *enableTLS {
		if !supportedTLSStages[stage] {
			logger.Info("Error: --enable-tls stages must be one of: " + strings.Join(supportedTLSStagesNames(), ", "))
			return
		}
	}

	for _, stage := range *tlsInsecureSkipVerify {
		if !supportedTLSStages[stage] {
			logger.Info("Error: --tls-insecure-skip-verify stages must be one of: " + strings.Join(supportedTLSStagesNames(), ", "))
			return
		}
	}

	// Determine namespace and pool name for SSRF protection
	if *enableSSRFProtection {
		if *inferencePoolNamespace == "" {
			logger.Info("Error: --inference-pool-namespace or INFERENCE_POOL_NAMESPACE environment variable is required when --enable-ssrf-protection is true")
			return
		}
		if *inferencePoolName == "" {
			logger.Info("Error: --inference-pool-name or INFERENCE_POOL_NAME environment variable is required when --enable-ssrf-protection is true")
			return
		}

		logger.Info("SSRF protection enabled", "namespace", inferencePoolNamespace, "poolName", inferencePoolName)
	}

	// start reverse proxy HTTP server
	scheme := "http"
	if containsStage(*enableTLS, decodeStage) {
		scheme = "https"
	}
	targetURL, err := url.Parse(scheme + "://localhost:" + *vLLMPort)
	if err != nil {
		logger.Error(err, "failed to create targetURL")
		return
	}

	config := proxy.Config{
		Connector:                   *connector,
		PrefillerUseTLS:             containsStage(*enableTLS, prefillStage),
		PrefillerInsecureSkipVerify: containsStage(*tlsInsecureSkipVerify, prefillStage),
		DecoderInsecureSkipVerify:   containsStage(*tlsInsecureSkipVerify, decodeStage),
		DataParallelSize:            *vLLMDataParallelSize,
		EnablePrefillerSampling:     *enablePrefillerSampling,
		SecureServing:               *secureProxy,
		CertPath:                    *certPath,
	}

	// Create SSRF protection validator
	validator, err := proxy.NewAllowlistValidator(*enableSSRFProtection, *poolGroup, *inferencePoolNamespace, *inferencePoolName)
	if err != nil {
		logger.Error(err, "failed to create SSRF protection validator")
		return
	}

	proxyServer := proxy.NewProxy(*port, targetURL, config)

	if err := proxyServer.Start(ctx, validator); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
