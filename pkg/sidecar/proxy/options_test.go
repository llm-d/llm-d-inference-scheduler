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
			err := opts.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompleteDeprecatedFlags(t *testing.T) {
	opts := NewOptions()
	opts.PrefillerUseTLS = true
	opts.DecoderUseTLS = true

	err := opts.Complete()
	if err != nil {
		t.Errorf("Complete() unexpected error: %v", err)
	}

	if !containsStage(opts.EnableTLS, "prefiller") {
		t.Error("Expected 'prefiller' to be in EnableTLS after Complete()")
	}
	if !containsStage(opts.EnableTLS, "decoder") {
		t.Error("Expected 'decoder' to be in EnableTLS after Complete()")
	}
}

func TestCompleteDeprecatedInsecureFlags(t *testing.T) {
	opts := NewOptions()
	opts.PrefillerInsecureSkipVerify = true
	opts.DecoderInsecureSkipVerify = true

	err := opts.Complete()
	if err != nil {
		t.Errorf("Complete() unexpected error: %v", err)
	}

	if !containsStage(opts.TLSInsecureSkipVerify, "prefiller") {
		t.Error("Expected 'prefiller' to be in TLSInsecureSkipVerify after Complete()")
	}
	if !containsStage(opts.TLSInsecureSkipVerify, "decoder") {
		t.Error("Expected 'decoder' to be in TLSInsecureSkipVerify after Complete()")
	}
}

func TestGetPrefillerUseTLS(t *testing.T) {
	opts := NewOptions()
	opts.EnableTLS = []string{"prefiller"}

	if !opts.GetPrefillerUseTLS() {
		t.Error("Expected GetPrefillerUseTLS() to return true")
	}

	opts.EnableTLS = []string{"decoder"}
	if opts.GetPrefillerUseTLS() {
		t.Error("Expected GetPrefillerUseTLS() to return false")
	}
}

func TestGetDecoderUseTLS(t *testing.T) {
	opts := NewOptions()
	opts.EnableTLS = []string{"decoder"}

	if !opts.GetDecoderUseTLS() {
		t.Error("Expected GetDecoderUseTLS() to return true")
	}

	opts.EnableTLS = []string{"prefiller"}
	if opts.GetDecoderUseTLS() {
		t.Error("Expected GetDecoderUseTLS() to return false")
	}
}

func TestContainsStage(t *testing.T) {
	stages := []string{"prefiller", "decoder"}

	if !containsStage(stages, "prefiller") {
		t.Error("Expected containsStage to find 'prefiller'")
	}
	if !containsStage(stages, "decoder") {
		t.Error("Expected containsStage to find 'decoder'")
	}
	if containsStage(stages, "invalid") {
		t.Error("Expected containsStage to not find 'invalid'")
	}
}
