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
	"gopkg.in/yaml.v3"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
)

const (
	// Flags
	port                    = "port"
	vllmPort                = "vllm-port"
	dataParallelSize        = "data-parallel-size"
	kvConnector             = "kv-connector"
	ecConnector             = "ec-connector"
	enableSSRFProtection    = "enable-ssrf-protection"
	enablePrefillerSampling = "enable-prefiller-sampling"
	enableTLS               = "enable-tls"
	tlsInsecureSkipVerify   = "tls-insecure-skip-verify"
	secureServing           = "secure-proxy"
	certPath                = "cert-path"
	poolGroup               = "pool-group"
	configuration           = "configuration"
	configurationFile       = "configuration-file"
	inferencePool           = "inference-pool"

	// Deprecated flags
	connector                      = "connector"
	prefillerUseTLS                = "prefiller-use-tls"
	decoderUseTLS                  = "decoder-use-tls"
	prefillerTLSInsecureSkipVerify = "prefiller-tls-insecure-skip-verify"
	decoderTLSInsecureSkipVerify   = "decoder-tls-insecure-skip-verify"
	inferencePoolNamespace         = "inference-pool-namespace"
	inferencePoolName              = "inference-pool-name"

	// Environment variables
	envInferencePool           = "INFERENCE_POOL"
	envInferencePoolNamespace  = "INFERENCE_POOL_NAMESPACE"
	envInferencePoolName       = "INFERENCE_POOL_NAME"
	envEnablePrefillerSampling = "ENABLE_PREFILLER_SAMPLING"

	// Defaults
	defaultPort             = "8000"
	defaultVLLMPort         = "8001"
	defaultDataParallelSize = 1

	// TLS stages
	prefillStage = "prefiller"
	decodeStage  = "decoder"
	encodeStage  = "encoder"
)

type configurationMap map[string]any

type configurationState struct {
	FromEnv    []string
	FromFlags  []string
	FromInline []string
	FromFile   []string
	Defaults   []string
}

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

	loggingOptions                       zap.Options // loggingOptions holds the zap logging configuration
	FlagSet                              *pflag.FlagSet
	configurationFromInlineSpecification string
	configurationFromFile                string

	// ConfigurationState lists configuration values from flags, environment variables, inline specification, file and default values
	ConfigurationState configurationState
}

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
	if val, err := strconv.ParseBool(os.Getenv(envEnablePrefillerSampling)); err == nil {
		enablePrefillerSampling = val
	}

	return &Options{
		Config: Config{
			Port:                    defaultPort,
			DataParallelSize:        defaultDataParallelSize,
			SecureServing:           true,
			EnablePrefillerSampling: enablePrefillerSampling,
			PoolGroup:               DefaultPoolGroup,
			InferencePoolNamespace:  os.Getenv(envInferencePoolNamespace),
			InferencePoolName:       os.Getenv(envInferencePoolName),
		},
		vllmPort:      defaultVLLMPort,
		inferencePool: os.Getenv(envInferencePool),
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

	fs.StringVar(&opts.Port, port, opts.Port, "the port the sidecar is listening on")
	fs.StringVar(&opts.vllmPort, vllmPort, opts.vllmPort, "the port vLLM is listening on")
	fs.IntVar(&opts.DataParallelSize, dataParallelSize, opts.DataParallelSize, "the vLLM DATA-PARALLEL-SIZE value")
	fs.StringVar(&opts.KVConnector, kvConnector, opts.KVConnector,
		"the KV protocol between prefiller and decoder. Supported: "+supportedKVConnectorNamesStr)
	fs.StringVar(&opts.ECConnector, ecConnector, opts.ECConnector,
		"the EC protocol between encoder and prefiller (for EPD mode). Supported: "+supportedECConnectorNamesStr+". Leave empty to skip encoder stage.")
	fs.BoolVar(&opts.SecureServing, secureServing, opts.SecureServing, "Enables secure proxy. Defaults to true.")
	fs.StringVar(&opts.CertPath, certPath, opts.CertPath, "The path to the certificate for secure proxy. The certificate and private key files are assumed to be named tls.crt and tls.key, respectively. If not set, and secureProxy is enabled, then a self-signed certificate is used (for testing).")
	fs.BoolVar(&opts.EnableSSRFProtection, enableSSRFProtection, opts.EnableSSRFProtection, "enable SSRF protection using InferencePool allowlisting")
	fs.BoolVar(&opts.EnablePrefillerSampling, enablePrefillerSampling, opts.EnablePrefillerSampling, "if true, the target prefill instance will be selected randomly from among the provided prefill host values")
	fs.StringVar(&opts.PoolGroup, poolGroup, opts.PoolGroup, "group of the InferencePool this Endpoint Picker is associated with.")

	fs.StringSliceVar(&opts.enableTLS, enableTLS, opts.enableTLS, "stages to enable TLS for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringSliceVar(&opts.tlsInsecureSkipVerify, tlsInsecureSkipVerify, opts.tlsInsecureSkipVerify, "stages to skip TLS verification for. Supported: "+supportedTLSStageNamesStr+". Can be specified multiple times or as comma-separated values.")
	fs.StringVar(&opts.inferencePool, inferencePool, opts.inferencePool, "InferencePool in namespace/name or name format (e.g., default/my-pool or my-pool). A single name implies the 'default' namespace. Can also use INFERENCE_POOL env var.")

	// Deprecated flags - kept for backward compatibility
	fs.StringVar(&opts.connector, connector, opts.connector, "Deprecated: use --kv-connector instead. The P/D connector being used. Supported: "+supportedKVConnectorNamesStr)
	_ = fs.MarkDeprecated(connector, "use --kv-connector instead")

	fs.BoolVar(&opts.prefillerUseTLS, prefillerUseTLS, opts.prefillerUseTLS, "Deprecated: use --enable-tls=prefiller instead. Whether to use TLS when sending requests to prefillers.")
	_ = fs.MarkDeprecated(prefillerUseTLS, "use --enable-tls=prefiller instead")
	fs.BoolVar(&opts.decoderUseTLS, "decoder-use-tls", opts.decoderUseTLS, "Deprecated: use --enable-tls=decoder instead. Whether to use TLS when sending requests to the decoder.")
	_ = fs.MarkDeprecated(decoderUseTLS, "use --enable-tls=decoder instead")
	fs.BoolVar(&opts.prefillerInsecureSkipVerify, prefillerTLSInsecureSkipVerify, opts.prefillerInsecureSkipVerify, "Deprecated: use --tls-insecure-skip-verify=prefiller instead. Skip TLS verification for requests to prefiller.")
	_ = fs.MarkDeprecated(prefillerTLSInsecureSkipVerify, "use --tls-insecure-skip-verify=prefiller instead")
	fs.BoolVar(&opts.decoderInsecureSkipVerify, decoderTLSInsecureSkipVerify, opts.decoderInsecureSkipVerify, "Deprecated: use --tls-insecure-skip-verify=decoder instead. Skip TLS verification for requests to decoder.")
	_ = fs.MarkDeprecated(decoderTLSInsecureSkipVerify, "use --tls-insecure-skip-verify=decoder instead")

	fs.StringVar(&opts.InferencePoolNamespace, inferencePoolNamespace, opts.InferencePoolNamespace, "Deprecated: use --inference-pool instead. The Kubernetes namespace for the InferencePool (defaults to INFERENCE_POOL_NAMESPACE env var)")
	_ = fs.MarkDeprecated(inferencePoolNamespace, "use --inference-pool instead")
	fs.StringVar(&opts.InferencePoolName, inferencePoolName, opts.InferencePoolName, "Deprecated: use --inference-pool instead. The specific InferencePool name (defaults to INFERENCE_POOL_NAME env var)")
	_ = fs.MarkDeprecated(inferencePoolName, "use --inference-pool instead")
	fs.StringVar(&opts.configurationFromInlineSpecification, configuration, "", "Sidecar configuration in YAML provided as inline specification. Example `--configuration={port: 8085, vllm-port: 8203}`")
	fs.StringVar(&opts.configurationFromFile, configurationFile, "", "Path to file which contains sidecar configuration in YAML. Example `--configuration-file=/etc/config/sidecar-config.yaml`")
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
	if err := opts.extractYAMLConfiguration(); err != nil {
		return err
	}

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

// GetConfigurationState determines if configuration was obtained from:
// 1. Default values
// 2. Environment variables
// 3. Individual flags
func (opts *Options) GetConfigurationState() {
	opts.getDefaults()
	opts.getEnvVars()
	opts.getFlags()
}

// getDefaults updates `ConfigurationState` with configuration keys which contain default values
func (opts *Options) getDefaults() {
	opts.ConfigurationState.Defaults = append(opts.ConfigurationState.Defaults,
		port,
		vllmPort,
		dataParallelSize,
		kvConnector,
		ecConnector,
		enableSSRFProtection,
		enableTLS,
		tlsInsecureSkipVerify,
		secureServing,
		certPath,
		poolGroup,
	)
}

// getEnvVars updates `ConfigurationState` with configuration keys which contain values provided through environment variables
func (opts *Options) getEnvVars() {
	envVarMap := map[string]string{
		envInferencePool:           inferencePool,
		envInferencePoolNamespace:  inferencePoolNamespace,
		envInferencePoolName:       inferencePoolName,
		envEnablePrefillerSampling: enablePrefillerSampling,
	}
	for key, value := range envVarMap {
		if os.Getenv(key) != "" {
			appendItem(&opts.ConfigurationState.FromEnv, value)
		}
	}
}

// getFlags updates `ConfigurationState` with configuration keys which contain values provided through flags
func (opts *Options) getFlags() {
	fromEnv := []struct {
		envKey   string
		envValue any
	}{
		{inferencePool, opts.inferencePool},
		{inferencePoolNamespace, opts.InferencePoolNamespace},
		{inferencePoolName, opts.InferencePoolName},
		{enablePrefillerSampling, opts.EnablePrefillerSampling},
	}
	for _, check := range fromEnv {
		if check.envValue != nil && opts.isParsed(check.envKey) {
			appendItem(&opts.ConfigurationState.FromFlags, check.envKey)
			removeItems(&opts.ConfigurationState.FromEnv, []string{check.envKey})
		}
	}
	if len(opts.ConfigurationState.Defaults) != 0 {
		for i := 0; i < len(opts.ConfigurationState.Defaults); i++ {
			if opts.isParsed(opts.ConfigurationState.Defaults[i]) {
				appendItem(&opts.ConfigurationState.FromFlags, opts.ConfigurationState.Defaults[i])
				removeItems(&opts.ConfigurationState.Defaults, []string{opts.ConfigurationState.Defaults[i]})
				i--
			}
		}
	}
}

// extractYAMLConfiguration extracts sidecar configuration (if provided)
// from `--configuration` and `--configuration-file` parameters
func (opts *Options) extractYAMLConfiguration() error {
	var inlineConfiguration, fileConfiguration configurationMap
	var err error
	if opts.configurationFromInlineSpecification != "" {
		inlineConfiguration, err = opts.YAMLConfigurationFromInlineSpecification()
		if err != nil {
			return err
		}
	}
	if opts.configurationFromFile != "" {
		fileConfiguration, err = opts.YAMLConfigurationFromFile()
		if err != nil {
			return err
		}
	}
	switch {
	case inlineConfiguration != nil && fileConfiguration != nil:
		if len(inlineConfiguration) != 0 && len(fileConfiguration) != 0 {
			result1 := opts.mergeYAMLConfigurations(fileConfiguration, inlineConfiguration)
			err = opts.updateSidecarConfiguration(result1)
			if err != nil {
				return err
			}
			for key := range inlineConfiguration {
				appendItem(&opts.ConfigurationState.FromInline, strings.Split(key, ":")[0])
			}
			for key := range fileConfiguration {
				appendItem(&opts.ConfigurationState.FromFile, key)
			}
			removeDuplicates(&opts.ConfigurationState.FromFile, opts.ConfigurationState.FromInline)
			removeDuplicates(&opts.ConfigurationState.FromFile, opts.ConfigurationState.FromFlags)
			removeDuplicates(&opts.ConfigurationState.FromInline, opts.ConfigurationState.FromFlags)
			removeDuplicates(&opts.ConfigurationState.Defaults, opts.ConfigurationState.FromInline)
			removeDuplicates(&opts.ConfigurationState.FromInline, opts.ConfigurationState.FromEnv)

		}
	case inlineConfiguration != nil && fileConfiguration == nil:
		if len(inlineConfiguration) != 0 {
			err = opts.updateSidecarConfiguration(inlineConfiguration)
			if err != nil {
				return err
			}
			for key := range inlineConfiguration {
				appendItem(&opts.ConfigurationState.FromInline, key)
			}
			removeDuplicates(&opts.ConfigurationState.FromInline, opts.ConfigurationState.FromFlags)
			removeDuplicates(&opts.ConfigurationState.Defaults, opts.ConfigurationState.FromInline)
			removeDuplicates(&opts.ConfigurationState.FromInline, opts.ConfigurationState.FromEnv)
		}
	case inlineConfiguration == nil && fileConfiguration != nil:
		if len(fileConfiguration) != 0 {
			err = opts.updateSidecarConfiguration(fileConfiguration)
			if err != nil {
				return err
			}
			for key := range fileConfiguration {
				appendItem(&opts.ConfigurationState.FromFile, key)
			}
			removeDuplicates(&opts.ConfigurationState.FromFile, opts.ConfigurationState.FromFlags)
			removeDuplicates(&opts.ConfigurationState.Defaults, opts.ConfigurationState.FromFile)
			removeDuplicates(&opts.ConfigurationState.FromFile, opts.ConfigurationState.FromEnv)
		}
	}
	return nil
}

// YAMLConfigurationFromInlineSpecification extracts YAML configuration provided as inline specification
// "--configuration={port: 8085, vllm-port: 8203}"
func (opts *Options) YAMLConfigurationFromInlineSpecification() (map[string]any, error) {
	var temp map[string]any
	if err := yaml.Unmarshal([]byte(opts.configurationFromInlineSpecification), &temp); err != nil {
		return nil, errors.New("failed to unmarshal sidecar configuration")
	}
	for key := range temp {
		if strings.Contains(key, ":") {
			data := strings.Split(key, ":")
			temp[data[0]] = data[1]
			delete(temp, key)
		}
	}
	return temp, nil
}

// YAMLConfigurationFromFile extracts YAML configuration from file path
// "--configuration-file=/etc/config/sidecar-config.yaml"
func (opts *Options) YAMLConfigurationFromFile() (map[string]any, error) {
	var temp map[string]any
	rawFile, err := os.ReadFile(opts.configurationFromFile)
	if err != nil {
		return nil, fmt.Errorf("failed to read sidecar configuration: %w", err)
	}
	if err := yaml.Unmarshal(rawFile, &temp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal sidecar configuration: %w", err)

	}
	return temp, nil
}

// mergeYAMLConfigurations merges following:
// 1. YAML configuration from file path `--configuration-file`
// 2. YAML configuration provided as inline specification `--configuration“,
// and gives higher priority to configuration provided in inline specification `--configuration`
func (opts *Options) mergeYAMLConfigurations(fileYAML, parameterYAML map[string]any) map[string]any {
	for fileKey := range fileYAML {
		appendItem(&opts.ConfigurationState.FromFile, fileKey)
	}
	for parameterKey, parameterValue := range parameterYAML {
		appendItem(&opts.ConfigurationState.FromInline, parameterKey)
		if fileYAMLValue, ok := fileYAML[parameterKey]; ok {
			fileYAMLMap, fileYAMLOk := fileYAMLValue.(map[string]any)
			parameterYAMLMap, parameterYAMLOk := parameterValue.(map[string]any)
			if fileYAMLOk && parameterYAMLOk {
				fileYAML[parameterKey] = opts.mergeYAMLConfigurations(fileYAMLMap, parameterYAMLMap)
				continue
			}
		}
		fileYAML[parameterKey] = parameterValue
	}
	removeDuplicates(&opts.ConfigurationState.FromFile, opts.ConfigurationState.FromInline)
	return fileYAML
}

// flagConfiguration contains
// 1. flag name
// 2. function to update value when flag contains default value
type flagConfiguration struct {
	flag   string
	update func(opts *Options, value any) error
}

var flagConfigurationList = []flagConfiguration{
	{
		flag: port,
		update: func(opts *Options, value any) error {
			v, err := extractInt(value)
			if err != nil {
				return err
			}
			opts.Port = strconv.Itoa(v)
			return nil
		},
	},
	{
		flag: vllmPort,
		update: func(opts *Options, value any) error {
			v, err := extractInt(value)
			if err != nil {
				return err
			}
			opts.vllmPort = strconv.Itoa(v)
			return nil
		},
	},
	{
		flag: dataParallelSize,
		update: func(opts *Options, value any) error {
			v, err := extractInt(value)
			if err != nil {
				return err
			}
			opts.DataParallelSize = v
			return nil
		},
	},
	{
		flag: kvConnector,
		update: func(opts *Options, value any) error {
			v, err := extractString(value)
			if err != nil {
				return err
			}
			opts.KVConnector = v
			return nil
		},
	},
	{
		flag: ecConnector,
		update: func(opts *Options, value any) error {
			v, err := extractString(value)
			if err != nil {
				return err
			}
			opts.ECConnector = v
			return nil
		},
	},
	{
		flag: connector,
		update: func(opts *Options, value any) error {
			v, err := extractString(value)
			if err != nil {
				return err
			}
			opts.connector = v
			return nil
		},
	},
	{
		flag: enableSSRFProtection,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.EnableSSRFProtection = v
			return nil
		},
	},
	{
		flag: enablePrefillerSampling,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.EnablePrefillerSampling = v
			return nil
		},
	},
	{
		flag: enableTLS,
		update: func(opts *Options, v any) error {
			switch t := v.(type) {
			case string:
				opts.enableTLS = strings.Split(t, ",")
			case []any:
				temp := make([]string, 0, 10)
				for _, val := range t {
					temp = append(temp, fmt.Sprintf("%v", val))
				}
				opts.enableTLS = temp
			default:
				return fmt.Errorf("invalid type %T", v)
			}
			return nil
		},
	},
	{
		flag: prefillerUseTLS,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.prefillerUseTLS = v
			return nil
		},
	},
	{
		flag: decoderUseTLS,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.decoderUseTLS = v
			return nil
		},
	},
	{
		flag: tlsInsecureSkipVerify,
		update: func(opts *Options, value any) error {
			v, err := extractSliceString(value)
			if err != nil {
				return err
			}
			opts.tlsInsecureSkipVerify = v
			return nil
		},
	},
	{
		flag: prefillerTLSInsecureSkipVerify,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.prefillerInsecureSkipVerify = v
			return nil
		},
	},
	{
		flag: decoderTLSInsecureSkipVerify,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.decoderInsecureSkipVerify = v
			return nil
		},
	},
	{
		flag: secureServing,
		update: func(opts *Options, value any) error {
			v, err := extractBool(value)
			if err != nil {
				return err
			}
			opts.SecureServing = v
			return nil
		},
	},
	{
		flag: certPath,
		update: func(opts *Options, value any) error {
			v, err := extractString(value)
			if err != nil {
				return err
			}
			opts.CertPath = v
			return nil
		},
	},
	{
		flag: poolGroup,
		update: func(opts *Options, value any) error {
			v, err := extractString(value)
			if err != nil {
				return err
			}
			opts.PoolGroup = v
			return nil
		},
	},
}

// updateSidecarConfiguration updates value from YAML only when:
// 1. YAML configuration contains value
// 2. sidecar configuration contains default value
// i.e. gives higher priority to configuration provided individually through flags (e.g. `--port`, `--vllm-port`) over configuration provided through YAML
func (opts *Options) updateSidecarConfiguration(configurationMap configurationMap) error {
	for _, flagConfiguration := range flagConfigurationList {
		value, ok := configurationMap[flagConfiguration.flag]
		if !ok {
			continue
		}
		removeItems(&opts.ConfigurationState.Defaults, []string{flagConfiguration.flag})
		if opts.isParsed(flagConfiguration.flag) {
			appendItem(&opts.ConfigurationState.FromFlags, flagConfiguration.flag)
			continue
		}
		if err := flagConfiguration.update(opts, value); err != nil {
			return fmt.Errorf("update failed for: %v. %w", value, err)
		}
	}
	if v, ok := configurationMap[inferencePool].(string); ok {
		if opts.inferencePool == "" {
			opts.inferencePool = v
			removeItems(&opts.ConfigurationState.FromEnv, []string{inferencePool, inferencePoolNamespace, inferencePoolName})
			removeItems(&opts.ConfigurationState.FromFlags, []string{inferencePool, inferencePoolNamespace, inferencePoolName})
		}
	}
	return nil
}

// isParsed returns true if flag contains parsed value (and not default value)
func (opts *Options) isParsed(parameter string) bool {
	flag := opts.FlagSet.Lookup(parameter)
	return flag == nil || flag.Changed
}

// extractString extracts string from interface
func extractString(value any) (string, error) {
	v, ok := value.(string)
	if !ok {
		return "", fmt.Errorf("type assertion failed for value: %v", value)
	}
	return v, nil
}

// extractSliceString returns slice of strings from interface
func extractSliceString(value any) ([]string, error) {
	switch v := value.(type) {
	case []string:
		return v, nil
	case []any:
		result := make([]string, 0, len(v))
		for _, item := range v {
			str, ok := item.(string)
			if !ok {
				return nil, fmt.Errorf("type assertion failed for value: %v", value)
			}
			result = append(result, str)
		}
		return result, nil
	case string:
		if v == "" {
			return nil, nil
		}
		return strings.Split(v, ","), nil
	default:
		return nil, fmt.Errorf("type assertion failed for value: %v", value)
	}
}

// extractBool extracts bool from interface
func extractBool(value any) (bool, error) {
	v, ok := value.(bool)
	if !ok {
		return false, fmt.Errorf("type assertion failed for value: %v", value)
	}
	return v, nil
}

// extractInt extracts int from interface
func extractInt(value any) (int, error) {
	switch v := value.(type) {
	case int:
		return v, nil
	case float64:
		return int(v), nil
	default:
		return 0, fmt.Errorf("type assertion failed for value: %v", value)
	}
}

// removeDuplicates removes duplicate value from lower priority slice
func removeDuplicates(lowPriority *[]string, highPriority []string) {
	temp := make(map[string]bool)
	for _, item := range highPriority {
		temp[item] = true
	}
	result := (*lowPriority)[:0]
	for _, item := range *lowPriority {
		if !temp[item] {
			result = append(result, item)
		}
	}
	*lowPriority = result
}

// removeItems removes list of items from a slice
func removeItems(slice *[]string, items []string) {
	temp := make(map[string]struct{})
	for _, item := range items {
		temp[item] = struct{}{}
	}
	result := (*slice)[:0]
	for _, value := range *slice {
		if _, ok := temp[value]; !ok {
			result = append(result, value)
		}
	}
	*slice = result
}

// appendItem appends an item to slice only if it is unique
func appendItem(slice *[]string, item string) {
	if !slices.Contains(*slice, item) {
		*slice = append(*slice, item)
	}
}
