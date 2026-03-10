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

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/version"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
	"github.com/stretchr/testify/assert/yaml"
)

var (
	// supportedConnectors defines all valid P/D connector types
	supportedConnectors = []string{
		proxy.ConnectorNIXLV2,
		proxy.ConnectorSharedStorage,
		proxy.ConnectorSGLang,
	}
)

type SidecarConfig struct {
	// the port the sidecar is listening on
	Port int `yaml:"port"`

	// the port vLLM is listening on
	VLLMPort int `yaml:"vllm-port"`

	// the vLLM DATA-PARALLEL-SIZE value
	VLLMDataParallelSize int `yaml:"data-parallel-size"`

	// the P/D connector being used. Supported: "+strings.Join(supportedConnectors, ", ")
	Connector string `yaml:"connector"`

	// whether to use TLS when sending requests to prefillers
	PrefillerUseTLS bool `yaml:"prefiller-use-tls"`

	// whether to use TLS when sending requests to the decoder
	DecoderUseTLS bool `yaml:"decoder-use-tls"`

	// configures the proxy to skip TLS verification for requests to prefiller
	PrefillerInsecureSkipVerify bool `yaml:"prefiller-tls-insecure-skip-verify"`

	// configures the proxy to skip TLS verification for requests to decoder
	DecoderInsecureSkipVerify bool `yaml:"decoder-tls-insecure-skip-verify"`

	// Enables secure proxy. Defaults to true
	SecureProxy bool `yaml:"secure-proxy"`

	// The path to the certificate for secure proxy. The certificate and private key files "+
	// "are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, "+
	// "then a self-signed certificate is used (for testing).
	CertPath string `yaml:"cert-path"`

	// enable SSRF protection using InferencePool allowlisting
	EnableSSRFProtection bool `yaml:"enable-ssrf-protection"`

	// the Kubernetes namespace to watch for InferencePool resources (defaults to INFERENCE_POOL_NAMESPACE env var)
	InferencePoolNamespace string `yaml:"inference-pool-namespace"`

	// the specific InferencePool name to watch (defaults to INFERENCE_POOL_NAME env var)
	InferencePoolName string `yaml:"inference-pool-name"`

	// if true, the target prefill instance will be selected randomly from among the provided prefill host values
	EnablePrefillerSampling bool `yaml:"enable-prefiller-sampling"`

	// group of the InferencePool this Endpoint Picker is associated with}
	PoolGroup string `yaml:"pool-group"`
}

func NewSidecarConfig() *SidecarConfig {
	return &SidecarConfig{
		Port:                        8000,
		VLLMPort:                    8001,
		VLLMDataParallelSize:        1,
		Connector:                   proxy.ConnectorNIXLV2,
		PrefillerUseTLS:             false,
		DecoderUseTLS:               false,
		PrefillerInsecureSkipVerify: false,
		DecoderInsecureSkipVerify:   false,
		SecureProxy:                 true,
		CertPath:                    "",
		EnableSSRFProtection:        false,
		InferencePoolNamespace:      os.Getenv("INFERENCE_POOL_NAMESPACE"),
		InferencePoolName:           os.Getenv("INFERENCE_POOL_NAMESPACE"),
		EnablePrefillerSampling:     func() bool { b, _ := strconv.ParseBool(os.Getenv("ENABLE_PREFILLER_SAMPLING")); return b }(),
		PoolGroup:                   proxy.DefaultPoolGroup,
	}
}

func main() {
	sidecarConfig := NewSidecarConfig()
	configFile := flag.String("config", "", "sidecar config file")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine) // optional to allow zap logging control via CLI
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&opts))
	log.SetLogger(logger)

	ctx := ctrl.SetupSignalHandler()
	log.IntoContext(ctx, logger)

	rawFile, err := os.ReadFile(*configFile)
	if err != nil {
		logger.Error(err, "Failed to read sidecar config file")
	}
	if err := yaml.Unmarshal(rawFile, sidecarConfig); err != nil {
		logger.Error(err, "Failed to unmarshal sidecar config")
	}

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
		if sidecarConfig.Connector == validConnector {
			isValidConnector = true
			break
		}
	}
	if !isValidConnector {
		logger.Info("Error: --connector must be one of: " + strings.Join(supportedConnectors, ", "))
		return
	}
	logger.Info("p/d connector validated", "connector", sidecarConfig.Connector)

	// Determine namespace and pool name for SSRF protection
	if sidecarConfig.EnableSSRFProtection {
		if sidecarConfig.InferencePoolNamespace == "" {
			logger.Info("Error: --inference-pool-namespace or INFERENCE_POOL_NAMESPACE environment variable is required when --enable-ssrf-protection is true")
			return
		}
		if sidecarConfig.InferencePoolName == "" {
			logger.Info("Error: --inference-pool-name or INFERENCE_POOL_NAME environment variable is required when --enable-ssrf-protection is true")
			return
		}

		logger.Info("SSRF protection enabled", "namespace", sidecarConfig.InferencePoolNamespace, "poolName", sidecarConfig.InferencePoolName)
	}

	// start reverse proxy HTTP server
	scheme := "http"
	if sidecarConfig.DecoderUseTLS {
		scheme = "https"
	}
	targetURL, err := url.Parse(scheme + "://localhost:" + strconv.Itoa(sidecarConfig.VLLMPort))
	if err != nil {
		logger.Error(err, "failed to create targetURL")
		return
	}

	config := proxy.Config{
		Connector:                   sidecarConfig.Connector,
		PrefillerUseTLS:             sidecarConfig.PrefillerUseTLS,
		PrefillerInsecureSkipVerify: sidecarConfig.PrefillerInsecureSkipVerify,
		DecoderInsecureSkipVerify:   sidecarConfig.DecoderInsecureSkipVerify,
		DataParallelSize:            sidecarConfig.VLLMDataParallelSize,
		EnablePrefillerSampling:     sidecarConfig.EnablePrefillerSampling,
		SecureServing:               sidecarConfig.SecureProxy,
		CertPath:                    sidecarConfig.CertPath,
	}

	// Create SSRF protection validator
	validator, err := proxy.NewAllowlistValidator(sidecarConfig.EnableSSRFProtection, sidecarConfig.PoolGroup, sidecarConfig.InferencePoolNamespace, sidecarConfig.InferencePoolName)
	if err != nil {
		logger.Error(err, "failed to create SSRF protection validator")
		return
	}

	proxyServer := proxy.NewProxy(strconv.Itoa(sidecarConfig.Port), targetURL, config)

	if err := proxyServer.Start(ctx, validator); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
