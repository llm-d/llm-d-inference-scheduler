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

package datalayer

import (
	"testing"

	"github.com/stretchr/testify/assert"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	attrstring "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/attribute/string"
)

func TestStampDiscoveryAttributes(t *testing.T) {
	ep := fwkdl.NewEndpoint(nil, nil)
	meta := &fwkdl.EndpointMetadata{
		Attributes: map[string]string{"pathPrefix": "/laneA", "model": "fp8"},
	}

	stampDiscoveryAttributes(ep, meta)

	for k, want := range map[string]string{"pathPrefix": "/laneA", "model": "fp8"} {
		v, ok := attrstring.ReadValue(ep.GetAttributes(), fwkplugin.NewDataKey(k, fwkdl.DiscoveryAttributesProducer))
		assert.True(t, ok, "attribute %q should be stamped", k)
		assert.Equal(t, attrstring.Value(want), v)
	}
}

func TestStampDiscoveryAttributes_Empty(t *testing.T) {
	ep := fwkdl.NewEndpoint(nil, nil)

	stampDiscoveryAttributes(ep, &fwkdl.EndpointMetadata{})
	stampDiscoveryAttributes(ep, nil)

	assert.Empty(t, ep.GetAttributes().Keys())
}
