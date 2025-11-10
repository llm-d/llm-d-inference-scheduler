package filter

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestByLabelFactory(t *testing.T) {
	tests := []struct {
		name       string
		pluginName string
		jsonParams string
		expectErr  bool
	}{
		{
			name:       "valid configuration with non-empty validValues",
			pluginName: "valid-filter",
			jsonParams: fmt.Sprintf(`{
				"label": %q,
				"allowsNoLabel": false,
				"validValues": [%q]
			}`, RoleLabel, RolePrefill),
			expectErr: false,
		},
		{
			name:       "allowsNoLabel true with empty validValues",
			pluginName: "allow-no-label",
			jsonParams: fmt.Sprintf(`{
				"label": %q,
				"allowsNoLabel": true,
				"validValues": []
			}`, RoleLabel),
			expectErr: false,
		},
		{
			name:       "allowsNoLabel true with multiple valid roles",
			pluginName: "mixed-mode",
			jsonParams: fmt.Sprintf(`{
				"label": %q,
				"allowsNoLabel": true,
				"validValues": [%q, %q]
			}`, RoleLabel, RoleDecode, RoleBoth),
			expectErr: false,
		},
		{
			name:       "empty label name should error",
			pluginName: "empty-label",
			jsonParams: fmt.Sprintf(`{
				"label": "",
				"allowsNoLabel": false,
				"validValues": [%q]
			}`, RolePrefill),
			expectErr: true,
		},
		{
			name:       "missing label field should error",
			pluginName: "missing-label",
			jsonParams: fmt.Sprintf(`{
				"allowsNoLabel": false,
				"validValues": [%q]
			}`, RolePrefill),
			expectErr: true,
		},
		{
			name:       "contradictory config: empty validValues and allowsNoLabel=false",
			pluginName: "invalid-contradiction",
			jsonParams: fmt.Sprintf(`{
				"label": %q,
				"allowsNoLabel": false,
				"validValues": []
			}`, RoleLabel),
			expectErr: true,
		},
		{
			name:       "contradictory config: no validValues field and allowsNoLabel=false",
			pluginName: "no-valid-values",
			jsonParams: fmt.Sprintf(`{
				"label": %q,
				"allowsNoLabel": false
			}`, RoleLabel),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)
			plugin, err := ByLabelFactory(tt.pluginName, rawParams, nil)

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

func TestByLabelFactoryInvalidJSON(t *testing.T) {
	invalidTests := []struct {
		name       string
		jsonParams string
	}{
		{
			name:       "malformed JSON",
			jsonParams: `{"label": "app", "validValues": ["a"`, // missing closing ]
		},
		{
			name:       "validValues as string instead of array",
			jsonParams: `{"label": "app", "validValues": "true"}`,
		},
		{
			name:       "allowsNoLabel as string",
			jsonParams: `{"label": "app", "allowsNoLabel": "yes", "validValues": ["true"]}`,
		},
	}

	for _, tt := range invalidTests {
		t.Run(tt.name, func(t *testing.T) {
			rawParams := json.RawMessage(tt.jsonParams)
			plugin, err := ByLabelFactory("test", rawParams, nil)

			assert.Error(t, err)
			assert.Nil(t, plugin)
		})
	}
}
