package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// AlwaysEncodeDeciderPluginType is the type-name of the AlwaysEncodeDecider plugin.
	AlwaysEncodeDeciderPluginType = "always-encode-decider"
)

// compile-time type assertion
var _ deciderPlugin = &AlwaysEncodeDecider{}

// AlwaysEncodeDecider is an EP decider plugin which always decides to encode.
type AlwaysEncodeDecider struct {
	typedName plugin.TypedName
}

// AlwaysEncodeDeciderPluginFactory defines the factory function for creating
// a new instance of the AlwaysEncodeDecider.
func AlwaysEncodeDeciderPluginFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return newAlwaysEncodeDecider().WithName(name), nil
}

func newAlwaysEncodeDecider() *AlwaysEncodeDecider {
	return &AlwaysEncodeDecider{
		typedName: plugin.TypedName{Type: AlwaysEncodeDeciderPluginType},
	}
}

// TypedName returns the typed name of the plugin.
func (d *AlwaysEncodeDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *AlwaysEncodeDecider) WithName(name string) *AlwaysEncodeDecider {
	d.typedName.Name = name
	return d
}

func (d *AlwaysEncodeDecider) decide(_ context.Context, request *scheduling.LLMRequest, _ scheduling.Endpoint) bool {
	return hasMultimodalContent(request)
}

// hasMultimodalContent returns true if the request contains any image, video, or audio content blocks.
func hasMultimodalContent(request *scheduling.LLMRequest) bool {
	if request == nil || request.Body == nil || request.Body.ChatCompletions == nil {
		return false
	}
	for _, msg := range request.Body.ChatCompletions.Messages {
		// See https://github.com/vllm-project/vllm/blob/main/docs/features/multimodal_inputs.md#online-serving
		for _, block := range msg.Content.Structured {
			if block.Type == "image_url" || block.Type == "video_url" || block.Type == "input_audio" {
				return true
			}
		}
	}
	return false
}
