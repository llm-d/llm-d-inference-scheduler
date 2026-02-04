package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// AlwaysDisaggrDeciderPluginType is the type-name of the alwaysDisaggrPDDecider plugin.
	AlwaysDisaggrDeciderPluginType = "always-disaggr-pd-decider"
)

// compile-time type assertion
var _ pdDeciderPlugin = &AlwaysDisaggrPDDecider{}

// AlwaysDisaggrPDDecider is a PD decider plugin which always decide to disaggregate PD
type AlwaysDisaggrPDDecider struct {
	typedName plugin.TypedName
}

// AlwaysDisaggrPDDeciderPluginFactory defines the factory function for creating
// a new instance of the AlwaysDisaggrPDDecider.
func AlwaysDisaggrPDDeciderPluginFactory(name string, _ json.RawMessage,
	_ plugin.Handle) (plugin.Plugin, error) {
	return newAlwaysDisaggrPDDecider().WithName(name), nil
}

func newAlwaysDisaggrPDDecider() *AlwaysDisaggrPDDecider {
	return &AlwaysDisaggrPDDecider{}
}

// TypedName returns the typed name of the plugin.
func (d *AlwaysDisaggrPDDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *AlwaysDisaggrPDDecider) WithName(name string) *AlwaysDisaggrPDDecider {
	d.typedName.Name = name
	return d
}

func (d *AlwaysDisaggrPDDecider) shouldDisaggregate(ctx context.Context, inputTokens int, endpoint scheduling.Endpoint) bool {
	return true
}
