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

// Package sglangbootstrap connects coordinator prefill requests to SGLang's
// bootstrap service using the endpoint selected by EPP.
package sglangbootstrap

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/common/routing"
	fwkrequest "github.com/llm-d/llm-d-router/pkg/epp/framework/common/request"
	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	fwkrc "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requestcontrol"
	fwkrh "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requesthandling"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
)

const PluginType = "sglang-bootstrap-handler"

type Handler struct {
	typedName   fwkplugin.TypedName
	profileName string
}

var (
	_ fwkrc.PreRequest              = &Handler{}
	_ fwkrc.ResponseHeaderProcessor = &Handler{}
)

func Factory(name string, raw *json.Decoder, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	params := struct {
		ProfileName string `json:"profileName"`
	}{ProfileName: "prefill"}
	if raw != nil {
		if err := raw.Decode(&params); err != nil {
			return nil, fmt.Errorf("%s: parameters: %w", PluginType, err)
		}
	}
	if params.ProfileName == "" {
		return nil, errors.New("sglang-bootstrap-handler: profileName must not be empty")
	}
	if name == "" {
		name = PluginType
	}
	return &Handler{typedName: fwkplugin.TypedName{Type: PluginType, Name: name}, profileName: params.ProfileName}, nil
}

func (h *Handler) TypedName() fwkplugin.TypedName { return h.typedName }

func (h *Handler) isPrefill(req *fwksched.InferenceRequest, result *fwksched.SchedulingResult) bool {
	return req != nil && fwkrequest.GetRequestPath(req.Headers) == reqcommon.PathSGLangGenerate && result != nil && result.PrimaryProfileName == h.profileName
}

func (h *Handler) PreRequest(_ context.Context, req *fwksched.InferenceRequest, result *fwksched.SchedulingResult) error {
	if !h.isPrefill(req, result) {
		return nil
	}
	selected := result.ProfileResults[h.profileName]
	if selected == nil || len(selected.TargetEndpoints) != 1 || selected.TargetEndpoints[0] == nil {
		return errors.New("sglang-bootstrap-handler: one prefill endpoint is required")
	}
	if req.Body == nil || !req.Body.Stream || req.Body.Payload == nil {
		return errors.New("sglang-bootstrap-handler: streaming SGLang request is required")
	}
	payload, ok := req.Body.Payload.AsMap()
	if !ok || payload[reqcommon.FieldBootstrapRoom] == nil || payload[reqcommon.FieldBootstrapPort] == nil {
		return errors.New("sglang-bootstrap-handler: bootstrap room and port are required")
	}
	endpoint := selected.TargetEndpoints[0].GetMetadata()
	if endpoint == nil || endpoint.Address == "" {
		return errors.New("sglang-bootstrap-handler: prefill address is required")
	}
	req.Body.MutatePayloadMap(func(payload fwkrh.PayloadMap) { payload[reqcommon.FieldBootstrapHost] = endpoint.Address })
	return nil
}

func (h *Handler) ResponseHeader(_ context.Context, req *fwksched.InferenceRequest, response *fwkrc.Response, endpoint *fwkdl.EndpointMetadata) {
	if req == nil || !h.isPrefill(req, req.SchedulingResult) || response == nil || response.Headers == nil {
		return
	}
	delete(response.Headers, routing.PrefillEndpointHeader)
	if endpoint != nil && endpoint.Address != "" && endpoint.Port != "" {
		response.Headers[routing.PrefillEndpointHeader] = net.JoinHostPort(endpoint.Address, endpoint.Port)
	}
}
