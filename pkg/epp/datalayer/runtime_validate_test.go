/*
Copyright 2026 The Kubernetes Authors.

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

package datalayer

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/runtime/schema"

	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	extmocks "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/extractor/mocks"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/source/mocks"
)

func TestRuntimeConfigure_Validation(t *testing.T) {
	podGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"}
	svcGVK := schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Service"}

	tests := []struct {
		name      string
		cfg       *Config
		wantErr   bool
		wantInErr string
	}{
		{
			name: "no extractors is allowed",
			cfg: &Config{Sources: []DataSourceConfig{
				{Plugin: &mocks.MetricsDataSource{}},
			}},
			wantErr: false,
		},
		{
			name: "duplicate notification GVK across sources",
			cfg: &Config{Sources: []DataSourceConfig{
				{Plugin: mocks.NewNotificationSource("test", "source1", podGVK)},
				{Plugin: mocks.NewNotificationSource("test", "source2", podGVK)},
			}},
			wantErr:   true,
			wantInErr: "duplicate",
		},
		{
			name: "extractor variant doesn't match source variant",
			cfg: &Config{Sources: []DataSourceConfig{
				{
					Plugin:     &mocks.MetricsDataSource{},
					Extractors: []fwkplugin.Plugin{extmocks.NewEndpointExtractor("ep-ext")},
				},
			}},
			wantErr:   true,
			wantInErr: "PollingExtractor",
		},
		{
			name: "notification extractor GVK doesn't match source GVK",
			cfg: &Config{Sources: []DataSourceConfig{
				{
					Plugin:     mocks.NewNotificationSource("test", "source1", podGVK),
					Extractors: []fwkplugin.Plugin{extmocks.NewNotificationExtractor("ext1").WithGVK(svcGVK)},
				},
			}},
			wantErr:   true,
			wantInErr: "GVK",
		},
		{
			name: "duplicate source name within the same variant",
			cfg: &Config{Sources: []DataSourceConfig{
				{Plugin: mocks.NewDataSource(fwkplugin.TypedName{Type: "metrics", Name: "same"})},
				{Plugin: mocks.NewDataSource(fwkplugin.TypedName{Type: "metrics", Name: "same"})},
			}},
			wantErr:   true,
			wantInErr: "duplicate",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := NewRuntime(1)
			err := r.Configure(tc.cfg, false, "", newTestLogger(t))
			if tc.wantErr {
				require.Error(t, err)
				if tc.wantInErr != "" {
					require.Contains(t, err.Error(), tc.wantInErr)
				}
				return
			}
			require.NoError(t, err)
		})
	}
}
