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

package proxy

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// Options holds all configuration options for the pd-sidecar proxy.
type Options struct {
	// Port is the port the sidecar is listening on
	Port string

	// VLLMPort is the port vLLM is listening on
	VLLMPort string

	// DataParallelSize is the vLLM DATA-PARALLEL-SIZE value
	DataParallelSize int

	// KVConnector is the KV protocol between Prefiller and Decoder
	KVConnector string

	// ECConnector is the EC protocol between Encoder and Prefiller (for EPD mode)
	ECConnector string

	// Deprecated: Use KVConnector instead. Connector is the P/D connector being used
	Connector string

	// EnableTLS stages to enable TLS for (new StringSlice flag)
	EnableTLS []string

	// TLSInsecureSkipVerify stages to skip TLS verification for (new StringSlice flag)
	TLSInsecureSkipVerify []string

	// Deprecated: Use EnableTLS instead. PrefillerUseTLS indicates whether to use TLS when sending requests to prefillers
	PrefillerUseTLS bool

	// Deprecated: Use EnableTLS instead. DecoderUseTLS indicates whether to use TLS when sending requests to the decoder
	DecoderUseTLS bool

	// Deprecated: Use TLSInsecureSkipVerify instead. PrefillerInsecureSkipVerify configures the proxy to skip TLS verification for requests to prefiller
	PrefillerInsecureSkipVerify bool

	// Deprecated: Use TLSInsecureSkipVerify instead. DecoderInsecureSkipVerify configures the proxy to skip TLS verification for requests to decoder
	DecoderInsecureSkipVerify bool

	// SecureProxy enables secure proxy
	SecureProxy bool

	// CertPath is the path to the certificate for secure proxy
	CertPath string

	// EnableSSRFProtection enables SSRF protection using InferencePool allowlisting
	EnableSSRFProtection bool

	// InferencePoolNamespace is the Kubernetes namespace to watch for InferencePool resources
	InferencePoolNamespace string

	// InferencePoolName is the specific InferencePool name to watch
	InferencePoolName string

	// EnablePrefillerSampling enables random selection of prefill instances
	EnablePrefillerSampling bool

	// PoolGroup is the group of the InferencePool this Endpoint Picker is associated with
	PoolGroup string

	// LoggingOptions holds the zap logging configuration
	LoggingOptions zap.Options
}

const (
	// TLS stages
	prefillStage = "prefiller"
	decodeStage  = "decoder"
	encodeStage  = "encoder"
)

var (
	// supportedKVConnectors defines all valid P/D KV connector types
	supportedKVConnectors = map[string]struct{}{
		KVConnectorNIXLV2:        {},
		KVConnectorSharedStorage: {},
		KVConnectorSGLang:        {},
	}

	// supportedECConnectors defines all valid E/P EC connector types
	supportedECConnectors = map[string]struct{}{
		ECExampleConnector: {},
	}

	// supportedTLSStages defines all valid stages for TLS configuration
	supportedTLSStages = map[string]struct{}{
		prefillStage: {},
		decodeStage:  {},
		encodeStage:  {},
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
	return supportedNames(supportedECConnectors)
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

// NewOptions returns a new Options struct initialized with default values.
func NewOptions() *Options {
	// Get default value for EnablePrefillerSampling from environment
	enablePrefillerSampling := false
	if val := os.Getenv("ENABLE_PREFILLER_SAMPLING"); val != "" {
		enablePrefillerSampling, _ = strconv.ParseBool(val)
	}

	return &Options{
		Port:                        "8000",
		VLLMPort:                    "8001",
		DataParallelSize:            1,
		KVConnector:                 KVConnectorNIXLV2,
		ECConnector:                 "",
		Connector:                   KVConnectorNIXLV2,
		EnableTLS:                   []string{},
		TLSInsecureSkipVerify:       []string{},
		PrefillerUseTLS:             false,
		DecoderUseTLS:               false,
		PrefillerInsecureSkipVerify: false,
		DecoderInsecureSkipVerify:   false,
		SecureProxy:                 true,
		CertPath:                    "",
		EnableSSRFProtection:        false,
		InferencePoolNamespace:      os.Getenv("INFERENCE_POOL_NAMESPACE"),
		InferencePoolName:           os.Getenv("INFERENCE_POOL_NAME"),
		EnablePrefillerSampling:     enablePrefillerSampling,
		PoolGroup:                   DefaultPoolGroup,
		LoggingOptions:              zap.Options{},
	}
}

// AddFlags binds the Options fields to command-line flags on the given FlagSet.
// It also binds logging flags to the standard flag.CommandLine FlagSet.
func (opts *Options) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&opts.Port, "port", opts.Port,
		"the port the sidecar is listening on")

	fs.StringVar(&opts.VLLMPort, "vllm-port", opts.VLLMPort,
		"the port vLLM is listening on")

	fs.IntVar(&opts.DataParallelSize, "data-parallel-size", opts.DataParallelSize,
		"the vLLM DATA-PARALLEL-SIZE value")

	fs.StringVar(&opts.KVConnector, "kv-connector", opts.KVConnector,
		"the KV protocol between Prefiller and Decoder. Supported: "+strings.Join(supportedKVConnectorsNames(), ", "))

	fs.StringVar(&opts.ECConnector, "ec-connector", opts.ECConnector,
		"the EC protocol between Encoder and Prefiller (for EPD mode). Supported: "+strings.Join(supportedECConnectorsNames(), ", ")+". Leave empty to skip encoder stage.")

	fs.StringVar(&opts.Connector, "connector", opts.Connector,
		"Deprecated: use --kv-connector instead. The P/D connector being used. Supported: "+strings.Join(supportedKVConnectorsNames(), ", "))

	fs.StringSliceVar(&opts.EnableTLS, "enable-tls", opts.EnableTLS,
		"stages to enable TLS for. Supported: "+strings.Join(supportedTLSStagesNames(), ", ")+". Can be specified multiple times or as comma-separated values.")

	fs.StringSliceVar(&opts.TLSInsecureSkipVerify, "tls-insecure-skip-verify", opts.TLSInsecureSkipVerify,
		"stages to skip TLS verification for. Supported: "+strings.Join(supportedTLSStagesNames(), ", ")+". Can be specified multiple times or as comma-separated values.")

	// Deprecated flags - kept for backward compatibility
	fs.BoolVar(&opts.PrefillerUseTLS, "prefiller-use-tls", opts.PrefillerUseTLS,
		"Deprecated: use --enable-tls=prefiller instead. Whether to use TLS when sending requests to prefillers.")

	fs.BoolVar(&opts.DecoderUseTLS, "decoder-use-tls", opts.DecoderUseTLS,
		"Deprecated: use --enable-tls=decoder instead. Whether to use TLS when sending requests to the decoder.")

	fs.BoolVar(&opts.PrefillerInsecureSkipVerify, "prefiller-tls-insecure-skip-verify", opts.PrefillerInsecureSkipVerify,
		"Deprecated: use --tls-insecure-skip-verify=prefiller instead. Skip TLS verification for requests to prefiller.")

	fs.BoolVar(&opts.DecoderInsecureSkipVerify, "decoder-tls-insecure-skip-verify", opts.DecoderInsecureSkipVerify,
		"Deprecated: use --tls-insecure-skip-verify=decoder instead. Skip TLS verification for requests to decoder.")

	fs.BoolVar(&opts.SecureProxy, "secure-proxy", opts.SecureProxy,
		"Enables secure proxy. Defaults to true.")

	fs.StringVar(&opts.CertPath, "cert-path", opts.CertPath,
		"The path to the certificate for secure proxy. The certificate and private key files "+
			"are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, "+
			"then a self-signed certificate is used (for testing).")

	fs.BoolVar(&opts.EnableSSRFProtection, "enable-ssrf-protection", opts.EnableSSRFProtection,
		"enable SSRF protection using InferencePool allowlisting")

	fs.StringVar(&opts.InferencePoolNamespace, "inference-pool-namespace", opts.InferencePoolNamespace,
		"the Kubernetes namespace to watch for InferencePool resources (defaults to INFERENCE_POOL_NAMESPACE env var)")

	fs.StringVar(&opts.InferencePoolName, "inference-pool-name", opts.InferencePoolName,
		"the specific InferencePool name to watch (defaults to INFERENCE_POOL_NAME env var)")

	fs.BoolVar(&opts.EnablePrefillerSampling, "enable-prefiller-sampling", opts.EnablePrefillerSampling,
		"if true, the target prefill instance will be selected randomly from among the provided prefill host values")

	fs.StringVar(&opts.PoolGroup, "pool-group", opts.PoolGroup,
		"group of the InferencePool this Endpoint Picker is associated with.")

	// Add logging flags to the standard flag set
	opts.LoggingOptions.BindFlags(flag.CommandLine)
}

// Validate checks the Options for invalid or conflicting values.
func (opts *Options) Validate() error {
	// Validate KV connector
	if _, ok := supportedKVConnectors[opts.KVConnector]; !ok {
		return fmt.Errorf("--kv-connector must be one of: %s", strings.Join(supportedKVConnectorsNames(), ", "))
	}

	// Validate EC connector if provided
	if opts.ECConnector != "" {
		if _, ok := supportedECConnectors[opts.ECConnector]; !ok {
			return fmt.Errorf("--ec-connector must be one of: %s", strings.Join(supportedECConnectorsNames(), ", "))
		}
	}

	// Validate deprecated connector flag
	if opts.Connector != "" && opts.Connector != opts.KVConnector {
		if _, ok := supportedKVConnectors[opts.Connector]; !ok {
			return fmt.Errorf("--connector must be one of: %s", strings.Join(supportedKVConnectorsNames(), ", "))
		}
	}

	// Validate TLS stages
	for _, stage := range opts.EnableTLS {
		if _, ok := supportedTLSStages[stage]; !ok {
			return fmt.Errorf("--enable-tls stages must be one of: %s", strings.Join(supportedTLSStagesNames(), ", "))
		}
	}

	for _, stage := range opts.TLSInsecureSkipVerify {
		if _, ok := supportedTLSStages[stage]; !ok {
			return fmt.Errorf("--tls-insecure-skip-verify stages must be one of: %s", strings.Join(supportedTLSStagesNames(), ", "))
		}
	}

	// Validate SSRF protection requirements
	if opts.EnableSSRFProtection {
		if opts.InferencePoolNamespace == "" {
			return errors.New("--inference-pool-namespace or INFERENCE_POOL_NAMESPACE environment variable is required when --enable-ssrf-protection is true")
		}
		if opts.InferencePoolName == "" {
			return errors.New("--inference-pool-name or INFERENCE_POOL_NAME environment variable is required when --enable-ssrf-protection is true")
		}
	}

	return nil
}

// Complete performs post-processing of parsed command-line arguments.
// This handles migration from deprecated boolean flags to new StringSlice flags.
func (opts *Options) Complete() error {
	// Migrate deprecated Connector flag to KVConnector
	if opts.Connector != "" && opts.KVConnector == KVConnectorNIXLV2 {
		opts.KVConnector = opts.Connector
	}

	// Migrate deprecated boolean TLS flags to new StringSlice flags
	if opts.PrefillerUseTLS {
		if !containsStage(opts.EnableTLS, prefillStage) {
			opts.EnableTLS = append(opts.EnableTLS, prefillStage)
		}
	}
	if opts.DecoderUseTLS {
		if !containsStage(opts.EnableTLS, decodeStage) {
			opts.EnableTLS = append(opts.EnableTLS, decodeStage)
		}
	}
	if opts.PrefillerInsecureSkipVerify {
		if !containsStage(opts.TLSInsecureSkipVerify, prefillStage) {
			opts.TLSInsecureSkipVerify = append(opts.TLSInsecureSkipVerify, prefillStage)
		}
	}
	if opts.DecoderInsecureSkipVerify {
		if !containsStage(opts.TLSInsecureSkipVerify, decodeStage) {
			opts.TLSInsecureSkipVerify = append(opts.TLSInsecureSkipVerify, decodeStage)
		}
	}

	return nil
}

// GetPrefillerUseTLS returns whether TLS should be used for prefiller based on EnableTLS
func (opts *Options) GetPrefillerUseTLS() bool {
	return containsStage(opts.EnableTLS, prefillStage)
}

// GetDecoderUseTLS returns whether TLS should be used for decoder based on EnableTLS
func (opts *Options) GetDecoderUseTLS() bool {
	return containsStage(opts.EnableTLS, decodeStage)
}

// GetEncoderUseTLS returns whether TLS should be used for encoder based on EnableTLS
func (opts *Options) GetEncoderUseTLS() bool {
	return containsStage(opts.EnableTLS, encodeStage)
}

// GetPrefillerInsecureSkipVerify returns whether to skip TLS verification for prefiller
func (opts *Options) GetPrefillerInsecureSkipVerify() bool {
	return containsStage(opts.TLSInsecureSkipVerify, prefillStage)
}

// GetDecoderInsecureSkipVerify returns whether to skip TLS verification for decoder
func (opts *Options) GetDecoderInsecureSkipVerify() bool {
	return containsStage(opts.TLSInsecureSkipVerify, decodeStage)
}

// GetEncoderInsecureSkipVerify returns whether to skip TLS verification for encoder
func (opts *Options) GetEncoderInsecureSkipVerify() bool {
	return containsStage(opts.TLSInsecureSkipVerify, encodeStage)
}
