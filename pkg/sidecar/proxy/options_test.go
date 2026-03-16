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
	"testing"
)

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
