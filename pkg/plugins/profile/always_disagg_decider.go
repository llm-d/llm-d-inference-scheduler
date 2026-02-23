package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// AlwaysDisaggDeciderPluginType is the type-name of the alwaysDisaggPDDecider plugin.
	AlwaysDisaggDeciderPluginType = "always-disagg-pd-decider"
)

// compile-time type assertion
var _ pdDeciderPlugin = &AlwaysDisaggPDDecider{}

// AlwaysDisaggPDDecider is a PD decider plugin which always decide to disaggregate PD
type AlwaysDisaggPDDecider struct {
	typedName plugin.TypedName
}

// AlwaysDisaggPDDeciderPluginFactory defines the factory function for creating
// a new instance of the AlwaysDisaggPDDecider.
func AlwaysDisaggPDDeciderPluginFactory(name string, _ json.RawMessage,
	_ plugin.Handle) (plugin.Plugin, error) {
	return newAlwaysDisaggPDDecider().WithName(name), nil
}

func newAlwaysDisaggPDDecider() *AlwaysDisaggPDDecider {
	return &AlwaysDisaggPDDecider{}
}

// TypedName returns the typed name of the plugin.
func (d *AlwaysDisaggPDDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *AlwaysDisaggPDDecider) WithName(name string) *AlwaysDisaggPDDecider {
	d.typedName.Name = name
	d.typedName.Type = AlwaysDisaggDeciderPluginType
	return d
}

func (d *AlwaysDisaggPDDecider) disaggregate(ctx context.Context, inputTokens int, endpoint scheduling.Endpoint) bool {
	return true
}
