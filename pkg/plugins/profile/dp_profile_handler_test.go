package profile

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestDataParallelProfileHandlerFactory(t *testing.T) {
	tests := []struct {
		name         string
		pluginName   string
		jsonParams   string
		expectErr    bool
		expectedPort string // expected primaryPort as string
	}{
		{
			name:         "use default primaryPort (8000)",
			pluginName:   "default-handler",
			jsonParams:   "{}",
			expectErr:    false,
			expectedPort: "8000",
		},
		{
			name:         "explicit primaryPort = 9000",
			pluginName:   "custom-port",
			jsonParams:   `{"primaryPort": 9000}`,
			expectErr:    false,
			expectedPort: "9000",
		},
		{
			name:         "primaryPort = 1 (minimum valid)",
			pluginName:   "min-port",
			jsonParams:   `{"primaryPort": 1}`,
			expectErr:    false,
			expectedPort: "1",
		},
		{
			name:         "primaryPort = 65535 (maximum valid)",
			pluginName:   "max-port",
			jsonParams:   `{"primaryPort": 65535}`,
			expectErr:    false,
			expectedPort: "65535",
		},
		{
			name:         "primaryPort = 0 is allowed (but results in '0' string)",
			pluginName:   "zero-port",
			jsonParams:   `{"primaryPort": 0}`,
			expectErr:    false,
			expectedPort: "0",
		},
		{
			name:       "primaryPort = -1 should error",
			pluginName: "negative-port",
			jsonParams: `{"primaryPort": -1}`,
			expectErr:  true,
		},
		{
			name:       "primaryPort = 65536 should error",
			pluginName: "port-too-high",
			jsonParams: `{"primaryPort": 65536}`,
			expectErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var rawParams json.RawMessage
			if tt.jsonParams != "" {
				rawParams = json.RawMessage(tt.jsonParams)
			}
			plugin, err := DataParallelProfileHandlerFactory(tt.pluginName, rawParams, nil)

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

func TestDataParallelProfileHandlerFactoryInvalidJSON(t *testing.T) {
	invalidTests := []struct {
		name       string
		jsonParams string
	}{
		{
			name:       "malformed JSON",
			jsonParams: `{"primaryPort":`,
		},
		{
			name:       "primaryPort as string",
			jsonParams: `{"primaryPort": "8000"}`,
		},
		{
			name:       "primaryPort as boolean",
			jsonParams: `{"primaryPort": true}`,
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {

			rawParams := json.RawMessage(tt.jsonParams)
			plugin, err := DataParallelProfileHandlerFactory("test", rawParams, nil)

			assert.Error(t, err)
			assert.Nil(t, plugin)
		})
	}
}
