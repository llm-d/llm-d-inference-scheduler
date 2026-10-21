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

package runner

import (
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	configapiv1 "github.com/llm-d/llm-d-router/apix/config/v1"
	"github.com/llm-d/llm-d-router/pkg/epp/config/loader"
)

func TestMetricsRegisterOptionsFromConfig(t *testing.T) {
	tpot := []float64{0.005, 0.01, 0.02}
	itl := []float64{0.001, 0.002}

	testCases := []struct {
		name     string
		config   *configapiv1.EndpointPickerConfig
		wantTPOT []float64
		wantITL  []float64
		wantErr  string
	}{
		{
			name:   "nil config keeps the built-in edges",
			config: nil,
		},
		{
			name:   "omitted metrics section keeps the built-in edges",
			config: &configapiv1.EndpointPickerConfig{},
		},
		{
			name:   "empty metrics section keeps the built-in edges",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{}},
		},
		{
			name: "both histograms configured independently",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{
				RequestTPOTBuckets: tpot,
				RequestITLBuckets:  itl,
			}},
			wantTPOT: tpot,
			wantITL:  itl,
		},
		{
			name: "only TPOT configured leaves ITL on its defaults",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{
				RequestTPOTBuckets: tpot,
			}},
			wantTPOT: tpot,
		},
		{
			name: "only ITL configured leaves TPOT on its defaults",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{
				RequestITLBuckets: itl,
			}},
			wantITL: itl,
		},
		{
			name: "an explicitly empty list is rejected rather than ignored",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{
				RequestTPOTBuckets: []float64{},
			}},
			wantErr: "metrics.requestTPOTBuckets: at least one bucket boundary is required",
		},
		{
			name: "non-increasing edges are rejected",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{
				RequestTPOTBuckets: []float64{0.01, 0.01},
			}},
			wantErr: "metrics.requestTPOTBuckets: bucket boundary 1 is 0.01, want a value greater than the preceding 0.01",
		},
		{
			name: "a non-positive edge is rejected",
			config: &configapiv1.EndpointPickerConfig{Metrics: &configapiv1.MetricsConfig{
				RequestITLBuckets: []float64{0, 0.01},
			}},
			wantErr: "metrics.requestITLBuckets: bucket boundary 0 is 0, want a positive value",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := metricsRegisterOptions(tc.config)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tc.wantErr)
				assert.Empty(t, got.RequestTPOTBuckets, "no options are returned alongside an error")
				assert.Empty(t, got.RequestITLBuckets, "no options are returned alongside an error")
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantTPOT, got.RequestTPOTBuckets)
			assert.Equal(t, tc.wantITL, got.RequestITLBuckets)
		})
	}
}

// The histograms are built during metric registration, which both startup paths
// reach only after the configuration file has been parsed. This pins the
// ordering the configuration location depends on: a global setting read from
// the file can still reach the collectors.
func TestMetricsBucketsParseFromConfigFile(t *testing.T) {
	configText := `apiVersion: llm-d.ai/v1
kind: EndpointPickerConfig
metrics:
  requestTPOTBuckets: [0.005, 0.01, 0.02]
  requestITLBuckets: [0.001, 0.002]
plugins:
- type: single-profile-handler
schedulingProfiles:
- name: default
  plugins: []
`
	rawConfig, _, err := loader.LoadRawConfig([]byte(configText), testr.New(t))
	require.NoError(t, err)
	require.NotNil(t, rawConfig.Metrics)

	got, err := metricsRegisterOptions(rawConfig)
	require.NoError(t, err)
	assert.Equal(t, []float64{0.005, 0.01, 0.02}, got.RequestTPOTBuckets)
	assert.Equal(t, []float64{0.001, 0.002}, got.RequestITLBuckets)
}

// An omitted key, an explicit null and an explicit empty list are three
// different inputs, and only the last one is an error. This pins the decoding
// step as well as the check: a YAML empty list has to survive as a non-nil
// empty slice, because that is what separates "left at the defaults" from
// "configured with nothing".
func TestMetricsBucketsEmptyListIsNotTheSameAsOmitted(t *testing.T) {
	testCases := []struct {
		name            string
		metricsFragment string
		wantSectionNil  bool
		wantEdgesNil    bool
		wantErr         string
	}{
		{
			name:           "section omitted",
			wantSectionNil: true,
		},
		{
			name:            "section present but empty",
			metricsFragment: "metrics: {}\n",
			wantEdgesNil:    true,
		},
		{
			name:            "key set to null",
			metricsFragment: "metrics:\n  requestTPOTBuckets: null\n",
			wantEdgesNil:    true,
		},
		{
			name:            "key set to an empty list",
			metricsFragment: "metrics:\n  requestTPOTBuckets: []\n",
			wantErr:         "metrics.requestTPOTBuckets: at least one bucket boundary is required",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			configText := "apiVersion: llm-d.ai/v1\nkind: EndpointPickerConfig\n" + tc.metricsFragment +
				"plugins:\n- type: single-profile-handler\nschedulingProfiles:\n- name: default\n  plugins: []\n"
			rawConfig, _, err := loader.LoadRawConfig([]byte(configText), testr.New(t))
			require.NoError(t, err)

			if tc.wantSectionNil {
				require.Nil(t, rawConfig.Metrics)
			} else {
				require.NotNil(t, rawConfig.Metrics)
				assert.Equal(t, tc.wantEdgesNil, rawConfig.Metrics.RequestTPOTBuckets == nil,
					"an empty YAML list must decode to a non-nil empty slice, an omitted key to nil")
				assert.Empty(t, rawConfig.Metrics.RequestTPOTBuckets)
			}

			got, err := metricsRegisterOptions(rawConfig)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.EqualError(t, err, tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Empty(t, got.RequestTPOTBuckets)
		})
	}
}
