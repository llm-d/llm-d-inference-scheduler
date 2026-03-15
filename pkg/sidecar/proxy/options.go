/*
Copyright 2026 The llm-d Authors.

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
	Port             string // Port is the port the sidecar is listening on
	VLLMPort         string // VLLMPort is the port vLLM is listening on
	TargetURL        string // TargetURL is the target URL for the proxy
	DataParallelSize int    // DataParallelSize is the vLLM DATA-PARALLEL-SIZE value
	// KVConnector is the KV protocol between Prefiller and Decoder
	KVConnector string
	// ECConnector is the EC protocol between Encoder and Prefiller (for EPD mode)
	ECConnector string
	// Deprecated: Use KVConnector instead. Connector is the P/D connector being used
	Connector                      string
	EnableTLS                      []string // EnableTLS stages to enable TLS for (new StringSlice flag)
	TLSInsecureSkipVerify          []string // TLSInsecureSkipVerify stages to skip TLS verification for (new StringSlice flag)
	UseTLSForPrefiller             bool     // UseTLSForPrefiller indicates whether to use TLS when sending requests to prefillers (set from EnableTLS)
	UseTLSForEncoder               bool     // UseTLSForEncoder indicates whether to use TLS when sending requests to encoders (set from EnableTLS)
	UseTLSForDecoder               bool     // UseTLSForDecoder indicates whether to use TLS when sending requests to the decoder (set from EnableTLS)
	InsecureSkipVerifyForPrefiller bool     // InsecureSkipVerifyForPrefiller configures the proxy to skip TLS verification for requests to prefiller (set from TLSInsecureSkipVerify)
	InsecureSkipVerifyForEncoder   bool     // InsecureSkipVerifyForEncoder configures the proxy to skip TLS verification for requests to encoder (set from TLSInsecureSkipVerify)
	InsecureSkipVerifyForDecoder   bool     // InsecureSkipVerifyForDecoder configures the proxy to skip TLS verification for requests to decoder (set from TLSInsecureSkipVerify)

	// Deprecated flag fields (kept for backward compatibility)
	PrefillerUseTLS             bool   // Deprecated: Use EnableTLS instead. PrefillerUseTLS indicates whether to use TLS when sending requests to prefillers
	DecoderUseTLS               bool   // Deprecated: Use EnableTLS instead. DecoderUseTLS indicates whether to use TLS when sending requests to the decoder
	PrefillerInsecureSkipVerify bool   // Deprecated: Use TLSInsecureSkipVerify instead. PrefillerInsecureSkipVerify configures the proxy to skip TLS verification for requests to prefiller
	DecoderInsecureSkipVerify   bool   // Deprecated: Use TLSInsecureSkipVerify instead. DecoderInsecureSkipVerify configures the proxy to skip TLS verification for requests to decoder
	SecureProxy                 bool   // SecureProxy enables secure proxy
	CertPath                    string // CertPath is the path to the certificate for secure proxy
	EnableSSRFProtection        bool   // EnableSSRFProtection enables SSRF protection using InferencePool allowlisting
	InferencePool               string // InferencePool in namespace/name or name format (e.g., default/my-pool or my-pool). A single name implies the 'default' namespace.

	// Deprecated flag fields for InferencePool (kept for backward compatibility)
	InferencePoolNamespace  string      // Deprecated: Use InferencePool instead. InferencePoolNamespace is the Kubernetes namespace to watch for InferencePool resources
	InferencePoolName       string      // Deprecated: Use InferencePool instead. InferencePoolName is the specific InferencePool name to watch
	EnablePrefillerSampling bool        // EnablePrefillerSampling enables random selection of prefill instances
	PoolGroup               string      // PoolGroup is the group of the InferencePool this Endpoint Picker is associated with
	LoggingOptions          zap.Options // LoggingOptions holds the zap logging configuration
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

	supportedKVConnectorNamesStr = strings.Join([]string{KVConnectorNIXLV2, KVConnectorSharedStorage, KVConnectorSGLang}, ", ")
	supportedECConnectorNamesStr = strings.Join([]string{ECExampleConnector}, ", ")
	supportedTLSStageNamesStr    = strings.Join([]string{prefillStage, decodeStage, encodeStage}, ", ")
)

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
	if val, err := strconv.ParseBool(os.Getenv("ENABLE_PREFILLER_SAMPLING")); err == nil {
		enablePrefillerSampling = val
	}

	return &Options{
		Port:                    "8000",
		VLLMPort:                "8001",
		DataParallelSize:        1,
		KVConnector:             "",
		ECConnector:             "",
		Connector:               KVConnectorNIXLV2,
		SecureProxy:             true,
		InferencePool:           os.Getenv("INFERENCE_POOL"),
		InferencePoolNamespace:  os.Getenv("INFERENCE_POOL_NAMESPACE"),
		InferencePoolName:       os.Getenv("INFERENCE_POOL_NAME"),
		EnablePrefillerSampling: enablePrefillerSampling,
		PoolGroup:               DefaultPoolGroup,
	}
}

// AddFlags binds the Options fields to command-line flags on the given FlagSet.
// It also sets up zap logging flags and integrates Go flags with pflag.
func (opts *Options) AddFlags(fs *pflag.FlagSet) {
	// Add logging flags to the standard flag set
	opts.LoggingOptions.BindFlags(flag.CommandLine)

	// Add Go flags to pflag (for zap options compatibility)
	fs.AddGoFlagSet(flag.CommandLine)

	fs.StringVar(&opts.Port, "port", opts.Port, "the port the sidecar is listening on")
	fs.StringVar(&opts.VLLMPort, "vllm-port", opts.VLLMPort, "the port vLLM is listening on")
	fs.IntVar(&opts.DataParallelSize, "data-parallel-size", opts.DataParallelSize, "the vLLM DATA-PARALLEL-SIZE value")

	fs.StringVar(&opts.KVConnector, "kv-connector", opts.KVConnector,
		"the KV protocol between Prefiller and Decoder. Supported: "+supportedKVConnectorNamesStr)

	fs.StringVar(&opts.ECConnector, "ec-connector", opts.ECConnector,
		"the EC protocol between Encoder and Prefiller (for EPD mode). Supported: "+supportedECConnectorNamesStr+". Leave empty to skip encoder stage.")

	fs.StringSliceVar(&opts.EnableTLS, "enable-tls", opts.EnableTLS, "stages to enable TLS for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringSliceVar(&opts.TLSInsecureSkipVerify, "tls-insecure-skip-verify", opts.TLSInsecureSkipVerify, "stages to skip TLS verification for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")

	// Deprecated flags - kept for backward compatibility
	fs.StringVar(&opts.Connector, "connector", opts.Connector, "Deprecated: use --kv-connector instead. The P/D connector being used. Supported: "+supportedKVConnectorNamesStr)
	_ = fs.MarkDeprecated("connector", "use --kv-connector instead")

	fs.BoolVar(&opts.PrefillerUseTLS, "prefiller-use-tls", opts.PrefillerUseTLS, "Deprecated: use --enable-tls=prefiller instead. Whether to use TLS when sending requests to prefillers.")
	_ = fs.MarkDeprecated("prefiller-use-tls", "use --enable-tls=prefiller instead")
	fs.BoolVar(&opts.DecoderUseTLS, "decoder-use-tls", opts.DecoderUseTLS, "Deprecated: use --enable-tls=decoder instead. Whether to use TLS when sending requests to the decoder.")
	_ = fs.MarkDeprecated("decoder-use-tls", "use --enable-tls=decoder instead")
	fs.BoolVar(&opts.PrefillerInsecureSkipVerify, "prefiller-tls-insecure-skip-verify", opts.PrefillerInsecureSkipVerify, "Deprecated: use --tls-insecure-skip-verify=prefiller instead. Skip TLS verification for requests to prefiller.")
	_ = fs.MarkDeprecated("prefiller-tls-insecure-skip-verify", "use --tls-insecure-skip-verify=prefiller instead")
	fs.BoolVar(&opts.DecoderInsecureSkipVerify, "decoder-tls-insecure-skip-verify", opts.DecoderInsecureSkipVerify, "Deprecated: use --tls-insecure-skip-verify=decoder instead. Skip TLS verification for requests to decoder.")
	_ = fs.MarkDeprecated("decoder-tls-insecure-skip-verify", "use --tls-insecure-skip-verify=decoder instead")
	fs.BoolVar(&opts.SecureProxy, "secure-proxy", opts.SecureProxy, "Enables secure proxy. Defaults to true.")
	fs.StringVar(&opts.CertPath, "cert-path", opts.CertPath, "The path to the certificate for secure proxy. The certificate and private key files are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, then a self-signed certificate is used (for testing).")
	fs.BoolVar(&opts.EnableSSRFProtection, "enable-ssrf-protection", opts.EnableSSRFProtection, "enable SSRF protection using InferencePool allowlisting")
	fs.StringVar(&opts.InferencePool, "inference-pool", opts.InferencePool, "InferencePool in namespace/name or name format (e.g., default/my-pool or my-pool). A single name implies the 'default' namespace. Can also use INFERENCE_POOL env var.")

	// Deprecated flags - kept for backward compatibility
	fs.StringVar(&opts.InferencePoolNamespace, "inference-pool-namespace", opts.InferencePoolNamespace, "Deprecated: use --inference-pool instead. The Kubernetes namespace for the InferencePool (defaults to INFERENCE_POOL_NAMESPACE env var)")
	_ = fs.MarkDeprecated("inference-pool-namespace", "use --inference-pool instead")
	fs.StringVar(&opts.InferencePoolName, "inference-pool-name", opts.InferencePoolName, "Deprecated: use --inference-pool instead. The specific InferencePool name (defaults to INFERENCE_POOL_NAME env var)")
	_ = fs.MarkDeprecated("inference-pool-name", "use --inference-pool instead")
	fs.BoolVar(&opts.EnablePrefillerSampling, "enable-prefiller-sampling", opts.EnablePrefillerSampling, "if true, the target prefill instance will be selected randomly from among the provided prefill host values")
	fs.StringVar(&opts.PoolGroup, "pool-group", opts.PoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")
}

// validateStages checks if all stages in the slice are valid according to the supportedStages map
func validateStages(stages []string, supportedStages map[string]struct{}, flagName string) error {
	for _, stage := range stages {
		if _, ok := supportedStages[stage]; !ok {
			return fmt.Errorf("%s stages must be one of: %s", flagName, supportedTLSStageNamesStr)
		}
	}
	return nil
}

// Complete performs post-processing of parsed command-line arguments.
// This handles migration from deprecated boolean flags to new StringSlice flags,
// parses the InferencePool field, sets configuration fields from flag fields, and computes the target URL.
func (opts *Options) Complete() error {
	// Migrate deprecated Connector flag to KVConnector
	if opts.Connector != "" && opts.KVConnector == "" {
		opts.KVConnector = opts.Connector
	}

	// Parse InferencePool field (namespace/name or just name)
	if opts.InferencePool != "" {
		parts := strings.SplitN(opts.InferencePool, "/", 2)
		if len(parts) == 2 {
			// Format: namespace/name
			opts.InferencePoolNamespace = parts[0]
			opts.InferencePoolName = parts[1]
		} else {
			// Format: name (implies default namespace)
			opts.InferencePoolNamespace = "default"
			opts.InferencePoolName = parts[0]
		}
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

	// Set configuration fields from flag fields
	opts.UseTLSForPrefiller = containsStage(opts.EnableTLS, prefillStage)
	opts.UseTLSForEncoder = containsStage(opts.EnableTLS, encodeStage)
	opts.UseTLSForDecoder = containsStage(opts.EnableTLS, decodeStage)
	opts.InsecureSkipVerifyForPrefiller = containsStage(opts.TLSInsecureSkipVerify, prefillStage)
	opts.InsecureSkipVerifyForEncoder = containsStage(opts.TLSInsecureSkipVerify, encodeStage)
	opts.InsecureSkipVerifyForDecoder = containsStage(opts.TLSInsecureSkipVerify, decodeStage)

	// Compute target URL based on decoder TLS settings and VLLM port
	scheme := "http"
	if opts.UseTLSForDecoder {
		scheme = schemeHTTPS
	}
	opts.TargetURL = scheme + "://localhost:" + opts.VLLMPort

	return nil
}

// Validate checks the Options for invalid or conflicting values.
func (opts *Options) Validate() error {
	// Validate KV connector
	if _, ok := supportedKVConnectors[opts.KVConnector]; !ok {
		return fmt.Errorf("--kv-connector must be one of: %s", supportedKVConnectorNamesStr)
	}

	// Validate EC connector if provided
	if opts.ECConnector != "" {
		if _, ok := supportedECConnectors[opts.ECConnector]; !ok {
			return fmt.Errorf("--ec-connector must be one of: %s", supportedECConnectorNamesStr)
		}
	}

	// Validate deprecated connector flag
	if opts.Connector != "" && opts.Connector != opts.KVConnector {
		if _, ok := supportedKVConnectors[opts.Connector]; !ok {
			return fmt.Errorf("--connector must be one of: %s", supportedKVConnectorNamesStr)
		}
	}

	// Validate TLS stages
	if err := validateStages(opts.EnableTLS, supportedTLSStages, "--enable-tls"); err != nil {
		return err
	}

	if err := validateStages(opts.TLSInsecureSkipVerify, supportedTLSStages, "--tls-insecure-skip-verify"); err != nil {
		return err
	}

	// Validate InferencePool format if provided
	if opts.InferencePool != "" {
		// Check for invalid characters (only allow alphanumeric, hyphen, and forward slash)
		if strings.Count(opts.InferencePool, "/") > 1 {
			return errors.New("--inference-pool must be in format 'namespace/name' or 'name', not multiple slashes")
		}
		// Validate that it doesn't contain invalid characters like spaces or special chars
		parts := strings.Split(opts.InferencePool, "/")
		for _, part := range parts {
			if part == "" {
				return errors.New("--inference-pool cannot have empty namespace or name")
			}
		}
	}

	// Validate SSRF protection requirements
	if opts.EnableSSRFProtection {
		if opts.InferencePoolNamespace == "" {
			return errors.New("--inference-pool, --inference-pool-namespace, INFERENCE_POOL, or INFERENCE_POOL_NAMESPACE environment variable is required when --enable-ssrf-protection is true")
		}
		if opts.InferencePoolName == "" {
			return errors.New("--inference-pool, --inference-pool-name, INFERENCE_POOL, or INFERENCE_POOL_NAME environment variable is required when --enable-ssrf-protection is true")
		}
	}

	return nil
}
