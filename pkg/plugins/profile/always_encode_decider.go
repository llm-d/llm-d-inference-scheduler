package profile

import (
	"context"
	"encoding/json"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// AlwaysEncodeDeciderPluginType is the type-name of the AlwaysEncodeDecider plugin.
	AlwaysEncodeDeciderPluginType = "always-encode-decider"
)

// compile-time type assertion
var _ epdDeciderPlugin = &AlwaysEncodeDecider{}

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
	return &AlwaysEncodeDecider{}
}

// TypedName returns the typed name of the plugin.
func (d *AlwaysEncodeDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *AlwaysEncodeDecider) WithName(name string) *AlwaysEncodeDecider {
	d.typedName.Name = name
	d.typedName.Type = AlwaysEncodeDeciderPluginType
	return d
}

func (d *AlwaysEncodeDecider) disaggregateEncode(ctx context.Context, _ *scheduling.LLMRequest, _ scheduling.Endpoint) bool {
	log.FromContext(ctx).V(logutil.DEBUG).Info("in disaggregateEncode!")
	return true
}
