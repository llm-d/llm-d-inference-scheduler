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
	"slices"
	"strconv"
	"strings"

	"github.com/go-logr/logr"
	"github.com/spf13/pflag"
	uberzap "go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
)

// Options holds the CLI-facing configuration for the pd-sidecar proxy.
// It embeds Config which represents the complete processed runtime configuration.
// After Options.Complete(), the embedded Config is fully populated and ready to
// pass directly to NewProxy.
type Options struct {
	// Config holds the processed runtime configuration (populated by Complete()).
	// Fields with direct CLI flags are bound here via embedding; derived fields are set in Complete().
	Config

	// vllmPort is the port vLLM is listening on; used to compute Config.DecoderURL in Complete().
	vllmPort string
	// enableTLS is the list of stages to enable TLS for; used to compute Config.UseTLSFor* in Complete().
	enableTLS []string
	// tlsInsecureSkipVerify is the list of stages to skip TLS verification for; used to compute Config.InsecureSkipVerifyFor* in Complete().
	tlsInsecureSkipVerify []string
	// inferencePool in namespace/name or name format; used to compute Config.InferencePoolNamespace/Name in Complete().
	inferencePool string

	// Deprecated flag fields - kept for backward compatibility; migrated in Complete()
	connector                   string // Deprecated: use --kv-connector instead
	prefillerUseTLS             bool   // Deprecated: use --enable-tls=prefiller instead
	decoderUseTLS               bool   // Deprecated: use --enable-tls=decoder instead
	prefillerInsecureSkipVerify bool   // Deprecated: use --tls-insecure-skip-verify=prefiller instead
	decoderInsecureSkipVerify   bool   // Deprecated: use --tls-insecure-skip-verify=decoder instead

	loggingOptions zap.Options // loggingOptions holds the zap logging configuration
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
		vllmPort:      "8001",
		inferencePool: os.Getenv("INFERENCE_POOL"),
		connector:     KVConnectorNIXLV2,
	}
}

// AddFlags binds the Options fields to command-line flags on the given FlagSet.
// It also sets up zap logging flags and integrates Go flags with pflag.
func (opts *Options) AddFlags(fs *pflag.FlagSet) {
	// Add logging flags to the standard flag set
	opts.loggingOptions.BindFlags(flag.CommandLine)

	// Add Go flags to pflag (for zap options compatibility)
	fs.AddGoFlagSet(flag.CommandLine)

	fs.StringVar(&opts.Port, "port", opts.Port, "the port the sidecar is listening on")
	fs.StringVar(&opts.vllmPort, "vllm-port", opts.vllmPort, "the port vLLM is listening on")
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

	fs.StringSliceVar(&opts.enableTLS, "enable-tls", opts.enableTLS, "stages to enable TLS for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringSliceVar(&opts.tlsInsecureSkipVerify, "tls-insecure-skip-verify", opts.tlsInsecureSkipVerify, "stages to skip TLS verification for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringVar(&opts.inferencePool, "inference-pool", opts.inferencePool, "InferencePool in namespace/name or name format (e.g., default/my-pool or my-pool). A single name implies the 'default' namespace. Can also use INFERENCE_POOL env var.")

	// Deprecated flags - kept for backward compatibility
	fs.StringVar(&opts.connector, "connector", opts.connector, "Deprecated: use --kv-connector instead. The P/D connector being used. Supported: "+supportedKVConnectorNamesStr)
	_ = fs.MarkDeprecated("connector", "use --kv-connector instead")

	fs.BoolVar(&opts.prefillerUseTLS, "prefiller-use-tls", opts.prefillerUseTLS, "Deprecated: use --enable-tls=prefiller instead. Whether to use TLS when sending requests to prefillers.")
	_ = fs.MarkDeprecated("prefiller-use-tls", "use --enable-tls=prefiller instead")
	fs.BoolVar(&opts.decoderUseTLS, "decoder-use-tls", opts.decoderUseTLS, "Deprecated: use --enable-tls=decoder instead. Whether to use TLS when sending requests to the decoder.")
	_ = fs.MarkDeprecated("decoder-use-tls", "use --enable-tls=decoder instead")
	fs.BoolVar(&opts.prefillerInsecureSkipVerify, "prefiller-tls-insecure-skip-verify", opts.prefillerInsecureSkipVerify, "Deprecated: use --tls-insecure-skip-verify=prefiller instead. Skip TLS verification for requests to prefiller.")
	_ = fs.MarkDeprecated("prefiller-tls-insecure-skip-verify", "use --tls-insecure-skip-verify=prefiller instead")
	fs.BoolVar(&opts.decoderInsecureSkipVerify, "decoder-tls-insecure-skip-verify", opts.decoderInsecureSkipVerify, "Deprecated: use --tls-insecure-skip-verify=decoder instead. Skip TLS verification for requests to decoder.")
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
// computes boolean TLS fields, and builds Config.DecoderURL.
// After Complete(), opts.Config is fully populated.
func (opts *Options) Complete() error {
	// Migrate deprecated connector flag to KVConnector
	if opts.connector != "" && opts.KVConnector == "" {
		opts.KVConnector = opts.connector
	}

	// Parse inferencePool field (namespace/name or just name), overriding deprecated separate flags
	if opts.inferencePool != "" {
		parts := strings.SplitN(opts.inferencePool, "/", 2)
		if len(parts) == 2 {
			opts.InferencePoolNamespace = parts[0]
			opts.InferencePoolName = parts[1]
		} else {
			opts.InferencePoolNamespace = "default"
			opts.InferencePoolName = parts[0]
		}
	}

	// Migrate deprecated boolean TLS flags into enableTLS/tlsInsecureSkipVerify slices
	if opts.prefillerUseTLS && !slices.Contains(opts.enableTLS, prefillStage) {
		opts.enableTLS = append(opts.enableTLS, prefillStage)
	}
	if opts.decoderUseTLS && !slices.Contains(opts.enableTLS, decodeStage) {
		opts.enableTLS = append(opts.enableTLS, decodeStage)
	}
	if opts.prefillerInsecureSkipVerify && !slices.Contains(opts.tlsInsecureSkipVerify, prefillStage) {
		opts.tlsInsecureSkipVerify = append(opts.tlsInsecureSkipVerify, prefillStage)
	}
	if opts.decoderInsecureSkipVerify && !slices.Contains(opts.tlsInsecureSkipVerify, decodeStage) {
		opts.tlsInsecureSkipVerify = append(opts.tlsInsecureSkipVerify, decodeStage)
	}

	// Compute Config TLS fields from stage slices
	opts.UseTLSForPrefiller = slices.Contains(opts.enableTLS, prefillStage)
	opts.UseTLSForDecoder = slices.Contains(opts.enableTLS, decodeStage)
	opts.UseTLSForEncoder = slices.Contains(opts.enableTLS, encodeStage)
	opts.InsecureSkipVerifyForPrefiller = slices.Contains(opts.tlsInsecureSkipVerify, prefillStage)
	opts.InsecureSkipVerifyForEncoder = slices.Contains(opts.tlsInsecureSkipVerify, encodeStage)
	opts.InsecureSkipVerifyForDecoder = slices.Contains(opts.tlsInsecureSkipVerify, decodeStage)

	// Compute Config.DecoderURL from vllmPort and decoder TLS setting
	scheme := "http"
	if opts.UseTLSForDecoder {
		scheme = schemeHTTPS
	}
	var err error
	opts.DecoderURL, err = url.Parse(scheme + "://localhost:" + opts.vllmPort)
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
	if opts.connector != "" && opts.connector != opts.KVConnector {
		if _, ok := supportedKVConnectors[opts.connector]; !ok {
			return fmt.Errorf("--connector must be one of: %s", supportedKVConnectorNamesStr)
		}
	}

	// Validate TLS stages
	if err := validateStages(opts.enableTLS, supportedTLSStages, "--enable-tls"); err != nil {
		return err
	}
	if err := validateStages(opts.tlsInsecureSkipVerify, supportedTLSStages, "--tls-insecure-skip-verify"); err != nil {
		return err
	}

	// Validate inferencePool format if provided
	if opts.inferencePool != "" {
		if strings.Count(opts.inferencePool, "/") > 1 {
			return errors.New("--inference-pool must be in format 'namespace/name' or 'name', not multiple slashes")
		}
		parts := strings.Split(opts.inferencePool, "/")
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

// customLevelEncoder maps negative Zap levels to human-readable names that
// match the project's verbosity constants (VERBOSE=3, DEBUG=4, TRACE=5).
// Without this, controller-runtime's zap bridge emits all V(n) calls as
// "debug" in JSON output, which is misleading for V(1)–V(3) (verbose info).
func customLevelEncoder(l zapcore.Level, enc zapcore.PrimitiveArrayEncoder) {
	if l >= 0 {
		zapcore.LowercaseLevelEncoder(l, enc)
		return
	}
	switch l {
	case zapcore.Level(-1 * logutil.DEBUG): // V(4) → "debug"
		enc.AppendString("debug")
	case zapcore.Level(-1 * logutil.TRACE): // V(5) → "trace"
		enc.AppendString("trace")
	default:
		if l >= zapcore.Level(-1*logutil.VERBOSE) { // V(1)–V(3) → "info"
			enc.AppendString("info")
		} else { // V(6+) → "trace"
			enc.AppendString("trace")
		}
	}
}

// NewLogger returns a logger configured from the Options logging flags,
// with a custom level encoder that maps verbosity levels to their semantic
// names instead of always rendering V(n) as "debug".
func (opts *Options) NewLogger() logr.Logger {
	config := uberzap.NewProductionEncoderConfig()
	config.EncodeLevel = customLevelEncoder
	return zap.New(
		zap.UseFlagOptions(&opts.loggingOptions),
		zap.Encoder(zapcore.NewJSONEncoder(config)),
	)
}
