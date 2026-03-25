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
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

var validSidecarfilePath, invalidSidecarfilePath string

// createSidecarConfigurationYAMLFiles creates sidecar configuration files with valid and invalid YAML
func createSidecarConfigurationYAMLFiles(t *testing.T) {
	tempDir := t.TempDir()
	validYAML := []byte(`
port: 8083
vllm-port: 8201
data-parallel-size: 5
kv-connector: "sglang"
secure-proxy: true
ec-connector: "ec-example"
data-parallel-size: 6
enable-ssrf-protection: true
inference-pool: "pool3"
enable-tls: 
- decoder
- encoder
`)
	invalidYAML := []byte(`
port: 8083
vllm-port: 8201
*&&&&&&#######!!
`)
	// create sidecar configuration file with valid YAML
	validSidecarfilePath = filepath.Join(tempDir, "sidecar-configuration-valid.yaml")
	err := os.WriteFile(validSidecarfilePath, validYAML, 0644)
	if err != nil {
		t.Fatalf("failed to write sidecar configuration file: %v", err)
	}
	// create sidecar configuration file with invalid YAML
	invalidSidecarfilePath = filepath.Join(tempDir, "sidecar-configuration-invalid.yaml")
	err = os.WriteFile(invalidSidecarfilePath, invalidYAML, 0644)
	if err != nil {
		t.Fatalf("failed to write sidecar configuration file: %v", err)
	}
}

func newTestOptions(t *testing.T) (*Options, *pflag.FlagSet) {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	testFlagSet := pflag.NewFlagSet(t.Name(), pflag.ContinueOnError)
	opts := NewOptions()
	opts.AddFlags(testFlagSet)
	opts.FlagSet = testFlagSet
	return opts, testFlagSet
}

// TestSidecarConfig tests following cases:
// case 1: no sidecar configuration provided by user i.e. default values are used
// case 2: configuration provided individually through flags (e.g `--port`, `--vllm-port`)
// case 3: YAML configuration provided through inline specification `--configuration`
// case 4: YAML configuration provided through file `--configuration-file`
// case 5: case 2 + case 3 i.e. configuration provided through individual flags + YAML inline specification
// case 6: case 2 + case 4 i.e. configuration provided through individual flags + file
// case 7: case 3 + case 4 i.e. configuration provided through YAML inline specification + file
// case 8: case 2 + case 3 + case 4 i.e. configuration provided through individual flags + YAML inline specification + file
// case 9: invalid YAML configuration provided through inline specification `--configuration`
// case 10: invalid YAML configuration provided through file `--configuration-file`
func TestSidecarConfig(t *testing.T) {
	port := "8100"
	inlinePort := "8200"
	vllmPort := "7100"
	inlinevllmPort := "7200"
	fileDataParallelSize := 5
	kvConnectorNIXLV2 := fmt.Sprintf("%v", KVConnectorNIXLV2)
	kvConnectorSGLang := fmt.Sprintf("%v", KVConnectorSGLang)
	ecConnector := fmt.Sprintf("%v", ECExampleConnector)
	secureProxyEnabled := true
	secureProxyDisabled := false
	ssrfProtectionEnabled := true
	ssrfProtectionDisabled := false
	inferencePool := "pool1"
	fileInferencePool := "pool3"
	enableTLSDecode := []string{decodeStage}
	enableTLSPrefill := []string{prefillStage}
	configuration := "{port: 8200, vllm-port: 7200, kv-connector: sglang, enableTLS: 'prefiller,decoder'}"
	invalidConfiguration := "{port: 8200, vllm-port: 'sh'"
	expectedError := errors.New("Failed to unmarshal sidecar configuration")
	createSidecarConfigurationYAMLFiles(t)

	tests := []struct {
		name                         string
		expectedConfig               string
		expectedConfigFile           string
		expectedPort                 string
		expectedVLLMPort             string
		expectedKVConnector          string
		expectedECConnector          string
		expectedSecureProxy          bool
		expectedDataParallelSize     int
		expectedEnableSSRFProtection bool
		expectedInferencePool        string
		expectedUseTLSForDecoder     bool
		expectedUseTLSForPrefiller   bool
		expectedUseTLSForEncoder     bool
		expectedError                error
		inputFlags                   map[string]any
	}{
		{
			name:                         "case 1: no sidecar configuration provided by user i.e. default values are used",
			inputFlags:                   nil,
			expectedPort:                 defaultPort,
			expectedVLLMPort:             defaultvLLMPort,
			expectedDataParallelSize:     defaultDataParallelSize,
			expectedKVConnector:          kvConnectorNIXLV2,
			expectedECConnector:          "",
			expectedSecureProxy:          secureProxyEnabled,
			expectedEnableSSRFProtection: false,
			expectedInferencePool:        "",
			expectedUseTLSForPrefiller:   false,
			expectedUseTLSForDecoder:     false,
			expectedUseTLSForEncoder:     false,
			expectedConfig:               "",
			expectedConfigFile:           "",
			expectedError:                nil,
		},
		{
			name: "case 2: configuration provided individually through flags (e.g `--port`, `--vllm-port`)",
			inputFlags: map[string]any{
				"port":                   port,
				"vllm-port":              vllmPort,
				"kv-connector":           kvConnectorSGLang,
				"ec-connector":           ecConnector,
				"secure-proxy":           secureProxyDisabled,
				"enable-ssrf-protection": ssrfProtectionEnabled,
				"inference-pool":         inferencePool,
				"enable-tls":             &enableTLSDecode,
			},
			expectedPort:                 port,
			expectedVLLMPort:             vllmPort,
			expectedDataParallelSize:     defaultDataParallelSize,
			expectedKVConnector:          KVConnectorSGLang,
			expectedECConnector:          ecConnector,
			expectedSecureProxy:          secureProxyDisabled,
			expectedEnableSSRFProtection: ssrfProtectionEnabled,
			expectedInferencePool:        inferencePool,
			expectedUseTLSForPrefiller:   false,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     false,
			expectedConfig:               "",
			expectedConfigFile:           "",
			expectedError:                nil,
		},
		{
			name: "case 3: YAML configuration provided through inline specification `--configuration`",
			inputFlags: map[string]any{

				"configuration": &configuration,
			},
			expectedPort:                 inlinePort,
			expectedVLLMPort:             inlinevllmPort,
			expectedDataParallelSize:     defaultDataParallelSize,
			expectedKVConnector:          KVConnectorSGLang,
			expectedSecureProxy:          true,
			expectedEnableSSRFProtection: ssrfProtectionDisabled,
			expectedInferencePool:        "",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     false,
			expectedConfig:               configuration,
			expectedConfigFile:           "",
			expectedError:                nil,
		},
		{
			name: "case 4: YAML configuration provided through file `--configuration-file`",
			inputFlags: map[string]any{
				"configuration-file": validSidecarfilePath,
			},
			expectedPort:                 "8083",
			expectedVLLMPort:             "8201",
			expectedDataParallelSize:     fileDataParallelSize,
			expectedKVConnector:          KVConnectorSGLang,
			expectedECConnector:          ecConnector,
			expectedSecureProxy:          true,
			expectedEnableSSRFProtection: true,
			expectedInferencePool:        fileInferencePool,
			expectedUseTLSForPrefiller:   false,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     true,
			expectedConfig:               "",
			expectedConfigFile:           validSidecarfilePath,
			expectedError:                nil,
		},
		{
			name: "case 5: case 2 + case 3 i.e. configuration provided through individual flags + YAML inline specification",
			inputFlags: map[string]any{
				"port":          port,
				"vllm-port":     vllmPort,
				"kv-connector":  kvConnectorNIXLV2,
				"secure-proxy":  secureProxyDisabled,
				"configuration": configuration,
			},
			expectedPort:                 port,
			expectedVLLMPort:             vllmPort,
			expectedDataParallelSize:     defaultDataParallelSize,
			expectedKVConnector:          KVConnectorNIXLV2,
			expectedSecureProxy:          secureProxyDisabled,
			expectedEnableSSRFProtection: ssrfProtectionDisabled,
			expectedInferencePool:        "",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     true,
			expectedConfig:               configuration,
			expectedConfigFile:           "",
			expectedError:                nil,
		},
		{
			name: "case 6: case 2 + case 4 i.e. configuration provided through individual flags + file",
			inputFlags: map[string]any{
				"port":               port,
				"vllm-port":          vllmPort,
				"kv-connector":       kvConnectorNIXLV2,
				"enable-tls":         &enableTLSPrefill,
				"configuration-file": validSidecarfilePath,
			},
			expectedPort:                 port,
			expectedVLLMPort:             vllmPort,
			expectedDataParallelSize:     fileDataParallelSize,
			expectedKVConnector:          KVConnectorNIXLV2,
			expectedECConnector:          ecConnector,
			expectedSecureProxy:          secureProxyEnabled,
			expectedEnableSSRFProtection: ssrfProtectionEnabled,
			expectedInferencePool:        fileInferencePool,
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     true,
			expectedConfig:               "",
			expectedConfigFile:           validSidecarfilePath,
			expectedError:                nil,
		},
		{
			name: "case 7: case 3 + case 4 i.e. configuration provided through YAML inline specification + file",
			inputFlags: map[string]any{
				"configuration":      configuration,
				"configuration-file": validSidecarfilePath,
			},
			expectedPort:                 inlinePort,
			expectedVLLMPort:             inlinevllmPort,
			expectedDataParallelSize:     fileDataParallelSize,
			expectedKVConnector:          KVConnectorSGLang,
			expectedECConnector:          ecConnector,
			expectedSecureProxy:          secureProxyEnabled,
			expectedEnableSSRFProtection: ssrfProtectionEnabled,
			expectedInferencePool:        fileInferencePool,
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     true,
			expectedConfig:               configuration,
			expectedConfigFile:           validSidecarfilePath,
			expectedError:                nil,
		},
		{
			name: "case 8: case 2 + case 3 + case 4 i.e. configuration provided through individual flags + YAML inline specification + file",
			inputFlags: map[string]any{
				"port":               port,
				"vllm-port":          vllmPort,
				"configuration":      configuration,
				"configuration-file": validSidecarfilePath,
			},
			expectedPort:                 port,
			expectedVLLMPort:             vllmPort,
			expectedDataParallelSize:     defaultDataParallelSize,
			expectedKVConnector:          KVConnectorSGLang,
			expectedECConnector:          ecConnector,
			expectedSecureProxy:          secureProxyEnabled,
			expectedEnableSSRFProtection: ssrfProtectionEnabled,
			expectedInferencePool:        fileInferencePool,
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedUseTLSForEncoder:     true,
			expectedConfig:               configuration,
			expectedConfigFile:           validSidecarfilePath,
			expectedError:                nil,
		},
		{
			name: "case 9: invalid YAML configuration provided through inline specification `--configuration`",
			inputFlags: map[string]any{
				"configuration": invalidConfiguration,
			},
			expectedPort:                 defaultPort,
			expectedVLLMPort:             defaultvLLMPort,
			expectedDataParallelSize:     defaultDataParallelSize,
			expectedKVConnector:          kvConnectorNIXLV2,
			expectedECConnector:          "",
			expectedSecureProxy:          true,
			expectedEnableSSRFProtection: ssrfProtectionDisabled,
			expectedInferencePool:        "",
			expectedUseTLSForPrefiller:   false,
			expectedUseTLSForDecoder:     false,
			expectedUseTLSForEncoder:     false,
			expectedConfig:               invalidConfiguration,
			expectedConfigFile:           "",
			expectedError:                expectedError,
		},
		{
			name: "case 10: invalid YAML configuration provided through file `--configuration-file`",
			inputFlags: map[string]any{
				"configuration-file": invalidSidecarfilePath,
			},
			expectedPort:               defaultPort,
			expectedVLLMPort:           defaultvLLMPort,
			expectedDataParallelSize:   defaultDataParallelSize,
			expectedKVConnector:        kvConnectorNIXLV2,
			expectedECConnector:        "",
			expectedSecureProxy:        true,
			expectedUseTLSForPrefiller: false,
			expectedUseTLSForDecoder:   false,
			expectedUseTLSForEncoder:   false,
			expectedConfig:             "",
			expectedConfigFile:         "",
			expectedError:              expectedError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts, testFlagSet := newTestOptions(t)
			for k, val := range tt.inputFlags {
				switch v := val.(type) {
				case int:
					require.NoError(t, testFlagSet.Set(k, strconv.Itoa(v)))
				case float64:
					require.NoError(t, testFlagSet.Set(k, fmt.Sprintf("%v", v)))
				case string:
					require.NoError(t, testFlagSet.Set(k, v))
				case *string:
					require.NoError(t, testFlagSet.Set(k, *v))
				case *[]string:
					require.NoError(t, testFlagSet.Set(k, strings.Join(*v, " ")))
				case bool:
					require.NoError(t, testFlagSet.Set(k, strconv.FormatBool(v)))
				default:
					t.Errorf("%v type is unknown, value: %v", tt.inputFlags, v)
				}
			}
			err := testFlagSet.Parse(nil)
			require.NoError(t, err, "Flag Parse() error: %v", err)
			err = opts.Complete()
			if tt.expectedError != nil {
				require.EqualErrorf(t, err, tt.expectedError.Error(), "Error should be: %v, got: %v", tt.expectedError, err)
				return
			} else {
				require.NoError(t, err, "Complete() error: %v", err)
			}
			err = opts.Validate()
			require.NoError(t, err, "Validate() error: %v", err)
			require.Equal(t, tt.expectedPort, opts.Port)
			require.Equal(t, tt.expectedVLLMPort, opts.VLLMPort)
			require.Equal(t, tt.expectedKVConnector, opts.KVConnector)
			require.Equal(t, tt.expectedECConnector, opts.ECConnector)
			require.Equal(t, tt.expectedSecureProxy, opts.SecureProxy)
			require.Equal(t, tt.expectedEnableSSRFProtection, opts.EnableSSRFProtection)
			require.Equal(t, tt.expectedInferencePool, opts.InferencePool)
			require.Equal(t, tt.expectedConfig, opts.Configuration)
			require.Equal(t, tt.expectedConfigFile, opts.ConfigurationFile)
			expectedScheme := "http"
			if opts.UseTLSForDecoder {
				expectedScheme = "https"
			}
			require.Equal(t, expectedScheme+"://localhost:"+opts.VLLMPort, opts.TargetURL)
			if tt.inputFlags["enable-tls"] != nil {
				switch v := tt.inputFlags["enable-tls"].(type) {
				case *string:
					opts.EnableTLS = strings.Split(*v, ",")
				case *[]string:
					for _, s := range *v {
						opts.EnableTLS = append(opts.EnableTLS, fmt.Sprintf("%v", s))
						if tt.expectedUseTLSForPrefiller {
							require.True(t, slices.Contains(opts.EnableTLS, prefillStage))
						}
						if tt.expectedUseTLSForDecoder {
							require.True(t, slices.Contains(opts.EnableTLS, decodeStage))
						}
						if tt.expectedUseTLSForEncoder {
							require.True(t, slices.Contains(opts.EnableTLS, encodeStage))
						}
					}
				default:
					t.Errorf("%v type is unknown, value: %v\n", tt.inputFlags["enable-tls"], v)
				}
			}
		})
	}
}

func TestNewOptionsWithEnvVars(t *testing.T) {
	// Set environment variables - t.Setenv automatically handles cleanup
	t.Setenv("INFERENCE_POOL_NAMESPACE", "test-namespace")
	t.Setenv("INFERENCE_POOL_NAME", "test-pool")
	t.Setenv("ENABLE_PREFILLER_SAMPLING", "true")

	opts := NewOptions()

	if opts.InferencePoolNamespace != "test-namespace" {
		t.Errorf("Expected InferencePoolNamespace to be 'test-namespace', got '%s'", opts.InferencePoolNamespace)
	}
	if opts.InferencePoolName != "test-pool" {
		t.Errorf("Expected InferencePoolName to be 'test-pool', got '%s'", opts.InferencePoolName)
	}
	if !opts.EnablePrefillerSampling {
		t.Error("Expected EnablePrefillerSampling to be true")
	}
}

func TestValidateConnector(t *testing.T) {
	tests := []struct {
		name      string
		connector string
		wantErr   bool
	}{
		{"valid nixlv2", KVConnectorNIXLV2, false},
		{"valid shared-storage", KVConnectorSharedStorage, false},
		{"valid sglang", KVConnectorSGLang, false},
		{"invalid connector", "invalid", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.Connector = tt.connector
			_ = opts.Complete() // Complete must be called before Validate
			err := opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateTLSStages(t *testing.T) {
	tests := []struct {
		name      string
		enableTLS []string
		wantErr   bool
	}{
		{name: "valid prefiller", enableTLS: []string{"prefiller"}, wantErr: false},
		{name: "valid decoder", enableTLS: []string{"decoder"}, wantErr: false},
		{name: "valid both", enableTLS: []string{"prefiller", "decoder"}, wantErr: false},
		{name: "invalid stage", enableTLS: []string{"invalid"}, wantErr: true},
		{name: "mixed valid and invalid", enableTLS: []string{"prefiller", "invalid"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.EnableTLS = tt.enableTLS
			_ = opts.Complete() // Complete must be called before Validate
			err := opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateSSRFProtection(t *testing.T) {
	tests := []struct {
		name      string
		enabled   bool
		namespace string
		poolName  string
		wantErr   bool
	}{
		{name: "disabled", enabled: false, namespace: "", poolName: "", wantErr: false},
		{name: "enabled with both", enabled: true, namespace: "ns", poolName: "pool", wantErr: false},
		{name: "enabled missing namespace", enabled: true, namespace: "", poolName: "pool", wantErr: true},
		{name: "enabled missing pool name", enabled: true, namespace: "ns", poolName: "", wantErr: true},
		{name: "enabled missing both", enabled: true, namespace: "", poolName: "", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.EnableSSRFProtection = tt.enabled
			opts.InferencePoolNamespace = tt.namespace
			opts.InferencePoolName = tt.poolName
			_ = opts.Complete() // Complete must be called before Validate
			err := opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompleteInferencePoolParsing(t *testing.T) {
	tests := []struct {
		name              string
		inferencePool     string
		expectedNamespace string
		expectedName      string
	}{
		{
			name:              "namespace/name format",
			inferencePool:     "my-namespace/my-pool",
			expectedNamespace: "my-namespace",
			expectedName:      "my-pool",
		},
		{
			name:              "name only implies default namespace",
			inferencePool:     "my-pool",
			expectedNamespace: "default",
			expectedName:      "my-pool",
		},
		{
			name:              "empty string does not set values",
			inferencePool:     "",
			expectedNamespace: "",
			expectedName:      "",
		},
		{
			name:              "deprecated flags take precedence when InferencePool is empty",
			inferencePool:     "",
			expectedNamespace: "",
			expectedName:      "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.InferencePool = tt.inferencePool

			err := opts.Complete()
			if err != nil {
				t.Fatalf("Complete() unexpected error: %v", err)
			}

			if opts.InferencePoolNamespace != tt.expectedNamespace {
				t.Errorf("InferencePoolNamespace = %v, want %v", opts.InferencePoolNamespace, tt.expectedNamespace)
			}
			if opts.InferencePoolName != tt.expectedName {
				t.Errorf("InferencePoolName = %v, want %v", opts.InferencePoolName, tt.expectedName)
			}
		})
	}
}

func TestCompleteTLSConfiguration(t *testing.T) {
	tests := []struct {
		name                         string
		enableTLS                    []string
		tlsInsecureSkipVerify        []string
		deprecatedPrefillerUseTLS    bool
		deprecatedDecoderUseTLS      bool
		deprecatedPrefillerInsecure  bool
		deprecatedDecoderInsecure    bool
		vllmPort                     string
		expectedTargetURL            string
		expectedUseTLSForPrefiller   bool
		expectedUseTLSForDecoder     bool
		expectedInsecureForPrefiller bool
		expectedInsecureForDecoder   bool
	}{
		{
			name:                         "no TLS configuration",
			enableTLS:                    []string{},
			tlsInsecureSkipVerify:        []string{},
			vllmPort:                     "8001",
			expectedTargetURL:            "http://localhost:8001",
			expectedUseTLSForPrefiller:   false,
			expectedUseTLSForDecoder:     false,
			expectedInsecureForPrefiller: false,
			expectedInsecureForDecoder:   false,
		},
		{
			name:                         "prefiller TLS only",
			enableTLS:                    []string{"prefiller"},
			tlsInsecureSkipVerify:        []string{},
			vllmPort:                     "8001",
			expectedTargetURL:            "http://localhost:8001",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     false,
			expectedInsecureForPrefiller: false,
			expectedInsecureForDecoder:   false,
		},
		{
			name:                         "decoder TLS only",
			enableTLS:                    []string{"decoder"},
			tlsInsecureSkipVerify:        []string{},
			vllmPort:                     "8001",
			expectedTargetURL:            "https://localhost:8001",
			expectedUseTLSForPrefiller:   false,
			expectedUseTLSForDecoder:     true,
			expectedInsecureForPrefiller: false,
			expectedInsecureForDecoder:   false,
		},
		{
			name:                         "both stages TLS",
			enableTLS:                    []string{"prefiller", "decoder"},
			tlsInsecureSkipVerify:        []string{},
			vllmPort:                     "9000",
			expectedTargetURL:            "https://localhost:9000",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedInsecureForPrefiller: false,
			expectedInsecureForDecoder:   false,
		},
		{
			name:                         "TLS with insecure skip verify",
			enableTLS:                    []string{"prefiller", "decoder"},
			tlsInsecureSkipVerify:        []string{"prefiller", "decoder"},
			vllmPort:                     "8001",
			expectedTargetURL:            "https://localhost:8001",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedInsecureForPrefiller: true,
			expectedInsecureForDecoder:   true,
		},
		{
			name:                         "deprecated flags migration",
			enableTLS:                    []string{},
			tlsInsecureSkipVerify:        []string{},
			deprecatedPrefillerUseTLS:    true,
			deprecatedDecoderUseTLS:      true,
			deprecatedPrefillerInsecure:  true,
			deprecatedDecoderInsecure:    true,
			vllmPort:                     "8001",
			expectedTargetURL:            "https://localhost:8001",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedInsecureForPrefiller: true,
			expectedInsecureForDecoder:   true,
		},
		{
			name:                         "mixed deprecated and new flags",
			enableTLS:                    []string{"prefiller"},
			tlsInsecureSkipVerify:        []string{},
			deprecatedDecoderUseTLS:      true,
			deprecatedDecoderInsecure:    true,
			vllmPort:                     "8001",
			expectedTargetURL:            "https://localhost:8001",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedInsecureForPrefiller: false,
			expectedInsecureForDecoder:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.EnableTLS = tt.enableTLS
			opts.TLSInsecureSkipVerify = tt.tlsInsecureSkipVerify
			opts.PrefillerUseTLS = tt.deprecatedPrefillerUseTLS
			opts.DecoderUseTLS = tt.deprecatedDecoderUseTLS
			opts.PrefillerInsecureSkipVerify = tt.deprecatedPrefillerInsecure
			opts.DecoderInsecureSkipVerify = tt.deprecatedDecoderInsecure
			opts.VLLMPort = tt.vllmPort

			err := opts.Complete()
			if err != nil {
				t.Fatalf("Complete() unexpected error: %v", err)
			}

			// Verify configuration fields
			if opts.UseTLSForPrefiller != tt.expectedUseTLSForPrefiller {
				t.Errorf("UseTLSForPrefiller = %v, want %v", opts.UseTLSForPrefiller, tt.expectedUseTLSForPrefiller)
			}
			if opts.UseTLSForDecoder != tt.expectedUseTLSForDecoder {
				t.Errorf("UseTLSForDecoder = %v, want %v", opts.UseTLSForDecoder, tt.expectedUseTLSForDecoder)
			}
			if opts.InsecureSkipVerifyForPrefiller != tt.expectedInsecureForPrefiller {
				t.Errorf("InsecureSkipVerifyForPrefiller = %v, want %v", opts.InsecureSkipVerifyForPrefiller, tt.expectedInsecureForPrefiller)
			}
			if opts.InsecureSkipVerifyForDecoder != tt.expectedInsecureForDecoder {
				t.Errorf("InsecureSkipVerifyForDecoder = %v, want %v", opts.InsecureSkipVerifyForDecoder, tt.expectedInsecureForDecoder)
			}
			if opts.TargetURL != tt.expectedTargetURL {
				t.Errorf("TargetURL = %v, want %v", opts.TargetURL, tt.expectedTargetURL)
			}

		})
	}
}
