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
	// supportedKVConnectors defines all valid P/D (Prefiller-Decoder) KV connector types
	supportedKVConnectors = map[string]struct{}{
		proxy.KVConnectorNIXLV2:        {},
		proxy.KVConnectorSharedStorage: {},
		proxy.KVConnectorSGLang:        {},
	}

	// supportedECConnectors defines all valid EC (Encoder-Prefiller) connector types
	supportedECConnectors = map[string]struct{}{
		proxy.ECExampleConnector: {},
	}

	// supportedTLSStages defines all valid stages for TLS configuration
	supportedTLSStages = map[string]struct{}{
		prefillStage: {},
		decodeStage:  {},
	}
)

// supportedTLSStagesNames returns a slice of supported TLS stage names
func supportedTLSStagesNames() []string {
	return supportedNames(supportedTLSStages)
}

// supportedKVConnectorsNames returns a slice of supported KV connector names
func supportedKVConnectorsNames() []string {
	return supportedNames(supportedKVConnectors)
}

// supportedECConnectorsNames returns a slice of supported EC connector names
func supportedECConnectorsNames() []string {
	return supportedNames(supportedKVConnectors)
}

// supportedNames returns a slice of supported names from the given map[string]struct{}
func supportedNames(aMap map[string]struct{}) []string {
	names := make([]string, 0, len(aMap))
	for name := range aMap {
		names = append(names, name)
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
	kvConnector := pflag.String("kv-connector", proxy.KVConnectorNIXLV2, "the KV connector between Prefiller and Decoder. Supported: "+strings.Join(supportedKVConnectorsNames(), ", "))
	enableTLS := pflag.StringSlice("enable-tls", []string{}, "stages to enable TLS for. Supported: "+strings.Join(supportedTLSStagesNames(), ", ")+". Can be specified multiple times or as comma-separated values.")
	tlsInsecureSkipVerify := pflag.StringSlice("tls-insecure-skip-verify", []string{}, "stages to skip TLS verification for. Supported: "+strings.Join(supportedTLSStagesNames(), ", ")+". Can be specified multiple times or as comma-separated values.")

	// Deprecated flags - kept for backward compatibility, will be removed in a future release
	deprecatedConnector := pflag.String("connector", "", "The P/D connector being used.")
	prefillerUseTLS := pflag.Bool("prefiller-use-tls", false, "Deprecated: use --enable-tls=prefiller instead. Whether to use TLS when sending requests to prefillers.")
	decoderUseTLS := pflag.Bool("decoder-use-tls", false, "Deprecated: use --enable-tls=decoder instead. Whether to use TLS when sending requests to the decoder.")
	prefillerInsecureSkipVerify := pflag.Bool("prefiller-tls-insecure-skip-verify", false, "Deprecated: use --tls-insecure-skip-verify=prefiller instead. Skip TLS verification for requests to prefiller.")
	decoderInsecureSkipVerify := pflag.Bool("decoder-tls-insecure-skip-verify", false, "Deprecated: use --tls-insecure-skip-verify=decoder instead. Skip TLS verification for requests to decoder.")
	secureProxy := pflag.Bool("secure-proxy", true, "Enables secure proxy. Defaults to true.")
	certPath := pflag.String(
		"cert-path", "", "The path to the certificate for secure proxy. The certificate and private key files "+
			"are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, "+
			"then a self-signed certificate is used (for testing).")
	enableSSRFProtection := pflag.Bool("enable-ssrf-protection", false, "enable SSRF protection using InferencePool allowlisting")
	inferencePool := pflag.String("inference-pool", os.Getenv("INFERENCE_POOL"), "InferencePool in namespace/name or name format (e.g., default/my-pool or my-pool). A single name implies the 'default' namespace. Can also use INFERENCE_POOL env var.")
	// Deprecated: use --inference-pool instead
	deprecatedInferencePoolNamespace := pflag.String("inference-pool-namespace", os.Getenv("INFERENCE_POOL_NAMESPACE"), "DEPRECATED: use --inference-pool instead. The Kubernetes namespace for the InferencePool.")
	// Deprecated: use --inference-pool instead
	deprecatedInferencePoolName := pflag.String("inference-pool-name", os.Getenv("INFERENCE_POOL_NAME"), "DEPRECATED: use --inference-pool instead. The specific InferencePool name.")
	enablePrefillerSampling := pflag.Bool("enable-prefiller-sampling", func() bool { b, _ := strconv.ParseBool(os.Getenv("ENABLE_PREFILLER_SAMPLING")); return b }(), "if true, the target prefill instance will be selected randomly from among the provided prefill host values")
	poolGroup := pflag.String("pool-group", proxy.DefaultPoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")
	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine) // optional to allow zap logging control via CLI

	// Add Go flags to pflag (for zap options compatibility)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)

	// Mark deprecated flags
	pflag.CommandLine.MarkDeprecated("connector", "use --kv-connector instead")

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

	// Migrate deprecated connector flag to new kv-connector flag
	if *deprecatedConnector != "" {
		if *kvConnector == proxy.KVConnectorNIXLV2 {
			// Only override if kvConnector is still at default value
			*kvConnector = *deprecatedConnector
		}
	}

	// Migrate deprecated boolean TLS flags to new StringSlice flags
	if *prefillerUseTLS {
		logger.Info("WARNING: --prefiller-use-tls is deprecated, use --enable-tls=prefiller instead")
		if !containsStage(*enableTLS, prefillStage) {
			*enableTLS = append(*enableTLS, prefillStage)
		}
	}
	if *decoderUseTLS {
		logger.Info("WARNING: --decoder-use-tls is deprecated, use --enable-tls=decoder instead")
		if !containsStage(*enableTLS, decodeStage) {
			*enableTLS = append(*enableTLS, decodeStage)
		}
	}
	if *prefillerInsecureSkipVerify {
		logger.Info("WARNING: --prefiller-tls-insecure-skip-verify is deprecated, use --tls-insecure-skip-verify=prefiller instead")
		if !containsStage(*tlsInsecureSkipVerify, prefillStage) {
			*tlsInsecureSkipVerify = append(*tlsInsecureSkipVerify, prefillStage)
		}
	}
	if *decoderInsecureSkipVerify {
		logger.Info("WARNING: --decoder-tls-insecure-skip-verify is deprecated, use --tls-insecure-skip-verify=decoder instead")
		if !containsStage(*tlsInsecureSkipVerify, decodeStage) {
			*tlsInsecureSkipVerify = append(*tlsInsecureSkipVerify, decodeStage)
		}
	}

	logger.Info("Proxy starting", "Built on", version.BuildRef, "From Git SHA", version.CommitSHA)

	// Validate KV connector
	if _, ok := supportedKVConnectors[*kvConnector]; !ok {
		logger.Info("Error: --kv-connector must be one of: " + strings.Join(supportedKVConnectorsNames(), ", "))
		return
	}
	logger.Info("KV connector (prefiller-decoder) validated", "kvConnector", *kvConnector)

	// Validate TLS stages
	for _, stage := range *enableTLS {
		if _, ok := supportedTLSStages[stage]; !ok {
			logger.Info("Error: --enable-tls stages must be one of: " + strings.Join(supportedTLSStagesNames(), ", "))
			return
		}
	}

	for _, stage := range *tlsInsecureSkipVerify {
		if _, ok := supportedTLSStages[stage]; !ok {
			logger.Info("Error: --tls-insecure-skip-verify stages must be one of: " + strings.Join(supportedTLSStagesNames(), ", "))
			return
		}
	}

	// Parse and validate InferencePool for SSRF protection
	var inferencePoolNamespace, inferencePoolName string
	if *enableSSRFProtection {
		// Prefer --inference-pool over deprecated flags
		switch {
		case *inferencePool != "":
			if *deprecatedInferencePoolName != "" || *deprecatedInferencePoolNamespace != "" {
				logger.Info("Warning: --inference-pool takes precedence over deprecated --inference-pool-name/--inference-pool-namespace flags")
			}
			parts := strings.SplitN(*inferencePool, "/", 2)
			if len(parts) == 1 {
				// Single name implies "default" namespace
				inferencePoolNamespace = "default"
				inferencePoolName = parts[0]
			} else {
				if parts[0] == "" || parts[1] == "" {
					logger.Info("Error: --inference-pool must be in namespace/name or name format (e.g., default/my-pool or my-pool)")
					return
				}
				inferencePoolNamespace = parts[0]
				inferencePoolName = parts[1]
			}
		case *deprecatedInferencePoolName != "":
			// Fall back to deprecated flags
			logger.Info("Warning: using deprecated --inference-pool-name/--inference-pool-namespace flags, please migrate to --inference-pool")
			inferencePoolName = *deprecatedInferencePoolName
			inferencePoolNamespace = *deprecatedInferencePoolNamespace
			if inferencePoolNamespace == "" {
				inferencePoolNamespace = "default"
			}
		default:
			logger.Info("Error: --inference-pool is required when --enable-ssrf-protection is true")
			return
		}

		if inferencePoolName == "" {
			logger.Info("Error: InferencePool name must not be empty")
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
		KVConnector:                 *kvConnector,
		PrefillerUseTLS:             containsStage(*enableTLS, prefillStage),
		PrefillerInsecureSkipVerify: containsStage(*tlsInsecureSkipVerify, prefillStage),
		DecoderInsecureSkipVerify:   containsStage(*tlsInsecureSkipVerify, decodeStage),
		DataParallelSize:            *vLLMDataParallelSize,
		EnablePrefillerSampling:     *enablePrefillerSampling,
		SecureServing:               *secureProxy,
		CertPath:                    *certPath,
	}

	// Create SSRF protection validator
	validator, err := proxy.NewAllowlistValidator(*enableSSRFProtection, *poolGroup, inferencePoolNamespace, inferencePoolName)
	if err != nil {
		logger.Error(err, "failed to create SSRF protection validator")
		return
	}

	proxyServer := proxy.NewProxy(*port, targetURL, config)

	if err := proxyServer.Start(ctx, validator); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
