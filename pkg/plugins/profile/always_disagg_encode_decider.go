package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// AlwaysDisaggEncodePluginType is the type-name of the AlwaysDisaggEncodeDecider plugin.
	AlwaysDisaggEncodePluginType = "always-disagg-encode-decider"
)

// compile-time type assertion
var _ deciderPlugin = &AlwaysDisaggEncodeDecider{}

// AlwaysDisaggEncodeDecider is an EP decider plugin which always decides to encode.
type AlwaysDisaggEncodeDecider struct {
	typedName plugin.TypedName
}

// AlwaysDisaggEncodeDeciderPluginFactory defines the factory function for creating
// a new instance of the AlwaysDisaggEncodeDecider.
func AlwaysDisaggEncodeDeciderPluginFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return newAlwaysDisaggEncodeDecider().WithName(name), nil
}

func newAlwaysDisaggEncodeDecider() *AlwaysDisaggEncodeDecider {
	return &AlwaysDisaggEncodeDecider{
		typedName: plugin.TypedName{Type: AlwaysDisaggEncodePluginType},
	}
}

// TypedName returns the typed name of the plugin.
func (d *AlwaysDisaggEncodeDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *AlwaysDisaggEncodeDecider) WithName(name string) *AlwaysDisaggEncodeDecider {
	d.typedName.Name = name
	return d
}

func (d *AlwaysDisaggEncodeDecider) disaggregate(_ context.Context, request *scheduling.LLMRequest, _ scheduling.Endpoint) bool {
	return hasMultimodalContent(request)
}
