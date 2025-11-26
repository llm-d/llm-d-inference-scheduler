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
	"crypto/tls"
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
)

var (
	// supportedConnectors defines all valid P/D connector types
	supportedConnectors = []string{
		proxy.ConnectorNIXLV2,
		proxy.ConnectorLMCache,
		proxy.ConnectorSGLang,
	}
)

func main() {
	port := flag.String("port", "8000", "the port the sidecar is listening on")
	vLLMPort := flag.String("vllm-port", "8001", "the port vLLM is listening on")
	vLLMDataParallelSize := flag.Int("data-parallel-size", 1, "the vLLM DATA-PARALLEL-SIZE value")
	connector := flag.String("connector", proxy.ConnectorNIXLV2, "the P/D connector being used. Supported: "+strings.Join(supportedConnectors, ", "))
	prefillerUseTLS := flag.Bool("prefiller-use-tls", false, "whether to use TLS when sending requests to prefillers")
	decoderUseTLS := flag.Bool("decoder-use-tls", false, "whether to use TLS when sending requests to the decoder")
	prefillerInsecureSkipVerify := flag.Bool("prefiller-tls-insecure-skip-verify", false, "configures the proxy to skip TLS verification for requests to prefiller")
	decoderInsecureSkipVerify := flag.Bool("decoder-tls-insecure-skip-verify", false, "configures the proxy to skip TLS verification for requests to decoder")
	secureProxy := flag.Bool("secure-proxy", true, "Enables secure proxy. Defaults to true.")
	certPath := flag.String(
		"cert-path", "", "The path to the certificate for secure proxy. The certificate and private key files "+
			"are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, "+
			"then a self-signed certificate is used (for testing).")
	enableSSRFProtection := flag.Bool("enable-ssrf-protection", false, "enable SSRF protection using InferencePool allowlisting")
	inferencePoolNamespace := flag.String("inference-pool-namespace", os.Getenv("INFERENCE_POOL_NAMESPACE"), "the Kubernetes namespace to watch for InferencePool resources (defaults to INFERENCE_POOL_NAMESPACE env var)")
	inferencePoolName := flag.String("inference-pool-name", os.Getenv("INFERENCE_POOL_NAME"), "the specific InferencePool name to watch (defaults to INFERENCE_POOL_NAME env var)")
	enablePrefillerSampling := flag.Bool("enable-prefiller-sampling", func() bool { b, _ := strconv.ParseBool(os.Getenv("ENABLE_PREFILLER_SAMPLING")); return b }(), "if true, the target prefill instance will be selected randomly from among the provided prefill host values")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine) // optional to allow zap logging control via CLI
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&opts))
	log.SetLogger(logger)

	ctx := ctrl.SetupSignalHandler()
	log.IntoContext(ctx, logger)

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
	if *decoderUseTLS {
		scheme = "https"
	}
	targetURL, err := url.Parse(scheme + "://localhost:" + *vLLMPort)
	if err != nil {
		logger.Error(err, "failed to create targetURL")
		return
	}

	var cert *tls.Certificate
	if *secureProxy {
		var tempCert tls.Certificate
		if *certPath != "" {
			tempCert, err = tls.LoadX509KeyPair(*certPath+"/tls.crt", *certPath+"/tls.key")
		} else {
			tempCert, err = proxy.CreateSelfSignedTLSCertificate()
		}
		if err != nil {
			logger.Error(err, "failed to create TLS certificate")
			return
		}
		cert = &tempCert
	}

	config := proxy.Config{
		Connector:                   *connector,
		PrefillerUseTLS:             *prefillerUseTLS,
		PrefillerInsecureSkipVerify: *prefillerInsecureSkipVerify,
		DecoderInsecureSkipVerify:   *decoderInsecureSkipVerify,
		DataParallelSize:            *vLLMDataParallelSize,
		EnablePrefillerSampling:     *enablePrefillerSampling,
	}

	// Create SSRF protection validator
	validator, err := proxy.NewAllowlistValidator(*enableSSRFProtection, *inferencePoolNamespace, *inferencePoolName)
	if err != nil {
		logger.Error(err, "failed to create SSRF protection validator")
		return
	}

	proxyServer := proxy.NewProxy(*port, targetURL, config)

	if err := proxyServer.Start(ctx, cert, validator); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
