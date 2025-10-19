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
	"context"
	"flag"
	"net/url"
	"os"

	"k8s.io/klog/v2"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/proxy"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/sidecar/signals"
)

func main() {
	port := flag.String("port", "8000", "the port the sidecar is listening on")
	vLLMPort := flag.String("vllm-port", "8001", "the port vLLM is listening on")
	connector := flag.String("connector", "nixlv2", "the P/D connector being used. Either nixl, nixlv2 or lmcache")
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

	klog.InitFlags(nil)
	flag.Parse()

	// make sure to flush logs before exiting
	defer klog.Flush()

	ctx := signals.SetupSignalHandler(context.Background())
	logger := klog.FromContext(ctx)

	if *connector != proxy.ConnectorNIXLV1 && *connector != proxy.ConnectorNIXLV2 && *connector != proxy.ConnectorLMCache {
		logger.Info("Error: --connector must either be 'nixl', 'nixlv2' or 'lmcache'")
		return
	}
	if *connector == proxy.ConnectorNIXLV1 {
		logger.Info("Warning: nixl connector is deprecated and will be removed in a future release in favor of --connector=nixlv2")
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

	config := proxy.Config{
		Connector:                   *connector,
		PrefillerUseTLS:             *prefillerUseTLS,
		SecureProxy:                 *secureProxy,
		CertPath:                    *certPath,
		PrefillerInsecureSkipVerify: *prefillerInsecureSkipVerify,
		DecoderInsecureSkipVerify:   *decoderInsecureSkipVerify,
		EnableSSRFProtection:        *enableSSRFProtection,
		InferencePoolNamespace:      *inferencePoolNamespace,
		InferencePoolName:           *inferencePoolName,
	}

	proxy, err := proxy.NewProxy(*port, targetURL, config)
	if err != nil {
		logger.Error(err, "Failed to create proxy")
	}
	if err := proxy.Start(ctx); err != nil {
		logger.Error(err, "failed to start proxy server")
	}
}
