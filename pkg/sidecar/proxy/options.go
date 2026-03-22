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
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/pflag"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

// Options holds the CLI-facing configuration for the pd-sidecar proxy.
// It embeds Config which represents the complete processed runtime configuration.
// After Options.Complete(), the embedded Config is fully populated and ready to
// pass directly to NewProxy.
type Options struct {
	// Config holds the processed runtime configuration (populated by Complete()).
	// Fields with direct CLI flags are bound here via embedding; derived fields are set in Complete().
	Config

	// VLLMPort is the port vLLM is listening on; used to compute Config.TargetURL in Complete().
	VLLMPort string
	// EnableTLS is the list of stages to enable TLS for; used to compute Config.UseTLSFor* in Complete().
	EnableTLS []string
	// TLSInsecureSkipVerify is the list of stages to skip TLS verification for; used to compute Config.InsecureSkipVerifyFor* in Complete().
	TLSInsecureSkipVerify []string
	// InferencePool in namespace/name or name format; used to compute Config.InferencePoolNamespace/Name in Complete().
	InferencePool string

	// Deprecated flag fields - kept for backward compatibility; migrated in Complete()
	Connector                   string // Deprecated: use --kv-connector instead
	PrefillerUseTLS             bool   // Deprecated: use --enable-tls=prefiller instead
	DecoderUseTLS               bool   // Deprecated: use --enable-tls=decoder instead
	PrefillerInsecureSkipVerify bool   // Deprecated: use --tls-insecure-skip-verify=prefiller instead
	DecoderInsecureSkipVerify   bool   // Deprecated: use --tls-insecure-skip-verify=decoder instead

	LoggingOptions zap.Options // LoggingOptions holds the zap logging configuration
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

// NewOptions returns a new Options struct initialized with default values.
func NewOptions() *Options {
	enablePrefillerSampling := false
	if val, err := strconv.ParseBool(os.Getenv("ENABLE_PREFILLER_SAMPLING")); err == nil {
		enablePrefillerSampling = val
	}

	return &Options{
		Config: Config{
			Port:                    "8000",
			DataParallelSize:        1,
			SecureServing:           true,
			EnablePrefillerSampling: enablePrefillerSampling,
			PoolGroup:               DefaultPoolGroup,
			InferencePoolNamespace:  os.Getenv("INFERENCE_POOL_NAMESPACE"),
			InferencePoolName:       os.Getenv("INFERENCE_POOL_NAME"),
		},
		VLLMPort:      "8001",
		InferencePool: os.Getenv("INFERENCE_POOL"),
		Connector:     KVConnectorNIXLV2,
	}
}

// AddFlags binds the Options fields to command-line flags on the given FlagSet.
// It also sets up zap logging flags and integrates Go flags with pflag.
func (opts *Options) AddFlags(fs *pflag.FlagSet) {
	// Add logging flags to the standard flag set
	opts.LoggingOptions.BindFlags(flag.CommandLine)

	// Add Go flags to pflag (for zap options compatibility)
	fs.AddGoFlagSet(flag.CommandLine)

	// Direct Config fields - bound via embedded struct
	fs.StringVar(&opts.Port, "port", opts.Port, "the port the sidecar is listening on")
	fs.StringVar(&opts.VLLMPort, "vllm-port", opts.VLLMPort, "the port vLLM is listening on")
	fs.IntVar(&opts.DataParallelSize, "data-parallel-size", opts.DataParallelSize, "the vLLM DATA-PARALLEL-SIZE value")
	fs.StringVar(&opts.KVConnector, "kv-connector", opts.KVConnector,
		"the KV protocol between prefiller and decoder. Supported: "+supportedKVConnectorNamesStr)
	fs.StringVar(&opts.ECConnector, "ec-connector", opts.ECConnector,
		"the EC protocol between encoder and prefiller (for EPD mode). Supported: "+supportedECConnectorNamesStr+". Leave empty to skip encoder stage.")
	fs.BoolVar(&opts.SecureServing, "secure-proxy", opts.SecureServing, "Enables secure proxy. Defaults to true.")
	fs.StringVar(&opts.CertPath, "cert-path", opts.CertPath, "The path to the certificate for secure proxy. The certificate and private key files are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, then a self-signed certificate is used (for testing).")
	fs.BoolVar(&opts.EnableSSRFProtection, "enable-ssrf-protection", opts.EnableSSRFProtection, "enable SSRF protection using InferencePool allowlisting")
	fs.BoolVar(&opts.EnablePrefillerSampling, "enable-prefiller-sampling", opts.EnablePrefillerSampling, "if true, the target prefill instance will be selected randomly from among the provided prefill host values")
	fs.StringVar(&opts.PoolGroup, "pool-group", opts.PoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")

	// Raw flag fields (require transformation in Complete())
	fs.StringSliceVar(&opts.EnableTLS, "enable-tls", opts.EnableTLS, "stages to enable TLS for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringSliceVar(&opts.TLSInsecureSkipVerify, "tls-insecure-skip-verify", opts.TLSInsecureSkipVerify, "stages to skip TLS verification for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringVar(&opts.InferencePool, "inference-pool", opts.InferencePool, "InferencePool in namespace/name or name format (e.g., default/my-pool or my-pool). A single name implies the 'default' namespace. Can also use INFERENCE_POOL env var.")

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

	fs.StringVar(&opts.InferencePoolNamespace, "inference-pool-namespace", opts.InferencePoolNamespace, "Deprecated: use --inference-pool instead. The Kubernetes namespace for the InferencePool (defaults to INFERENCE_POOL_NAMESPACE env var)")
	_ = fs.MarkDeprecated("inference-pool-namespace", "use --inference-pool instead")
	fs.StringVar(&opts.InferencePoolName, "inference-pool-name", opts.InferencePoolName, "Deprecated: use --inference-pool instead. The specific InferencePool name (defaults to INFERENCE_POOL_NAME env var)")
	_ = fs.MarkDeprecated("inference-pool-name", "use --inference-pool instead")
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
// It handles migration from deprecated flags, parses the InferencePool field,
// computes boolean TLS fields, and builds Config.TargetURL.
// After Complete(), opts.Config is fully populated.
func (opts *Options) Complete() error {
	// Migrate deprecated Connector flag to KVConnector
	if opts.Connector != "" && opts.KVConnector == "" {
		opts.KVConnector = opts.Connector
	}

	// Parse InferencePool field (namespace/name or just name), overriding deprecated separate flags
	if opts.InferencePool != "" {
		parts := strings.SplitN(opts.InferencePool, "/", 2)
		if len(parts) == 2 {
			opts.InferencePoolNamespace = parts[0]
			opts.InferencePoolName = parts[1]
		} else {
			opts.InferencePoolNamespace = "default"
			opts.InferencePoolName = parts[0]
		}
	}

	// Migrate deprecated boolean TLS flags into EnableTLS/TLSInsecureSkipVerify slices
	if opts.PrefillerUseTLS && !containsStage(opts.EnableTLS, prefillStage) {
		opts.EnableTLS = append(opts.EnableTLS, prefillStage)
	}
	if opts.DecoderUseTLS && !containsStage(opts.EnableTLS, decodeStage) {
		opts.EnableTLS = append(opts.EnableTLS, decodeStage)
	}
	if opts.PrefillerInsecureSkipVerify && !containsStage(opts.TLSInsecureSkipVerify, prefillStage) {
		opts.TLSInsecureSkipVerify = append(opts.TLSInsecureSkipVerify, prefillStage)
	}
	if opts.DecoderInsecureSkipVerify && !containsStage(opts.TLSInsecureSkipVerify, decodeStage) {
		opts.TLSInsecureSkipVerify = append(opts.TLSInsecureSkipVerify, decodeStage)
	}

	// Compute Config TLS fields from stage slices
	opts.UseTLSForPrefiller = containsStage(opts.EnableTLS, prefillStage)
	opts.UseTLSForEncoder = containsStage(opts.EnableTLS, encodeStage)
	useTLSForDecoder := containsStage(opts.EnableTLS, decodeStage)
	opts.InsecureSkipVerifyForPrefiller = containsStage(opts.TLSInsecureSkipVerify, prefillStage)
	opts.InsecureSkipVerifyForEncoder = containsStage(opts.TLSInsecureSkipVerify, encodeStage)
	opts.InsecureSkipVerifyForDecoder = containsStage(opts.TLSInsecureSkipVerify, decodeStage)

	// Compute Config.TargetURL from VLLMPort and decoder TLS setting
	scheme := "http"
	if useTLSForDecoder {
		scheme = schemeHTTPS
	}
	var err error
	opts.TargetURL, err = url.Parse(scheme + "://localhost:" + opts.VLLMPort)
	if err != nil {
		return fmt.Errorf("failed to parse target URL: %w", err)
	}

	return nil
}

// Validate checks the Options for invalid or conflicting values.
// Complete must be called before Validate.
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
		if strings.Count(opts.InferencePool, "/") > 1 {
			return errors.New("--inference-pool must be in format 'namespace/name' or 'name', not multiple slashes")
		}
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

// containsStage checks if a stage is present in the slice
func containsStage(stages []string, stage string) bool {
	for _, s := range stages {
		if s == stage {
			return true
		}
	}
	return false
}
