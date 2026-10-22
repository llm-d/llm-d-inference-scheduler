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

// Package headerstamp provides a PreRequest plugin that carries attributes of
// the selected endpoint onto outgoing request headers. The canonical pairing
// is the label-producer extractor (which turns discovery labels into string
// attributes) with stamps declared here, so any discovery source can publish
// routing hints that downstream gateways match on — e.g. a pathPrefix label
// prepended to :path so peers behind one shared gateway host are
// discriminated by path, or a model label stamped as x-llm-d-model for
// header matching.
package headerstamp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
	fwkrc "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requestcontrol"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	attrstring "github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/attribute/string"
)

// PluginType is the plugin type for the endpoint header stamper.
const PluginType = "endpoint-header-stamper"

const (
	modeSet     = "set"
	modePrepend = "prepend"
)

type stamp struct {
	// AttributeKey is the endpoint attribute to read, as published by the
	// producing extractor (e.g. label-producer's attributeKey).
	AttributeKey string `json:"attributeKey"`
	// HeaderName is the request header the attribute value is written to.
	// Pseudo-headers such as ":path" are allowed.
	HeaderName string `json:"headerName"`
	// Mode is "set" (default; overwrite the header) or "prepend" (attribute
	// value, trailing slash trimmed, in front of the existing header value).
	Mode string `json:"mode,omitempty"`
}

type parameters struct {
	Stamps []stamp `json:"stamps"`
	// AttributeProducerName selects the producer whose attributes are read.
	// Defaults to fwkdl.DiscoveryAttributesProducer — the key discovery-
	// declared endpoint attributes are stamped with.
	AttributeProducerName string `json:"attributeProducerName,omitempty"`
}

type stampRule struct {
	headerName string
	mode       string
	dataKey    fwkplugin.DataKey
}

// Factory creates an endpoint header stamper.
func Factory(name string, rawParameters *json.Decoder, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	params := parameters{}
	if rawParameters != nil {
		if err := rawParameters.Decode(&params); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' plugin: %w", PluginType, err)
		}
	}
	if len(params.Stamps) == 0 {
		return nil, fmt.Errorf("invalid configuration for '%s' plugin: 'stamps' must be non-empty", PluginType)
	}
	if params.AttributeProducerName == "" {
		params.AttributeProducerName = fwkdl.DiscoveryAttributesProducer
	}
	rules := make([]stampRule, 0, len(params.Stamps))
	for _, s := range params.Stamps {
		if s.AttributeKey == "" || s.HeaderName == "" {
			return nil, fmt.Errorf("invalid configuration for '%s' plugin: every stamp needs attributeKey and headerName", PluginType)
		}
		mode := s.Mode
		if mode == "" {
			mode = modeSet
		}
		if mode != modeSet && mode != modePrepend {
			return nil, fmt.Errorf("invalid configuration for '%s' plugin: stamp mode %q must be %q or %q", PluginType, s.Mode, modeSet, modePrepend)
		}
		rules = append(rules, stampRule{
			headerName: strings.ToLower(s.HeaderName),
			mode:       mode,
			dataKey:    fwkplugin.NewDataKey(s.AttributeKey, params.AttributeProducerName),
		})
	}
	if name == "" {
		name = PluginType
	}
	return &Handler{
		typedName: fwkplugin.TypedName{Type: PluginType, Name: name},
		rules:     rules,
	}, nil
}

var (
	_ fwkrc.PreRequest         = &Handler{}
	_ fwkplugin.ConsumerPlugin = &Handler{}
)

// Handler stamps selected-endpoint attributes onto request headers.
type Handler struct {
	typedName fwkplugin.TypedName
	rules     []stampRule
}

func (h *Handler) TypedName() fwkplugin.TypedName { return h.typedName }

// Consumes declares every stamped attribute as optional: an endpoint without
// the attribute is skipped per stamp rather than failing the request.
func (h *Handler) Consumes() fwkplugin.DataDependencies {
	optional := make(map[fwkplugin.DataKey]any, len(h.rules))
	for _, r := range h.rules {
		optional[r.dataKey] = attrstring.Value("")
	}
	return fwkplugin.DataDependencies{Optional: optional}
}

// PreRequest stamps each configured header from the primary profile's
// selected endpoint. It no-ops when the profile did not run, no endpoint was
// selected, or the endpoint lacks a stamp's attribute.
func (h *Handler) PreRequest(_ context.Context, request *fwksched.InferenceRequest, schedulingResult *fwksched.SchedulingResult) error {
	if request == nil || schedulingResult == nil {
		return nil
	}
	result := schedulingResult.ProfileResults[schedulingResult.PrimaryProfileName]
	if result == nil || len(result.TargetEndpoints) == 0 || result.TargetEndpoints[0] == nil {
		return nil
	}
	endpoint := result.TargetEndpoints[0]
	if request.Headers == nil {
		request.Headers = map[string]string{}
	}
	for _, r := range h.rules {
		v, ok := attrstring.ReadValue(endpoint, r.dataKey)
		if !ok || v == "" {
			continue
		}
		switch r.mode {
		case modePrepend:
			prefix := strings.TrimSuffix(string(v), "/")
			request.Headers[r.headerName] = prefix + request.Headers[r.headerName]
		default:
			request.Headers[r.headerName] = string(v)
		}
	}
	return nil
}
