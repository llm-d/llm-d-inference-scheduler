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

// createValidSidecarConfiguration creates file with valid YAML
func createValidSidecarConfiguration(t *testing.T) (string, error) {
	t.Helper()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "valid.yaml")
	content := []byte(`
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
cert-path: "/etc/certificatesFromFile1"
inference-pool: "fileNamespace1/fileName1"
pool-group: "filePoolgroup1"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// createPartialSidecarConfiguration creates file with valid YAML
func createPartialSidecarConfiguration(t *testing.T) (string, error) {
	t.Helper()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "valid.yaml")
	content := []byte(`
data-parallel-size: 5
kv-connector: "sglang"
ec-connector: "ec-example"
enable-ssrf-protection: true
enable-tls:
- prefiller
- decoder
prefiller-use-tls: false
tls-insecure-skip-verify:
- prefiller
decoder-tls-insecure-skip-verify: true
secure-proxy: false
cert-path: "/etc/certificatesFromFile2"
inference-pool: "fileNamespace2/fileName2"
pool-group: "filePoolgroup2"
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// createInvalidSidecarConfiguration creates file with invalid YAML
func createInvalidSidecarConfiguration(t *testing.T) (string, error) {
	t.Helper()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "invalid.yaml")
	content := []byte(`
port: 8100
vllm-port: 8200
*&&&&&&#######!!
`)
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// createRecursiveSidecarConfiguration creates YAML file with `configuration-file` key to test handling of recursive `configuration-file` key
func createRecursiveSidecarConfiguration(t *testing.T) (string, error) {
	t.Helper()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "recursive.yaml")
	content := fmt.Sprintf("%v: %v", configurationFile, path)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// createBogusSidecarConfiguration creates YAML file with bogus key-value pair but valid YAML
func createBogusSidecarConfiguration(t *testing.T) (string, error) {
	t.Helper()
	tempDir := t.TempDir()
	path := filepath.Join(tempDir, "bogus.yaml")
	content := "{abc: xyz}"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", err
	}
	return path, nil
}

// newTestOptions creates:
// 1. new flag set for test
// 2. new Options struct initialized with default values
func newTestOptions(t *testing.T) (*Options, *pflag.FlagSet) {
	flag.CommandLine = flag.NewFlagSet(os.Args[0], flag.ContinueOnError)
	testFlagSet := pflag.NewFlagSet(t.Name(), pflag.ContinueOnError)
	opts := NewOptions()
	opts.AddFlags(testFlagSet)
	opts.FlagSet = testFlagSet
	return opts, testFlagSet
}

// TestSidecarConfiguration tests following cases:
// Case 1: No sidecar configuration provided by user i.e. default configuration is used.
// Case 2: Configuration provided individually through flags (e.g `--port`, `--vllm-port`). Configuration from flags over-ride default configuration.
// Case 3: Configuration provided through environment variables: `INFERENCE_POOL`, `INFERENCE_POOL_NAMESPACE` and `INFERENCE_POOL_NAME`. `INFERENCE_POOL` has higher priority over `INFERENCE_POOL_NAMESPACE and `INFERENCE_POOL_NAME`. Configuration from environment variables over-ride default configuration.
// Case 4: YAML configuration provided through inline specification `--configuration`. Configuration from YAML inline specification over-ride default configuration.
// Case 5: YAML configuration provided through file `--configuration-file`. Configuration from YAML file over-ride default configuration.
// Case 6: Case 2 + Case 3: Configuration provided through individual flags + environment variables. Configuration from individual flags over-ride configuration from environment variable.
// Case 7: Configuration provided through environment variables: `INFERENCE_POOL`, `INFERENCE_POOL_NAMESPACE` and `INFERENCE_POOL_NAME. Environment variable `INFERENCE_POOL` has higher priority over individual flags `--inference-pool-namespace` and `--inference-pool-name`.
// Case 8: Case 2 + Case 4: Configuration provided through individual flags + YAML inline specification. Configuration from individual flags over-ride configuration from inline specification.
// Case 9: Case 2 + Case 5: Configuration provided through individual flags + file. Configuration from individual flags over-ride configuration from file.
// Case 10: Case 3 + Case 4: Configuration provided through environment variable + YAML inline specification. Configuration from environment variable over-rides configuration from inline specification. Rest of the configuration is picked up from YAML inline specification.
// Case 11: Case 3 + case 4: Configuration provided through environment variable + file. Configuration from environment variable over-rides configuration from inline specification. Rest of the configuration is picked up from file.
// Case 12: Case 4 + Case 5: Configuration provided through YAML inline specification + file. YAML inline specification over-rides values from file.
// Case 13: Case 2 + Case 4 + Case 5: Configuration provided through individual flags + YAML inline specification + file. Individual flags have highest priority, then inline specification, then file.
// Case 14: Case 3 + Case 4 + Case 5 i.e. configuration provided through environment variable + YAML inline specification + file. Configuration from environment variable over-rides configuration from inline specification. Configuration from inline specification over-rides configuration from file.
// Case 15: Case 2 + Case 3 + Case 4 + Case 5 i.e. configuration provided through individual flags + environment variable + YAML inline specification + file. Configuration from Individual flags over-ride configuration from everything else.
// Case 16: Case 2 + Case 3 + Case 4 + Case 5 i.e. configuration provided through individual flags + environment variable + YAML inline specification + file. Configuration is picked up from different sources based on priority.
// Case 17: Invalid YAML configuration provided through inline specification `--configuration`. Error is expected.
// Case 18: Invalid YAML configuration provided through file `--configuration-file`. Error is expected.
// Case 19: Recursive YAML configuration through inline YAML configuration `--configuration`. Recursive YAML configuration through inline specification is not considered. No error. Default configuration is used.
// Case 20: Recursive YAML configuration through file `--configuration-file`. Recursive YAML configuration through file is not considered. No error. Default configuration is used.
// Case 21: Bogus key-value pair but valid YAML from inline specification. Bogus pair through inline specification is ignored. No error. Configuration should be picked up based on priority. In this case, default configuration is used.
// Case 22: Bogus key-value pair but valid YAML from file. Bogus pair through file is ignored. No error. Configuration should be picked up based on priority. In this case, default configuration is used.
func TestSidecarConfiguration(t *testing.T) {
	expectedError := errors.New("failed to unmarshal sidecar configuration")

	// InlineYAMLToOverrideDefaults to check that inline specification overrides default values
	InlineYAMLToOverrideDefaults :=
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
		cert-path: '/etc/certificatesFromInline1',
		inference-pool: inlineNamespace1/inlineInferencepool1,
		pool-group: inlinePoolgroup1
	}`

	InlineYAMLWithPartialConfiguration :=
		`{
		kv-connector: shared-storage,
		tls-insecure-skip-verify: ['encoder'],
		decoder-tls-insecure-skip-verify: true,
		secure-proxy: false,
		inference-pool: inlineNamespace2/inlineInferencepool2,
	}`

	// InlineYAMLToNotOverrideFlags to check that inline specification does not override values from individual flags
	InlineYAMLToNotOverrideFlags :=
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
		cert-path: '/etc/certificatesFromInline2',
		inference-pool: inlineNamespace2/inlineInferencepool2,
		pool-group: inlinePoolgroup2
	}`

	invalidInlineYAML := "{port: 8200, vllm-port: 'sh'"             // Invalid YAML for inline specification
	recursiveYAML := fmt.Sprintf("%v: {port: 8002}", configuration) // YAML with `configuration` key for recursive test
	bogusPairButValidInlineYAML := "{abc: xyz}"                     // Bogus key-value pair but valid YAML

	validPath, err := createValidSidecarConfiguration(t)
	if err != nil {
		t.Fatalf("failed to create file to test valid sidecar configuration: %v", err)
	}
	partialPath, err := createPartialSidecarConfiguration(t)
	if err != nil {
		t.Fatalf("failed to create file to test valid sidecar configuration: %v", err)
	}
	invalidPath, err := createInvalidSidecarConfiguration(t)
	if err != nil {
		t.Fatalf("failed to create file to test invalid sidecar configuration: %v", err)
	}
	recursivePath, err := createRecursiveSidecarConfiguration(t)
	if err != nil {
		t.Fatalf("failed to create file to test recursive sidecar configuration: %v", err)
	}
	bogusPath, err := createBogusSidecarConfiguration(t)
	if err != nil {
		t.Fatalf("failed to create file to test bogus key-value pair but valid YAML for sidecar configuration: %v", err)
	}

	tests := []struct {
		name                                         string
		expectedPort                                 string
		expectedVLLMPort                             string
		expectedDataParallelSize                     int
		expectedKVConnector                          string
		expectedECConnector                          string
		expectedSecureServing                        bool
		expectedEnableSSRFProtection                 bool
		expectedEnablePrefillerSampling              bool
		expectedEnableTLS                            []string
		expectedUseTLSForPrefiller                   bool
		expectedUseTLSForDecoder                     bool
		expectedUseTLSForEncoder                     bool
		expectedTLSInsecureSkipVerify                []string
		expectedPrefillerInsecureSkipVerify          bool
		expectedDecoderInsecureSkipVerify            bool
		expectedEncoderInsecureSkipVerify            bool
		expectedCertPath                             string
		expectedInferencePool                        string
		expectedInferencePoolNamespace               string
		expectedInferencePoolName                    string
		expectedPoolGroup                            string
		expectedConfigurationFromInlineSpecification string
		expectedConfigurationFromFile                string
		expectedConfigurationState                   struct {
			FromEnv    []string
			FromFlags  []string
			FromInline []string
			FromFile   []string
			Defaults   []string
		}
		expectedError error
		inputFlags    map[string]any
		inputEnvVar   map[string]any
	}{
		{
			name:                                         "Case 1: No sidecar configuration provided by user i.e. default configuration is used.",
			inputFlags:                                   nil,
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
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
					poolGroup},
			},
			expectedError: nil,
		},
		{
			name: "Case 2: Configuration provided individually through flags (e.g `--port`, `--vllm-port`). Configuration from flags over-ride default configuration.",
			inputFlags: map[string]any{
				port:                    "8001",
				vllmPort:                "8002",
				dataParallelSize:        2,
				kvConnector:             KVConnectorSGLang,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificatesForCase2",
				inferencePool:           "namespaceForCase2/nameForCase2",
				inferencePoolNamespace:  "ignoredNamespaceForCase3FromFlag",
				inferencePoolName:       "ignoredNameForCase3FromFlag",
				poolGroup:               "poolgroupForCase2",
			},
			expectedPort:                                 "8001",
			expectedVLLMPort:                             "8002",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase2",
			expectedInferencePool:                        "namespaceForCase2/nameForCase2",
			expectedInferencePoolNamespace:               "namespaceForCase2",
			expectedInferencePoolName:                    "nameForCase2",
			expectedPoolGroup:                            "poolgroupForCase2",
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					inferencePoolNamespace,
					inferencePoolName,
					poolGroup,
				},
			},
			expectedError: nil,
		},
		{
			name:       "Case 3: Configuration provided through environment variables: `INFERENCE_POOL`, `INFERENCE_POOL_NAMESPACE` and `INFERENCE_POOL_NAME`. `INFERENCE_POOL` has higher priority over `INFERENCE_POOL_NAMESPACE and `INFERENCE_POOL_NAME`. Configuration from environment variables over-ride default configuration.",
			inputFlags: nil,
			inputEnvVar: map[string]any{
				envInferencePool:           "namespaceForCase3/nameForCase3",
				envInferencePoolNamespace:  "ignoredNamespaceForCase3",
				envInferencePoolName:       "ignoredNameForCase3",
				envEnablePrefillerSampling: true,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedInferencePool:                        "namespaceForCase3/nameForCase3",
			expectedInferencePoolNamespace:               "namespaceForCase3",
			expectedInferencePoolName:                    "nameForCase3",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromEnv: []string{
					inferencePool,
					inferencePoolNamespace,
					inferencePoolName,
					enablePrefillerSampling,
				},
				Defaults: []string{
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
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 4: YAML configuration provided through inline specification `--configuration`. Configuration from YAML inline specification over-ride default configuration.",
			inputFlags: map[string]any{
				configuration: &InlineYAMLToOverrideDefaults,
			},
			expectedPort:                                 "8011",
			expectedVLLMPort:                             "8021",
			expectedDataParallelSize:                     3,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromInline1",
			expectedInferencePool:                        "inlineNamespace1/inlineInferencepool1",
			expectedInferencePoolNamespace:               "inlineNamespace1",
			expectedInferencePoolName:                    "inlineInferencepool1",
			expectedPoolGroup:                            "inlinePoolgroup1",
			expectedConfigurationFromInlineSpecification: InlineYAMLToOverrideDefaults,
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromInline: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					prefillerUseTLS,
					tlsInsecureSkipVerify,
					prefillerTLSInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 5: YAML configuration provided through file `--configuration-file`. Configuration from YAML file over-ride default configuration.",
			inputFlags: map[string]any{
				configurationFile: validPath,
			},
			expectedPort:                                 "8100",
			expectedVLLMPort:                             "8200",
			expectedDataParallelSize:                     5,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromFile1",
			expectedInferencePool:                        "fileNamespace1/fileName1",
			expectedInferencePoolNamespace:               "fileNamespace1",
			expectedInferencePoolName:                    "fileName1",
			expectedPoolGroup:                            "filePoolgroup1",
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFile: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					prefillerUseTLS,
					tlsInsecureSkipVerify,
					decoderTLSInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 6: Case 2 + Case 3: Configuration provided through individual flags + environment variables. Configuration from individual flags over-ride  configuration from environment variable.",
			inputFlags: map[string]any{
				port:             "8100",
				vllmPort:         "8200",
				dataParallelSize: 2,
				// NOTE: value of `kvConnector` flag is same as default value to show ConfigurationState.FromFlag should hold this value and not ConfigurationState.Defaults
				kvConnector:             KVConnectorSharedStorage,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificatesForCase7",
				inferencePool:           "namespaceForCase7/nameForCase7",
				poolGroup:               "poolgroupForCase7",
			},
			inputEnvVar: map[string]any{
				"INFERENCE_POOL":           "ignoredNamespaceForCase7FromEnvVar/ignoredInferencepoolForCase7FromEnvVar",
				"INFERENCE_POOL_NAMESPACE": "ignoredInferenceNamespaceForCase7",
				"INFERENCE_POOL_NAME":      "ignoredInferenceNameForCase7",
			},
			expectedPort:                                 "8100",
			expectedVLLMPort:                             "8200",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorSharedStorage,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase7",
			expectedInferencePool:                        "namespaceForCase7/nameForCase7",
			expectedInferencePoolNamespace:               "namespaceForCase7",
			expectedInferencePoolName:                    "nameForCase7",
			expectedPoolGroup:                            "poolgroupForCase7",
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
				FromEnv: []string{
					inferencePoolNamespace,
					inferencePoolName,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 7: Configuration provided through environment variables: `INFERENCE_POOL`, `INFERENCE_POOL_NAMESPACE` and `INFERENCE_POOL_NAME. Environment variable `INFERENCE_POOL` has higher priority over individual flags `--inference-pool-namespace` and `--inference-pool-name`.",
			inputFlags: map[string]any{
				port:                    "8001",
				vllmPort:                "8002",
				dataParallelSize:        2,
				kvConnector:             KVConnectorSGLang,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificatesForCase4",
				inferencePoolNamespace:  "namespaceForCase7",
				inferencePoolName:       "nameForCase7",
				poolGroup:               "poolgroupForCase4",
			},
			inputEnvVar: map[string]any{
				envInferencePool:          "inferencePoolFromEnvVar",
				envInferencePoolNamespace: "ignoredInferencePoolNamespaceFromEnvVar",
				envInferencePoolName:      "ignoredInferencePoolNameFromEnvVar",
			},
			expectedPort:                                 "8001",
			expectedVLLMPort:                             "8002",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase4",
			expectedInferencePool:                        "inferencePoolFromEnvVar",
			expectedInferencePoolNamespace:               "default",
			expectedInferencePoolName:                    "inferencePoolFromEnvVar",
			expectedPoolGroup:                            "poolgroupForCase4",
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePoolNamespace,
					inferencePoolName,
					poolGroup,
				},
				FromEnv: []string{
					inferencePool,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 8: Case 2 + Case 4: Configuration provided through individual flags + YAML inline specification. Configuration from individual flags over-ride configuration from inline specification.",
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
				certPath:                "/etc/certificatesForCase8",
				inferencePool:           "namespaceForCase8/nameForCase8",
				poolGroup:               "poolgroupForCase8",
				configuration:           InlineYAMLToNotOverrideFlags,
			},
			expectedPort:                                 "8100",
			expectedVLLMPort:                             "8200",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase8",
			expectedInferencePool:                        "namespaceForCase8/nameForCase8",
			expectedInferencePoolNamespace:               "namespaceForCase8",
			expectedInferencePoolName:                    "nameForCase8",
			expectedPoolGroup:                            "poolgroupForCase8",
			expectedConfigurationFromInlineSpecification: InlineYAMLToNotOverrideFlags,
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
				FromInline: []string{
					prefillerUseTLS,
					prefillerTLSInsecureSkipVerify,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 9: Case 2 + Case 5: Configuration provided through individual flags + file. Configuration from individual flags over-ride configuration from file.",
			inputFlags: map[string]any{
				port:                    "8001",
				vllmPort:                "8002",
				dataParallelSize:        2,
				kvConnector:             KVConnectorNIXLV2,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{},
				tlsInsecureSkipVerify:   &[]string{prefillStage, decodeStage, encodeStage},
				secureServing:           false,
				certPath:                "/etc/certificatesForCase9",
				inferencePool:           "namespaceForCase9/nameForCase9",
				poolGroup:               "poolgroupForCase9",
				configurationFile:       validPath,
			},
			expectedPort:                                 "8001",
			expectedVLLMPort:                             "8002",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{},
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage, encodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            true,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase9",
			expectedInferencePool:                        "namespaceForCase9/nameForCase9",
			expectedInferencePoolNamespace:               "namespaceForCase9",
			expectedInferencePoolName:                    "nameForCase9",
			expectedPoolGroup:                            "poolgroupForCase9",
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
				FromFile: []string{
					prefillerUseTLS,
					decoderTLSInsecureSkipVerify,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 10: Case 3 + Case 4: Configuration provided through environment variable + YAML inline specification. Configuration from environment variable over-rides configuration from inline specification. Rest of the configuration is picked up from YAML inline specification",
			inputFlags: map[string]any{
				configuration: InlineYAMLToOverrideDefaults,
			},
			inputEnvVar: map[string]any{
				envInferencePool:           "inferencePoolNamespace1/inferencePoolName1",
				envInferencePoolNamespace:  "inferencePoolNamespace2",
				envInferencePoolName:       "inferencePoolName2",
				envEnablePrefillerSampling: false,
			},
			expectedPort:                                 "8011",
			expectedVLLMPort:                             "8021",
			expectedDataParallelSize:                     3,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromInline1",
			expectedInferencePool:                        "inferencePoolNamespace1/inferencePoolName1",
			expectedInferencePoolNamespace:               "inferencePoolNamespace1",
			expectedInferencePoolName:                    "inferencePoolName1",
			expectedPoolGroup:                            "inlinePoolgroup1",
			expectedConfigurationFromInlineSpecification: InlineYAMLToOverrideDefaults,
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromEnv: []string{
					inferencePool,
					inferencePoolNamespace,
					inferencePoolName,
					enablePrefillerSampling,
				},
				FromInline: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enableTLS,
					prefillerUseTLS,
					tlsInsecureSkipVerify,
					prefillerTLSInsecureSkipVerify,
					secureServing,
					certPath,
					poolGroup,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 11: Case 3 + case 4: Configuration provided through environment variable + file. Configuration from environment variable over-rides configuration from inline specification. Rest of the configuration is picked up from file.",
			inputFlags: map[string]any{
				configurationFile: validPath,
			},
			inputEnvVar: map[string]any{
				envInferencePool:           "namespaceForCase11/inferencepoolForCase11",
				envInferencePoolNamespace:  "ignoredInferenceNamespaceForCase3",
				envInferencePoolName:       "ignoredInferenceNameForCase3",
				envEnablePrefillerSampling: false,
			},
			expectedPort:                                 "8100",
			expectedVLLMPort:                             "8200",
			expectedDataParallelSize:                     5,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromFile1",
			expectedInferencePool:                        "namespaceForCase11/inferencepoolForCase11",
			expectedInferencePoolNamespace:               "namespaceForCase11",
			expectedInferencePoolName:                    "inferencepoolForCase11",
			expectedPoolGroup:                            "filePoolgroup1",
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromEnv: []string{
					inferencePool,
					inferencePoolNamespace,
					inferencePoolName,
					enablePrefillerSampling,
				},
				FromFile: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enableTLS,
					prefillerUseTLS,
					tlsInsecureSkipVerify,
					decoderTLSInsecureSkipVerify,
					secureServing,
					certPath,
					poolGroup,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 12: Case 4 + Case 5: Configuration provided through YAML inline specification + file. YAML inline specification over-rides values from file.",
			inputFlags: map[string]any{
				configuration:     InlineYAMLToOverrideDefaults,
				configurationFile: validPath,
			},
			expectedPort:                                 "8011",
			expectedVLLMPort:                             "8021",
			expectedDataParallelSize:                     3,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromInline1",
			expectedInferencePool:                        "inlineNamespace1/inlineInferencepool1",
			expectedInferencePoolNamespace:               "inlineNamespace1",
			expectedInferencePoolName:                    "inlineInferencepool1",
			expectedPoolGroup:                            "inlinePoolgroup1",
			expectedConfigurationFromInlineSpecification: InlineYAMLToOverrideDefaults,
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromInline: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					prefillerUseTLS,
					tlsInsecureSkipVerify,
					prefillerTLSInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
				FromFile: []string{
					"decoder-tls-insecure-skip-verify",
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 13: Case 2 + Case 4 + Case 5: Configuration provided through individual flags + YAML inline specification + file. Individual flags have highest priority, then inline specification, then file.",
			inputFlags: map[string]any{
				port:                    "8001",
				vllmPort:                "8002",
				dataParallelSize:        2,
				kvConnector:             KVConnectorSGLang,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificatesForCase2",
				inferencePool:           "namespaceForCase2/inferencepoolForCase2",
				poolGroup:               "poolgroupForCase2",
				configuration:           InlineYAMLToNotOverrideFlags,
				configurationFile:       validPath,
			},
			expectedPort:                                 "8001",
			expectedVLLMPort:                             "8002",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase2",
			expectedInferencePool:                        "namespaceForCase2/inferencepoolForCase2",
			expectedInferencePoolNamespace:               "namespaceForCase2",
			expectedInferencePoolName:                    "inferencepoolForCase2",
			expectedPoolGroup:                            "poolgroupForCase2",
			expectedConfigurationFromInlineSpecification: InlineYAMLToNotOverrideFlags,
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
				FromInline: []string{
					prefillerUseTLS,
					prefillerTLSInsecureSkipVerify,
				},
				FromFile: []string{
					decoderTLSInsecureSkipVerify,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 14: Case 3 + Case 4 + Case 5 i.e. configuration provided through environment variable + YAML inline specification + file. Configuration from environment variable over-rides configuration from inline specification. Configuration from inline specification over-rides configuration from file.",
			inputFlags: map[string]any{
				configuration:     InlineYAMLToOverrideDefaults,
				configurationFile: validPath,
			},
			inputEnvVar: map[string]any{
				envInferencePool:          "namespaceForCase14FromEnvVar/nameForCase14FromEnvVar",
				envInferencePoolNamespace: "ignoredInferenceNamespaceForCase14",
				envInferencePoolName:      "ignoredInferenceNameForCase14",
			},
			expectedPort:                                 "8011",
			expectedVLLMPort:                             "8021",
			expectedDataParallelSize:                     3,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromInline1",
			expectedInferencePool:                        "namespaceForCase14FromEnvVar/nameForCase14FromEnvVar",
			expectedInferencePoolNamespace:               "namespaceForCase14FromEnvVar",
			expectedInferencePoolName:                    "nameForCase14FromEnvVar",
			expectedPoolGroup:                            "inlinePoolgroup1",
			expectedConfigurationFromInlineSpecification: InlineYAMLToOverrideDefaults,
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromInline: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					poolGroup,
					prefillerUseTLS,
					prefillerTLSInsecureSkipVerify,
				},
				FromEnv: []string{
					inferencePool,
					inferencePoolNamespace,
					inferencePoolName,
				},
				FromFile: []string{
					decoderTLSInsecureSkipVerify,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 15: Case 2 + Case 3 + Case 4 + Case 5 i.e. configuration provided through individual flags + environment variable + YAML inline specification + file. Configuration from Individual flags over-ride configuration from everything else.",
			inputFlags: map[string]any{
				port:                    "8001",
				vllmPort:                "8002",
				dataParallelSize:        2,
				kvConnector:             KVConnectorSGLang,
				ecConnector:             ECExampleConnector,
				enableSSRFProtection:    true,
				enablePrefillerSampling: true,
				enableTLS:               &[]string{prefillStage},
				tlsInsecureSkipVerify:   &[]string{prefillStage},
				secureServing:           false,
				certPath:                "/etc/certificatesForCase14",
				inferencePool:           "namespaceForCase14/nameForCase14",
				poolGroup:               "poolgroupForCase14",
				configuration:           InlineYAMLToOverrideDefaults,
				configurationFile:       validPath,
			},
			inputEnvVar: map[string]any{
				envInferencePool:          "ignoredNamespaceForCase14FromEnvVar/ignoredNameForCase14FromEnvVar",
				envInferencePoolNamespace: "ignoredInferenceNamespaceForCase14",
				envInferencePoolName:      "ignoredInferenceNameForCase14",
			},
			expectedPort:                                 "8001",
			expectedVLLMPort:                             "8002",
			expectedDataParallelSize:                     2,
			expectedKVConnector:                          KVConnectorSGLang,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{prefillStage, decodeStage},
			expectedPrefillerInsecureSkipVerify:          true,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesForCase14",
			expectedInferencePool:                        "namespaceForCase14/nameForCase14",
			expectedInferencePoolNamespace:               "namespaceForCase14",
			expectedInferencePoolName:                    "nameForCase14",
			expectedPoolGroup:                            "poolgroupForCase14",
			expectedConfigurationFromInlineSpecification: InlineYAMLToOverrideDefaults,
			expectedConfigurationFromFile:                validPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					inferencePool,
					poolGroup,
				},
				FromEnv: []string{
					inferencePoolNamespace,
					inferencePoolName,
				},
				FromInline: []string{
					prefillerUseTLS,
					prefillerTLSInsecureSkipVerify,
				},
				FromFile: []string{
					decoderTLSInsecureSkipVerify,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 16: Case 2 + Case 3 + Case 4 + Case 5 i.e. configuration provided through individual flags + environment variable + YAML inline specification + file. Configuration is picked up from different sources based on priority.",
			inputFlags: map[string]any{
				port:              "8001",
				vllmPort:          "8002",
				configuration:     InlineYAMLWithPartialConfiguration,
				configurationFile: partialPath,
			},
			inputEnvVar: map[string]any{
				envInferencePool:           "namespaceForCase14FromEnvVar/nameForCase14FromEnvVar",
				envInferencePoolNamespace:  "ignoredInferenceNamespaceForCase14",
				envInferencePoolName:       "ignoredInferenceNameForCase14",
				envEnablePrefillerSampling: true,
			},
			expectedPort:                                 "8001",
			expectedVLLMPort:                             "8002",
			expectedDataParallelSize:                     5,
			expectedKVConnector:                          KVConnectorSharedStorage,
			expectedECConnector:                          ECExampleConnector,
			expectedEnableSSRFProtection:                 true,
			expectedEnablePrefillerSampling:              true,
			expectedEnableTLS:                            []string{prefillStage, decodeStage},
			expectedUseTLSForPrefiller:                   true,
			expectedUseTLSForDecoder:                     true,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                []string{decodeStage, encodeStage},
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            true,
			expectedEncoderInsecureSkipVerify:            true,
			expectedSecureServing:                        false,
			expectedCertPath:                             "/etc/certificatesFromFile2",
			expectedInferencePool:                        "namespaceForCase14FromEnvVar/nameForCase14FromEnvVar",
			expectedInferencePoolNamespace:               "namespaceForCase14FromEnvVar",
			expectedInferencePoolName:                    "nameForCase14FromEnvVar",
			expectedPoolGroup:                            "filePoolgroup2",
			expectedConfigurationFromInlineSpecification: InlineYAMLWithPartialConfiguration,
			expectedConfigurationFromFile:                partialPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				FromFlags: []string{
					port,
					vllmPort,
				},
				FromEnv: []string{
					inferencePool,
					inferencePoolNamespace,
					inferencePoolName,
					enablePrefillerSampling,
				},
				FromInline: []string{
					kvConnector,
					secureServing,
					tlsInsecureSkipVerify,
					decoderTLSInsecureSkipVerify,
				},
				FromFile: []string{
					dataParallelSize,
					ecConnector,
					enableSSRFProtection,
					enableTLS,
					prefillerUseTLS,
					certPath,
					poolGroup,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 17: Invalid YAML configuration provided through inline specification `--configuration`. Error is expected.",
			inputFlags: map[string]any{
				configuration: invalidInlineYAML,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: invalidInlineYAML,
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					poolGroup},
			},
			expectedError: expectedError,
		},
		{
			name: "Case 18: Invalid YAML configuration provided through file `--configuration-file`. Error is expected.",
			inputFlags: map[string]any{
				configurationFile: invalidPath,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                invalidPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
					port,
					vllmPort,
					dataParallelSize,
					kvConnector,
					ecConnector,
					enableSSRFProtection,
					enablePrefillerSampling,
					enableTLS,
					tlsInsecureSkipVerify,
					secureServing,
					certPath,
					poolGroup},
			},
			expectedError: expectedError,
		},
		{
			name: "Case 19: Recursive YAML configuration through inline YAML configuration `--configuration`. Recursive YAML configuration through inline specification is not considered. No error. Default configuration is used.",
			inputFlags: map[string]any{
				configuration: &recursiveYAML,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: recursiveYAML,
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
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
				},
				FromInline: []string{
					configuration,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 20: Recursive YAML configuration through file `--configuration-file`. Recursive YAML configuration through file is not considered. No error. Default configuration is used.",
			inputFlags: map[string]any{
				configurationFile: recursivePath,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                recursivePath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
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
					poolGroup},
				FromFile: []string{
					configurationFile,
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 21: Bogus key-value pair but valid YAML from inline specification. Bogus pair through inline specification is ignored. No error. Configuration should be picked up based on priority. In this case, default configuration is used.",
			inputFlags: map[string]any{
				configuration: bogusPairButValidInlineYAML,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: bogusPairButValidInlineYAML,
			expectedConfigurationFromFile:                "",
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
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
					poolGroup},
				FromInline: []string{
					"abc",
				},
			},
			expectedError: nil,
		},
		{
			name: "Case 22: Bogus key-value pair but valid YAML from file. Bogus pair through file is ignored. No error. Configuration should be picked up based on priority. In this case, default configuration is used.",
			inputFlags: map[string]any{
				configurationFile: bogusPath,
			},
			expectedPort:                                 defaultPort,
			expectedVLLMPort:                             defaultVLLMPort,
			expectedDataParallelSize:                     defaultDataParallelSize,
			expectedKVConnector:                          KVConnectorNIXLV2,
			expectedECConnector:                          "",
			expectedEnableSSRFProtection:                 false,
			expectedEnablePrefillerSampling:              false,
			expectedEnableTLS:                            nil,
			expectedUseTLSForPrefiller:                   false,
			expectedUseTLSForDecoder:                     false,
			expectedUseTLSForEncoder:                     false,
			expectedTLSInsecureSkipVerify:                nil,
			expectedPrefillerInsecureSkipVerify:          false,
			expectedDecoderInsecureSkipVerify:            false,
			expectedEncoderInsecureSkipVerify:            false,
			expectedSecureServing:                        true,
			expectedCertPath:                             "",
			expectedInferencePool:                        "",
			expectedInferencePoolNamespace:               "",
			expectedInferencePoolName:                    "",
			expectedPoolGroup:                            DefaultPoolGroup,
			expectedConfigurationFromInlineSpecification: "",
			expectedConfigurationFromFile:                bogusPath,
			expectedConfigurationState: struct {
				FromEnv    []string
				FromFlags  []string
				FromInline []string
				FromFile   []string
				Defaults   []string
			}{
				Defaults: []string{
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
					poolGroup},
				FromFile: []string{
					"abc",
				},
			},
			expectedError: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for envVarKey, envVarValue := range tt.inputEnvVar {
				if envVarValue != nil {
					switch v := envVarValue.(type) {
					case string:
						t.Setenv(envVarKey, v)
					case bool:
						t.Setenv(envVarKey, strconv.FormatBool(v))
					default:
						t.Errorf("%v type is unknown, value: %v", envVarKey, envVarValue)
					}
				}
			}
			opts, testFlagSet := newTestOptions(t)
			for flagName, flagValue := range tt.inputFlags {
				if flagValue != nil {
					switch v := flagValue.(type) {
					case int:
						require.NoError(t, testFlagSet.Set(flagName, strconv.Itoa(v)))
					case float64:
						require.NoError(t, testFlagSet.Set(flagName, fmt.Sprintf("%v", v)))
					case string:
						require.NoError(t, testFlagSet.Set(flagName, v))
					case *string:
						require.NoError(t, testFlagSet.Set(flagName, *v))
					case *[]string:
						require.NoError(t, testFlagSet.Set(flagName, strings.Join(*v, ",")))
					case bool:
						require.NoError(t, testFlagSet.Set(flagName, strconv.FormatBool(v)))
					default:
						t.Errorf("%v type is unknown, value: %v", flagName, flagValue)
					}
				}
			}
			err := testFlagSet.Parse(nil)
			require.NoError(t, err, "Flag Parse() error: %v", err)
			opts.GetConfigurationState()
			err = opts.Complete()
			if tt.expectedError != nil {
				require.ErrorContains(t, err, tt.expectedError.Error(), "Error should be: %v, got: %v", tt.expectedError, err)
				return
			}
			require.NoError(t, err, "Complete() error: %v", err)
			err = opts.Validate()
			require.NoError(t, err, "Validate() error: %v", err)

			require.Equal(t, tt.expectedPort, opts.Port,
				"expected %v to be %v but got %v", port, tt.expectedPort, opts.Port)
			require.Equal(t, tt.expectedVLLMPort, opts.vllmPort,
				"expected %v to be %v but got %v", vllmPort, tt.expectedVLLMPort, opts.vllmPort)
			require.Equal(t, tt.expectedDataParallelSize, opts.DataParallelSize,
				"expected %v to be %v but got %v", dataParallelSize, tt.expectedDataParallelSize, opts.DataParallelSize)

			require.Equal(t, tt.expectedKVConnector, opts.KVConnector,
				"expected %v to be %v but got %v", kvConnector, tt.expectedKVConnector, opts.KVConnector)
			require.Equal(t, tt.expectedECConnector, opts.ECConnector,
				"expected %v to be %v but got %v", ecConnector, tt.expectedECConnector, opts.ECConnector)

			require.Equal(t, tt.expectedEnableSSRFProtection, opts.EnableSSRFProtection,
				"expected %v to be %v but got %v", enableSSRFProtection, tt.expectedEnableSSRFProtection, opts.EnableSSRFProtection)
			require.Equal(t, tt.expectedEnablePrefillerSampling, opts.EnablePrefillerSampling,
				"expected %v to be %v but got %v", enablePrefillerSampling, tt.expectedEnablePrefillerSampling, opts.EnablePrefillerSampling)

			require.Equal(t, tt.expectedUseTLSForPrefiller, opts.UseTLSForPrefiller,
				"expected %v to be %v but got %v", prefillerUseTLS, tt.expectedUseTLSForPrefiller, opts.UseTLSForPrefiller)
			require.Equal(t, tt.expectedUseTLSForDecoder, opts.UseTLSForDecoder,
				"expected %v to be %v but got %v", decoderUseTLS, tt.expectedUseTLSForDecoder, opts.UseTLSForDecoder)
			require.Equal(t, tt.expectedUseTLSForEncoder, opts.UseTLSForEncoder,
				"expected UseTLSForEncoder to be %v but got %v", tt.expectedUseTLSForEncoder, opts.UseTLSForEncoder)

			require.Equal(t, tt.expectedPrefillerInsecureSkipVerify, opts.InsecureSkipVerifyForPrefiller,
				"expected %v to be %v but got %v", prefillerTLSInsecureSkipVerify, tt.expectedPrefillerInsecureSkipVerify, opts.InsecureSkipVerifyForPrefiller)
			require.Equal(t, tt.expectedDecoderInsecureSkipVerify, opts.InsecureSkipVerifyForDecoder,
				"expected %v to be %v but got %v", decoderTLSInsecureSkipVerify, tt.expectedDecoderInsecureSkipVerify, opts.InsecureSkipVerifyForDecoder)
			require.Equal(t, tt.expectedEncoderInsecureSkipVerify, opts.InsecureSkipVerifyForEncoder,
				"expected InsecureSkipVerifyForEncoder to be %v but got %v", tt.expectedEncoderInsecureSkipVerify, opts.InsecureSkipVerifyForEncoder)

			ok, missing, extra := compareSlices(tt.expectedEnableTLS, opts.enableTLS)
			require.True(t, ok,
				"%v mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", enableTLS, tt.expectedEnableTLS, opts.enableTLS, extra, missing)

			ok, missing, extra = compareSlices(tt.expectedTLSInsecureSkipVerify, opts.tlsInsecureSkipVerify)
			require.True(t, ok,
				"%v mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", tlsInsecureSkipVerify, tt.expectedTLSInsecureSkipVerify, opts.tlsInsecureSkipVerify, extra, missing)

			require.Equal(t, tt.expectedCertPath, opts.CertPath,
				"expected %v to be %v but got %v", certPath, tt.expectedCertPath, opts.CertPath)
			require.Equal(t, tt.expectedSecureServing, opts.SecureServing,
				"expected %v to be %v but got %v", secureServing, tt.expectedSecureServing, opts.SecureServing)

			require.Equal(t, tt.expectedInferencePool, opts.inferencePool,
				"expected %v to be %v but got %v", inferencePool, tt.expectedInferencePool, opts.inferencePool)
			require.Equal(t, tt.expectedInferencePoolNamespace, opts.InferencePoolNamespace,
				"expected %v to be %v but got %v", inferencePoolNamespace, tt.expectedInferencePoolNamespace, opts.InferencePoolNamespace)
			require.Equal(t, tt.expectedInferencePoolName, opts.InferencePoolName,
				"expected %v to be %v but got %v", inferencePoolName, tt.expectedInferencePoolName, opts.InferencePoolName)

			require.Equal(t, tt.expectedPoolGroup, opts.PoolGroup,
				"expected %v to be %v but got %v", poolGroup, tt.expectedPoolGroup, opts.PoolGroup)

			require.Equal(t, tt.expectedConfigurationFromInlineSpecification, opts.configurationFromInlineSpecification,
				"expected configuration from inline specification to be %v but got %v", tt.expectedConfigurationFromInlineSpecification, opts.configurationFromInlineSpecification)
			require.Equal(t, tt.expectedConfigurationFromFile, opts.configurationFromFile,
				"expected configuration from file to be %v but got %v", tt.expectedConfigurationFromFile, opts.configurationFromFile)

			ok, missing, extra = compareSlices(tt.expectedConfigurationState.Defaults, opts.ConfigurationState.Defaults)
			require.True(t, ok,
				"ConfigurationState.Defaults mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", tt.expectedConfigurationState.Defaults, opts.ConfigurationState.Defaults, extra, missing)

			ok, missing, extra = compareSlices(tt.expectedConfigurationState.FromFlags, opts.ConfigurationState.FromFlags)
			require.True(t, ok,
				"ConfigurationState.FromFlags mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", tt.expectedConfigurationState.FromFlags, opts.ConfigurationState.FromFlags, extra, missing)

			ok, missing, extra = compareSlices(tt.expectedConfigurationState.FromEnv, opts.ConfigurationState.FromEnv)
			require.True(t, ok,
				"ConfigurationState.FromEnv mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", tt.expectedConfigurationState.FromEnv, opts.ConfigurationState.FromEnv, extra, missing)

			ok, missing, extra = compareSlices(tt.expectedConfigurationState.FromInline, opts.ConfigurationState.FromInline)
			require.True(t, ok,
				"ConfigurationState.FromInline mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", tt.expectedConfigurationState.FromInline, opts.ConfigurationState.FromInline, extra, missing)

			ok, missing, extra = compareSlices(tt.expectedConfigurationState.FromFile, opts.ConfigurationState.FromFile)
			require.True(t, ok,
				"ConfigurationState.FromFile mismatch:\nexpected: %v\ngot %v\nextra %v\nmissing %v\n", tt.expectedConfigurationState.FromFile, opts.ConfigurationState.FromFile, extra, missing)

			require.Equal(t, calculateURL(t, tt.expectedUseTLSForDecoder, tt.expectedVLLMPort), opts.DecoderURL)
		})
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
