package profile

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestPdProfileHandlerFactory(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		jsonParams string
		expectErr  bool
	}{
		{
			name:       "valid configuration with all defaults",
			pluginName: "default-handler",
			jsonParams: "{}",
			expectErr:  false,
		},
		{
			name:       "valid configuration with custom values",
			pluginName: "custom-handler",
			jsonParams: `{
				"threshold": 100,
				"decodeProfile": "my-decode",
				"prefillProfile": "my-prefill",
				"prefixPluginName": "my-prefix-cache",
				"hashBlockSize": 32,
				"primaryPort": 8080
			}`,
			expectErr: false,
		},
		{
			name:       "zero primaryPort is allowed",
			pluginName: "zero-port",
			jsonParams: `{"primaryPort": 0}`,
			expectErr:  false,
		},
		{
			name:       "threshold = 0 is allowed",
			pluginName: "zero-threshold",
			jsonParams: `{"threshold": 0}`,
			expectErr:  false,
		},
		{
			name:       "negative threshold should error",
			pluginName: "neg-threshold",
			jsonParams: `{"threshold": -1}`,
			expectErr:  true,
		},
		{
			name:       "hashBlockSize = 0 should error",
			pluginName: "zero-block-size",
			jsonParams: `{"hashBlockSize": 0}`,
			expectErr:  true,
		},
		{
			name:       "negative hashBlockSize should error",
			pluginName: "neg-block-size",
			jsonParams: `{"hashBlockSize": -5}`,
			expectErr:  true,
		},
		{
			name:       "primaryPort below range should error",
			pluginName: "port-too-low",
			jsonParams: `{"primaryPort": 0}`, // OK
			expectErr:  false,
		},
		{
			name:       "primaryPort = 1 is valid",
			pluginName: "port-min",
			jsonParams: `{"primaryPort": 1}`,
			expectErr:  false,
		},
		{
			name:       "primaryPort = 65535 is valid",
			pluginName: "port-max",
			jsonParams: `{"primaryPort": 65535}`,
			expectErr:  false,
		},
		{
			name:       "empty decodeProfile is valid",
			pluginName: "empty-decode",
			jsonParams: `{"decodeProfile": ""}`,
			expectErr:  false,
		},
		{
			name:       "empty prefillProfile is valid",
			pluginName: "empty-prefill",
			jsonParams: `{"prefillProfile": ""}`,
			expectErr:  false,
		},
		{
			name:       "empty prefixPluginName is valid",
			pluginName: "empty-prefix-plugin",
			jsonParams: `{"prefixPluginName": ""}`,
			expectErr:  false,
		},
		{
			name:       "primaryPort = 65536 should error",
			pluginName: "port-too-high",
			jsonParams: `{"primaryPort": 65536}`,
			expectErr:  true,
		},
		{
			name:       "primaryPort = -10 should error",
			pluginName: "port-negative",
			jsonParams: `{"primaryPort": -10}`,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawParams json.RawMessage
			if tt.jsonParams != "" {
				rawParams = json.RawMessage(tt.jsonParams)
			}
			plugin, err := PdProfileHandlerFactory(tt.pluginName, rawParams, nil)

			if tt.expectErr {
				assert.Error(t, err)
				assert.Nil(t, plugin)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, plugin)
			}
		})
	}
}

func TestPdProfileHandlerFactoryInvalidJSON(t *testing.T) {
	invalidTests := []struct {
		name       string
		jsonParams string
	}{
		{
			name:       "malformed JSON",
			jsonParams: `{"threshold": 100, "hashBlockSize":`, // incomplete
		},
		{
			name:       "threshold as string instead of int",
			jsonParams: `{"threshold": "100"}`,
		},
		{
			name:       "hashBlockSize as boolean",
			jsonParams: `{"hashBlockSize": true}`,
		},
		{
			name:       "primaryPort as float",
			jsonParams: `{"primaryPort": 8080.5}`,
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)
			plugin, err := PdProfileHandlerFactory("test", rawParams, nil)

			assert.Error(t, err)
			assert.Nil(t, plugin)
		})
	}
}
