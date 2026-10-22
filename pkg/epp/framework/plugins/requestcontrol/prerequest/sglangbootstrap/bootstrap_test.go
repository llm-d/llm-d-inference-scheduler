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

package sglangbootstrap

import (
	"context"
	"testing"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/common/routing"
	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkrc "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requestcontrol"
	fwkrh "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requesthandling"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	"github.com/stretchr/testify/require"
)

func TestBootstrapUsesSelectedPrefill(t *testing.T) {
	plugin, err := Factory("", nil, nil)
	require.NoError(t, err)
	h := plugin.(*Handler)
	endpoint := fwksched.NewEndpoint(&fwkdl.EndpointMetadata{Address: "10.0.0.7", Port: "8000"}, &fwkdl.Metrics{}, nil)
	result := &fwksched.SchedulingResult{PrimaryProfileName: "prefill", ProfileResults: map[string]*fwksched.ProfileRunResult{
		"prefill": {TargetEndpoints: []fwksched.Endpoint{endpoint}},
	}}
	payload := fwkrh.PayloadMap{reqcommon.FieldBootstrapHost: "untrusted", reqcommon.FieldBootstrapPort: 8998, reqcommon.FieldBootstrapRoom: 123}
	req := &fwksched.InferenceRequest{Headers: map[string]string{":path": reqcommon.PathSGLangGenerate}, SchedulingResult: result,
		Body: &fwkrh.InferenceRequestBody{Stream: true, Payload: payload}}
	require.NoError(t, h.PreRequest(context.Background(), req, result))
	require.Equal(t, "10.0.0.7", payload[reqcommon.FieldBootstrapHost])
	require.True(t, req.Body.Mutated)
	require.Equal(t, 123, payload[reqcommon.FieldBootstrapRoom])
	response := &fwkrc.Response{Headers: map[string]string{routing.PrefillEndpointHeader: "forged:1"}}
	h.ResponseHeader(context.Background(), req, response, endpoint.GetMetadata())
	require.Equal(t, "10.0.0.7:8000", response.Headers[routing.PrefillEndpointHeader])
	// Decode keeps the prefill host supplied by the coordinator.
	result.PrimaryProfileName = "decode"
	req.Body.Mutated = false
	require.NoError(t, h.PreRequest(context.Background(), req, result))
	require.False(t, req.Body.Mutated)
	require.Equal(t, "10.0.0.7", payload[reqcommon.FieldBootstrapHost])
}
