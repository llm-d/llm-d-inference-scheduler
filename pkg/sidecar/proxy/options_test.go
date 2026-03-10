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
	"os"
	"testing"

	"github.com/spf13/pflag"
)

func TestNewOptions(t *testing.T) {
	opts := NewOptions()

	if opts.Port != "8000" {
		t.Errorf("Expected Port to be '8000', got '%s'", opts.Port)
	}
	if opts.VLLMPort != "8001" {
		t.Errorf("Expected VLLMPort to be '8001', got '%s'", opts.VLLMPort)
	}
	if opts.DataParallelSize != 1 {
		t.Errorf("Expected DataParallelSize to be 1, got %d", opts.DataParallelSize)
	}
	if opts.Connector != KVConnectorNIXLV2 {
		t.Errorf("Expected Connector to be '%s', got '%s'", KVConnectorNIXLV2, opts.Connector)
	}
	if !opts.SecureProxy {
		t.Error("Expected SecureProxy to be true")
	}
	if opts.PoolGroup != DefaultPoolGroup {
		t.Errorf("Expected PoolGroup to be '%s', got '%s'", DefaultPoolGroup, opts.PoolGroup)
	}
}

func TestNewOptionsWithEnvVars(t *testing.T) {
	// Set environment variables
	if err := os.Setenv("INFERENCE_POOL_NAMESPACE", "test-namespace"); err != nil {
		t.Fatalf("Failed to set INFERENCE_POOL_NAMESPACE: %v", err)
	}
	if err := os.Setenv("INFERENCE_POOL_NAME", "test-pool"); err != nil {
		t.Fatalf("Failed to set INFERENCE_POOL_NAME: %v", err)
	}
	if err := os.Setenv("ENABLE_PREFILLER_SAMPLING", "true"); err != nil {
		t.Fatalf("Failed to set ENABLE_PREFILLER_SAMPLING: %v", err)
	}
	defer func() {
		_ = os.Unsetenv("INFERENCE_POOL_NAMESPACE")
		_ = os.Unsetenv("INFERENCE_POOL_NAME")
		_ = os.Unsetenv("ENABLE_PREFILLER_SAMPLING")
	}()

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

func TestAddFlags(t *testing.T) {
	opts := NewOptions()
	fs := pflag.NewFlagSet("test", pflag.ContinueOnError)
	opts.AddFlags(fs)

	// Test that flags are registered
	if fs.Lookup("port") == nil {
		t.Error("Expected 'port' flag to be registered")
	}
	if fs.Lookup("connector") == nil {
		t.Error("Expected 'connector' flag to be registered")
	}
	if fs.Lookup("enable-tls") == nil {
		t.Error("Expected 'enable-tls' flag to be registered")
	}
	if fs.Lookup("tls-insecure-skip-verify") == nil {
		t.Error("Expected 'tls-insecure-skip-verify' flag to be registered")
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
		{"valid prefiller", []string{"prefiller"}, false},
		{"valid decoder", []string{"decoder"}, false},
		{"valid both", []string{"prefiller", "decoder"}, false},
		{"invalid stage", []string{"invalid"}, true},
		{"mixed valid and invalid", []string{"prefiller", "invalid"}, true},
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
		{"disabled", false, "", "", false},
		{"enabled with both", true, "ns", "pool", false},
		{"enabled missing namespace", true, "", "pool", true},
		{"enabled missing pool name", true, "ns", "", true},
		{"enabled missing both", true, "", "", true},
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
