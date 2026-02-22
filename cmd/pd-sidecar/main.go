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
	// supportedKVConnectors defines all valid KV (Prefiller-Decoder) connector types
	supportedKVConnectors = []string{
		proxy.KVConnectorNIXLV2,
		proxy.KVConnectorSharedStorage,
		proxy.KVConnectorSGLang,
	}

	// supportedECConnectors defines all valid EC (Encoder-Prefiller) connector types
	supportedECConnectors = []string{
		proxy.ECExampleConnector,
	}
)

func main() {
	port := flag.String("port", "8000", "the port the sidecar is listening on")
	vLLMPort := flag.String("vllm-port", "8001", "the port vLLM is listening on")
	vLLMDataParallelSize := flag.Int("data-parallel-size", 1, "the vLLM DATA-PARALLEL-SIZE value")
	kvConnector := flag.String("kv-connector", proxy.KVConnectorNIXLV2, "the KV connector between Prefiller and Decoder. Supported: "+strings.Join(supportedKVConnectors, ", "))
	ecConnector := flag.String("ec-connector", proxy.ECExampleConnector, "the EC connector between Encoder and Prefiller (optional, for EPD mode). Supported: "+strings.Join(supportedECConnectors, ", "))
	prefillerUseTLS := flag.Bool("prefiller-use-tls", false, "whether to use TLS when sending requests to prefillers")
	encoderUseTLS := flag.Bool("encoder-use-tls", false, "whether to use TLS when sending requests to encoders")
	decoderUseTLS := flag.Bool("decoder-use-tls", false, "whether to use TLS when sending requests to the decoder")
	prefillerInsecureSkipVerify := flag.Bool("prefiller-tls-insecure-skip-verify", false, "configures the proxy to skip TLS verification for requests to prefiller")
	encoderInsecureSkipVerify := flag.Bool("encoder-tls-insecure-skip-verify", false, "configures the proxy to skip TLS verification for requests to encoder")
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
	poolGroup := flag.String("pool-group", proxy.DefaultPoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")

	opts := zap.Options{}
	opts.BindFlags(flag.CommandLine) // optional to allow zap logging control via CLI
	flag.Parse()

	logger := zap.New(zap.UseFlagOptions(&opts))
	log.SetLogger(logger)

	ctx := ctrl.SetupSignalHandler()
	log.IntoContext(ctx, logger)

	logger.Info("Proxy starting", "Built on", version.BuildRef, "From Git SHA", version.CommitSHA)

	// Validate KV connector (Prefiller-Decoder)
	isValidConnector := false
	for _, validConnector := range supportedKVConnectors {
		if *kvConnector == validConnector {
			isValidConnector = true
			break
		}
	}
	if !isValidConnector {
		logger.Info("Error: --kv-connector must be one of: " + strings.Join(supportedKVConnectors, ", "))
		return
	}
	logger.Info("KV connector (prefiller-decoder) validated", "kvConnector", kvConnector)

	// Validate EC connector (Encoder-Prefiller) if specified
	if *ecConnector != "" {
		isValidEncoderConnector := false
		for _, validConnector := range supportedECConnectors {
			if *ecConnector == validConnector {
				isValidEncoderConnector = true
				break
			}
		}
		if !isValidEncoderConnector {
			logger.Info("Error: --ec-connector must be one of: " + strings.Join(supportedECConnectors, ", "))
			return
		}
		logger.Info("EC connector (encoder-prefiller) validated", "ecConnector", ecConnector)
	} else {
		logger.Info("EC connector (encoder-prefiller) not specified, encoder stage will be skipped")
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
		KVConnector:                 *kvConnector,
		ECConnector:                 *ecConnector,
		PrefillerUseTLS:             *prefillerUseTLS,
		EncoderUseTLS:               *encoderUseTLS,
		PrefillerInsecureSkipVerify: *prefillerInsecureSkipVerify,
		EncoderInsecureSkipVerify:   *encoderInsecureSkipVerify,
		DecoderInsecureSkipVerify:   *decoderInsecureSkipVerify,
		DataParallelSize:            *vLLMDataParallelSize,
		EnablePrefillerSampling:     *enablePrefillerSampling,
	}

	// Create SSRF protection validator
	validator, err := proxy.NewAllowlistValidator(*enableSSRFProtection, *poolGroup, *inferencePoolNamespace, *inferencePoolName)
	if err != nil {
		logger.Error(err, "failed to create SSRF protection validator")
		return
	}

	proxyServer := proxy.NewProxy(*port, targetURL, config)

	if err := proxyServer.Start(ctx, cert, validator); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
