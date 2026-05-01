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
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/spf13/pflag"
	"github.com/stretchr/testify/require"
)

func writeTempYAML(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

func createConfigWithValidYAML(t *testing.T) string {
	t.Helper()
	return writeTempYAML(t, "valid.yaml", `
port: 8100
vllm-port: 8200
data-parallel-size: 5
kv-connector: "sglang"
ec-connector: "ec-example"
enable-ssrf-protection: true
enable-prefiller-sampling: true
enable-tls:
- prefiller
- decoder
prefiller-use-tls: false
tls-insecure-skip-verify:
- prefiller
decoder-tls-insecure-skip-verify: true
secure-proxy: false
cert-path: "/etc/certificates-file"
inference-pool: "dev/inference-pool-file"
pool-group: "pool-group-file"
`)
}

func createConfigWithUnknownKeys(t *testing.T) string {
	t.Helper()
	return writeTempYAML(t, "valid.yaml", `
port: 8100
vllm-port: 8200
unknown-key: 1001
`)
}

func createConfigWithInvalidYAML(t *testing.T) string {
	t.Helper()
	return writeTempYAML(t, "invalid.yaml", `
port: 8100
invalid-yaml,
`)
}

func TestSidecarConfiguration(t *testing.T) {
	unmarshalError := errors.New("failed to unmarshal sidecar configuration")
	unknownKeysError := errors.New("failed to unmarshal sidecar configuration")
	mutuallyExclusiveError := fmt.Errorf("flags --%s and --%s are mutually exclusive", inlineConfiguration, configurationFile)

	// --- inline YAML for testing ---
	inlineYAMLOverridesDefaults :=
		`{
		port: 8011,
		vllm-port: 8021,
		data-parallel-size: 3,
		kv-connector: sglang,
		ec-connector: ec-example,
		enable-ssrf-protection: true,
		enable-prefiller-sampling: true,
		enable-tls: ['prefiller', 'decoder'],
		prefiller-use-tls: false,
		tls-insecure-skip-verify: ['decoder'],
		prefiller-tls-insecure-skip-verify: true,
		secure-proxy: false,
		cert-path: '/etc/certificates-inline',
		inference-pool: dev/inference-pool-inline,
		pool-group: pool-group-inline,
	}`
	flagsOverridesInlineYAML :=
		`{
		port: 8101,
		vllm-port: 8201,
		data-parallel-size: 4,
		kv-connector: nixlv2,
		enable-ssrf-protection: false,
		enable-prefiller-sampling: false,
		enable-tls: ['decoder', 'encoder'],
		prefiller-use-tls: false,
		tls-insecure-skip-verify: ['decoder', 'encoder'],
		prefiller-tls-insecure-skip-verify: true,
		secure-proxy: false,
		cert-path: '/etc/certificates-inline',
		inference-pool: dev/inference-pool-inline,
		pool-group: pool-group-inline
	}`
	invalidInlineYAML := "{port: 8200, vllm-port: 'sh'"

	// -- file YAML for testing ---
	validYAMLPath := createConfigWithValidYAML(t)
	unknownKeysYAMLPath := createConfigWithUnknownKeys(t)
	invalidYAMLPath := createConfigWithInvalidYAML(t)

	tests := []struct {
		name                                string
		expectedPort                        string
		expectedVLLMPort                    string
		expectedDataParallelSize            int
		expectedKVConnector                 string
		expectedECConnector                 string
		expectedSecureServing               bool
		expectedEnableSSRFProtection        bool
		expectedEnablePrefillerSampling     bool
		expectedEnableTLS                   []string
		expectedUseTLSForPrefiller          bool
		expectedUseTLSForDecoder            bool
		expectedUseTLSForEncoder            bool
		expectedTLSInsecureSkipVerify       []string
		expectedPrefillerInsecureSkipVerify bool
		expectedDecoderInsecureSkipVerify   bool
		expectedEncoderInsecureSkipVerify   bool
		expectedCertPath                    string
		expectedInferencePool               string
		expectedInferencePoolNamespace      string
		expectedInferencePoolName           string
		expectedPoolGroup                   string
		expectedMaxIdleConnections          int
		expectedInlineConfiguration         string
		expectedFileConfiguration           string
		expectedError                       error
		inputFlags                          map[string]any
		inputEnvVar                         map[string]any
	}{
		{
			name: "inline YAML overrides default",
			inputFlags: map[string]any{
				inlineConfiguration: &inlineYAMLOverridesDefaults,
			},
			expectedPort:                        "8011",
			expectedVLLMPort:                    "8021",
			expectedDataParallelSize:            3,
			expectedKVConnector:                 KVConnectorSGLang,
			expectedECConnector:                 ECExampleConnector,
			expectedEnableSSRFProtection:        true,
			expectedEnablePrefillerSampling:     true,
			expectedEnableTLS:                   []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:          true,
			expectedUseTLSForDecoder:            true,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify: true,
			expectedDecoderInsecureSkipVerify:   true,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               false,
			expectedCertPath:                    "/etc/certificates-inline",
			expectedInferencePool:               "dev/inference-pool-inline",
			expectedInferencePoolNamespace:      "dev",
			expectedInferencePoolName:           "inference-pool-inline",
			expectedPoolGroup:                   "pool-group-inline",
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         inlineYAMLOverridesDefaults,
			expectedFileConfiguration:           "",
			expectedError:                       nil,
		},
		{
			name: "file YAML overrides default",
			inputFlags: map[string]any{
				configurationFile: validYAMLPath,
			},
			expectedPort:                        "8100",
			expectedVLLMPort:                    "8200",
			expectedDataParallelSize:            5,
			expectedKVConnector:                 KVConnectorSGLang,
			expectedECConnector:                 ECExampleConnector,
			expectedEnableSSRFProtection:        true,
			expectedEnablePrefillerSampling:     true,
			expectedEnableTLS:                   []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:          true,
			expectedUseTLSForDecoder:            true,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify: true,
			expectedDecoderInsecureSkipVerify:   true,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               false,
			expectedCertPath:                    "/etc/certificates-file",
			expectedInferencePool:               "dev/inference-pool-file",
			expectedInferencePoolNamespace:      "dev",
			expectedInferencePoolName:           "inference-pool-file",
			expectedPoolGroup:                   "pool-group-file",
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         "",
			expectedFileConfiguration:           validYAMLPath,
			expectedError:                       nil,
		},
		{
			name: "flags override inline YAML",
			inputFlags: map[string]any{
				port:                    "8100",
				vllmPort:                "8200",
				dataParallelSize:        2,
				kvConnector:             KVConnectorSGLang,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificates",
				inferencePool:           "test/inference-pool",
				poolGroup:               "pool-group",
				inlineConfiguration:     flagsOverridesInlineYAML,
			},
			expectedPort:                        "8100",
			expectedVLLMPort:                    "8200",
			expectedDataParallelSize:            2,
			expectedKVConnector:                 KVConnectorSGLang,
			expectedECConnector:                 ECExampleConnector,
			expectedEnableSSRFProtection:        true,
			expectedEnablePrefillerSampling:     true,
			expectedEnableTLS:                   []string{prefillStage},
			expectedUseTLSForPrefiller:          true,
			expectedUseTLSForDecoder:            false,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       []string{prefillStage},
			expectedPrefillerInsecureSkipVerify: true,
			expectedDecoderInsecureSkipVerify:   false,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               false,
			expectedCertPath:                    "/etc/certificates",
			expectedInferencePool:               "test/inference-pool",
			expectedInferencePoolNamespace:      "test",
			expectedInferencePoolName:           "inference-pool",
			expectedPoolGroup:                   "pool-group",
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         flagsOverridesInlineYAML,
			expectedFileConfiguration:           "",
			expectedError:                       nil,
		},
		{
			name: "flags override file YAML",
			inputFlags: map[string]any{
				port:                    "8100",
				vllmPort:                "8200",
				dataParallelSize:        2,
				kvConnector:             KVConnectorSGLang,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificates",
				inferencePool:           "test/inference-pool",
				poolGroup:               "pool-group",
				configurationFile:       validYAMLPath,
			},
			expectedPort:                        "8100",
			expectedVLLMPort:                    "8200",
			expectedDataParallelSize:            2,
			expectedKVConnector:                 KVConnectorSGLang,
			expectedECConnector:                 ECExampleConnector,
			expectedEnableSSRFProtection:        true,
			expectedEnablePrefillerSampling:     true,
			expectedEnableTLS:                   []string{prefillStage},
			expectedUseTLSForPrefiller:          true,
			expectedUseTLSForDecoder:            false,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       []string{prefillStage},
			expectedPrefillerInsecureSkipVerify: true,
			expectedDecoderInsecureSkipVerify:   false,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               false,
			expectedCertPath:                    "/etc/certificates",
			expectedInferencePool:               "test/inference-pool",
			expectedInferencePoolNamespace:      "test",
			expectedInferencePoolName:           "inference-pool",
			expectedPoolGroup:                   "pool-group",
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         "",
			expectedFileConfiguration:           validYAMLPath,
			expectedError:                       nil,
		},
		{
			name: "invalid inline YAML ",
			inputFlags: map[string]any{
				inlineConfiguration: invalidInlineYAML,
			},
			expectedPort:                        defaultPort,
			expectedVLLMPort:                    defaultVLLMPort,
			expectedDataParallelSize:            defaultDataParallelSize,
			expectedKVConnector:                 KVConnectorNIXLV2,
			expectedECConnector:                 "",
			expectedEnableSSRFProtection:        false,
			expectedEnablePrefillerSampling:     false,
			expectedEnableTLS:                   nil,
			expectedUseTLSForPrefiller:          false,
			expectedUseTLSForDecoder:            false,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       nil,
			expectedPrefillerInsecureSkipVerify: false,
			expectedDecoderInsecureSkipVerify:   false,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               true,
			expectedCertPath:                    "",
			expectedInferencePool:               "",
			expectedInferencePoolNamespace:      "",
			expectedInferencePoolName:           "",
			expectedPoolGroup:                   DefaultPoolGroup,
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         invalidInlineYAML,
			expectedFileConfiguration:           "",
			expectedError:                       unmarshalError,
		},
		{
			name: "invalid file YAML",
			inputFlags: map[string]any{
				configurationFile: invalidYAMLPath,
			},
			expectedPort:                        defaultPort,
			expectedVLLMPort:                    defaultVLLMPort,
			expectedDataParallelSize:            defaultDataParallelSize,
			expectedKVConnector:                 KVConnectorNIXLV2,
			expectedECConnector:                 "",
			expectedEnableSSRFProtection:        false,
			expectedEnablePrefillerSampling:     false,
			expectedEnableTLS:                   nil,
			expectedUseTLSForPrefiller:          false,
			expectedUseTLSForDecoder:            false,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       nil,
			expectedPrefillerInsecureSkipVerify: false,
			expectedDecoderInsecureSkipVerify:   false,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               true,
			expectedCertPath:                    "",
			expectedInferencePool:               "",
			expectedInferencePoolNamespace:      "",
			expectedInferencePoolName:           "",
			expectedPoolGroup:                   DefaultPoolGroup,
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         "",
			expectedFileConfiguration:           invalidYAMLPath,
			expectedError:                       unmarshalError,
		},
		{
			name: "unknown keys in YAML",
			inputFlags: map[string]any{
				configurationFile: unknownKeysYAMLPath,
			},
			expectedPort:                        "8100",
			expectedVLLMPort:                    "8200",
			expectedDataParallelSize:            1,
			expectedKVConnector:                 KVConnectorNIXLV2,
			expectedECConnector:                 "",
			expectedEnableSSRFProtection:        false,
			expectedEnablePrefillerSampling:     false,
			expectedEnableTLS:                   nil,
			expectedUseTLSForPrefiller:          false,
			expectedUseTLSForDecoder:            false,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       nil,
			expectedPrefillerInsecureSkipVerify: false,
			expectedDecoderInsecureSkipVerify:   false,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               true,
			expectedCertPath:                    "",
			expectedInferencePool:               "",
			expectedInferencePoolNamespace:      "",
			expectedInferencePoolName:           "",
			expectedPoolGroup:                   "inference.networking.k8s.io",
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         "",
			expectedFileConfiguration:           unknownKeysYAMLPath,
			expectedError:                       unknownKeysError,
		},
		{
			name: "both inline and file YAML",
			inputFlags: map[string]any{
				inlineConfiguration: &inlineYAMLOverridesDefaults,
				configurationFile:   validYAMLPath,
			},
			expectedPort:                        defaultPort,
			expectedVLLMPort:                    defaultVLLMPort,
			expectedDataParallelSize:            defaultDataParallelSize,
			expectedKVConnector:                 KVConnectorNIXLV2,
			expectedECConnector:                 "",
			expectedEnableSSRFProtection:        false,
			expectedEnablePrefillerSampling:     false,
			expectedEnableTLS:                   nil,
			expectedUseTLSForPrefiller:          false,
			expectedUseTLSForDecoder:            false,
			expectedUseTLSForEncoder:            false,
			expectedTLSInsecureSkipVerify:       nil,
			expectedPrefillerInsecureSkipVerify: false,
			expectedDecoderInsecureSkipVerify:   false,
			expectedEncoderInsecureSkipVerify:   false,
			expectedSecureServing:               true,
			expectedCertPath:                    "",
			expectedInferencePool:               "",
			expectedInferencePoolNamespace:      "",
			expectedInferencePoolName:           "",
			expectedPoolGroup:                   DefaultPoolGroup,
			expectedMaxIdleConnections:          defaultMaxIdleConnsPerHost,
			expectedInlineConfiguration:         "",
			expectedFileConfiguration:           invalidYAMLPath,
			expectedError:                       mutuallyExclusiveError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEnv(t, tt.inputEnvVar)
			testGoFlagSet := flag.NewFlagSet(t.Name(), flag.ContinueOnError)
			testPFlagSet := pflag.NewFlagSet(t.Name(), pflag.ContinueOnError)
			opts := NewOptions()
			opts.loggingOptions.BindFlags(testGoFlagSet)
			opts.AddFlags(testPFlagSet)
			testPFlagSet.AddGoFlagSet(testGoFlagSet)
			for name, val := range tt.inputFlags {
				setFlag(t, testPFlagSet, name, val)
			}
			require.NoError(t, testPFlagSet.Parse(nil))
			err := opts.Complete()
			if tt.expectedError != nil {
				require.ErrorContains(t, err, tt.expectedError.Error(), "Error should be: %v, got: %v", tt.expectedError, err)
				return
			}
			require.NoError(t, err, "Complete() error: %v", err)
			require.NoError(t, opts.Validate())
			assertEqual := func(name string, expected, actual any) {
				t.Helper()
				require.Equal(t, expected, actual, "expected %v to be %v but got %v", name, expected, actual)
			}
			assertSlice := func(name string, expected, actual []string) {
				t.Helper()
				ok, missing, extra := compareSlices(expected, actual)
				require.True(t, ok,
					"%s mismatch:\nexpected: %v\ngot: %v\nextra: %v\nmissing: %v",
					name, expected, actual, extra, missing)
			}

			assertEqual(port, tt.expectedPort, opts.Port)
			assertEqual(vllmPort, tt.expectedVLLMPort, opts.vllmPort)
			assertEqual(dataParallelSize, tt.expectedDataParallelSize, opts.DataParallelSize)
			assertEqual(MaxIdleConnsPerHost, tt.expectedMaxIdleConnections, opts.MaxIdleConnsPerHost)

			// --- connectors ---
			assertEqual(kvConnector, tt.expectedKVConnector, opts.KVConnector)
			assertEqual(ecConnector, tt.expectedECConnector, opts.ECConnector)

			// -- enable/disable features ---
			assertEqual(enableSSRFProtection, tt.expectedEnableSSRFProtection, opts.EnableSSRFProtection)
			assertEqual(enablePrefillerSampling, tt.expectedEnablePrefillerSampling, opts.EnablePrefillerSampling)

			// --- TLS usage ---
			assertEqual(prefillerUseTLS, tt.expectedUseTLSForPrefiller, opts.UseTLSForPrefiller)
			assertEqual(decoderUseTLS, tt.expectedUseTLSForDecoder, opts.UseTLSForDecoder)
			assertEqual(encoderUseTLS, tt.expectedUseTLSForEncoder, opts.UseTLSForEncoder)

			// --- TLS insecure skip verify ---
			assertEqual(prefillerTLSInsecureSkipVerify, tt.expectedPrefillerInsecureSkipVerify, opts.InsecureSkipVerifyForPrefiller)
			assertEqual(decoderTLSInsecureSkipVerify, tt.expectedDecoderInsecureSkipVerify, opts.InsecureSkipVerifyForDecoder)
			assertEqual("InsecureSkipVerifyForEncoder", tt.expectedEncoderInsecureSkipVerify, opts.InsecureSkipVerifyForEncoder)

			// --- slices ---
			assertSlice(enableTLS, tt.expectedEnableTLS, opts.enableTLS)
			assertSlice(tlsInsecureSkipVerify, tt.expectedTLSInsecureSkipVerify, opts.tlsInsecureSkipVerify)

			// --- secure serving + cert ---
			assertEqual(certPath, tt.expectedCertPath, opts.CertPath)
			assertEqual(secureServing, tt.expectedSecureServing, opts.SecureServing)

			// --- inference pool ---
			assertEqual(inferencePool, tt.expectedInferencePool, opts.inferencePool)
			assertEqual(inferencePoolNamespace, tt.expectedInferencePoolNamespace, opts.InferencePoolNamespace)
			assertEqual(inferencePoolName, tt.expectedInferencePoolName, opts.InferencePoolName)
			assertEqual(poolGroup, tt.expectedPoolGroup, opts.PoolGroup)

			// --- configuration sources ---
			assertEqual("inlineConfiguration", tt.expectedInlineConfiguration, opts.inlineConfiguration)
			assertEqual("fileConfiguration", tt.expectedFileConfiguration, opts.fileConfiguration)

			// --- decoder URL ---
			require.Equal(t, calculateURL(t, tt.expectedUseTLSForDecoder, tt.expectedVLLMPort), opts.DecoderURL)
		})
	}
}

func setEnv(t *testing.T, env map[string]any) {
	t.Helper()
	for k, v := range env {
		switch val := v.(type) {
		case string:
			t.Setenv(k, val)
		case bool:
			t.Setenv(k, strconv.FormatBool(val))
		case int:
			t.Setenv(k, strconv.Itoa(val))
		default:
			require.FailNow(t, "unsupported env var type", "key=%s type=%T", k, v)
		}
	}
}

func setFlag(t *testing.T, fs *pflag.FlagSet, name string, value any) {
	t.Helper()
	if fs.Lookup(name) == nil {
		require.FailNow(t, "unknown flag", "flag=%s", name)
	}
	switch v := value.(type) {
	case string:
		require.NoError(t, fs.Set(name, v))
	case int:
		require.NoError(t, fs.Set(name, strconv.Itoa(v)))
	case float64:
		require.NoError(t, fs.Set(name, fmt.Sprintf("%v", v)))
	case bool:
		require.NoError(t, fs.Set(name, strconv.FormatBool(v)))
	case *string:
		require.NoError(t, fs.Set(name, *v))
	case *[]string:
		require.NoError(t, fs.Set(name, strings.Join(*v, ",")))
	case []string:
		require.NoError(t, fs.Set(name, strings.Join(v, ",")))
	default:
		require.FailNow(t, "unsupported flag type", "flag=%s type=%T", name, value)
	}
}

// calculateURL calculates decoder URL
func calculateURL(t *testing.T, useTLSForDecoder bool, vllmport string) *url.URL {
	expectedScheme := "http"
	if useTLSForDecoder {
		expectedScheme = schemeHTTPS
	}
	expectedURL, err := url.Parse(expectedScheme + "://localhost:" + vllmport)
	require.NoError(t, err)
	return expectedURL
}

// compareSlices returns:
// 1. true when two slices contain same elements irrespective of order
// 2. false when two slices contain different elements and
// - what elements are missing in `got` slice compared to `expected` slice
// - what elements are extra in `got` slice compared to `expected` slice
func compareSlices(expected, got []string) (bool, []string, []string) {
	temp := make(map[string]int)
	var missing []string
	var extra []string
	if len(expected) == 0 && len(got) == 0 {
		return true, nil, nil
	}
	for _, v := range expected {
		temp[v]++
	}
	for _, v := range got {
		temp[v]--
	}
	for k, v := range temp {
		if v > 0 {
			for i := 0; i < v; i++ {
				missing = append(missing, k)
			}
		} else if v < 0 {
			for i := 0; i < -v; i++ {
				extra = append(extra, k)
			}
		}
	}
	return len(missing) == 0 && len(extra) == 0, missing, extra
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
			opts.connector = tt.connector
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
			opts.enableTLS = tt.enableTLS
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
			opts.inferencePool = tt.inferencePool

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
		expectedDecoderURL           string
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
			expectedDecoderURL:           "http://localhost:8001",
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
			expectedDecoderURL:           "http://localhost:8001",
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
			expectedDecoderURL:           "https://localhost:8001",
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
			expectedDecoderURL:           "https://localhost:9000",
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
			expectedDecoderURL:           "https://localhost:8001",
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
			expectedDecoderURL:           "https://localhost:8001",
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
			expectedDecoderURL:           "https://localhost:8001",
			expectedUseTLSForPrefiller:   true,
			expectedUseTLSForDecoder:     true,
			expectedInsecureForPrefiller: false,
			expectedInsecureForDecoder:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := NewOptions()
			opts.enableTLS = tt.enableTLS
			opts.tlsInsecureSkipVerify = tt.tlsInsecureSkipVerify
			opts.prefillerUseTLS = tt.deprecatedPrefillerUseTLS
			opts.decoderUseTLS = tt.deprecatedDecoderUseTLS
			opts.prefillerInsecureSkipVerify = tt.deprecatedPrefillerInsecure
			opts.decoderInsecureSkipVerify = tt.deprecatedDecoderInsecure
			opts.vllmPort = tt.vllmPort

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
			if opts.DecoderURL == nil || opts.DecoderURL.String() != tt.expectedDecoderURL {
				t.Errorf("TargetURL = %v, want %v", opts.DecoderURL, tt.expectedDecoderURL)
			}

		})
	}
}
