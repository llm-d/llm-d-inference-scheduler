/*
Copyright 2025 The Kubernetes Authors.

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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	extractormocks "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/extractor/mocks"
)

func TestAssertAll(t *testing.T) {
	ext := extractormocks.NewEndpointExtractor("ext")

	cases := []struct {
		name    string
		input   []fwkplugin.Plugin
		wantLen int
		wantErr string
	}{
		{
			name:    "all valid",
			input:   []fwkplugin.Plugin{ext},
			wantLen: 1,
		},
		{
			name:    "nil element",
			input:   []fwkplugin.Plugin{ext, nil},
			wantErr: "nil plugin",
		},
		{
			name:    "wrong type",
			input:   []fwkplugin.Plugin{&wrongPlugin{}},
			wantErr: "is not a",
		},
		{
			name:    "empty",
			input:   nil,
			wantLen: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := assertAll[fwkdl.EndpointExtractor](tc.input, "EndpointExtractor")
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, tc.wantLen)
		})
	}
}

type wrongPlugin struct{}

func (*wrongPlugin) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: "wrong", Name: "wrong"}
}
