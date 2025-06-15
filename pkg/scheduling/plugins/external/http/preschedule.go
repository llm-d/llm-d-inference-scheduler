package http

import (
	"context"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// PreSchedule implementation of the external http pre scheduler
type PreSchedule struct {
	Plugin
}

var _ plugins.PreSchedule = &PreSchedule{} // validate interface conformance

// NewPreSchedule creates a new instance of external http pre schedule based on the given parameters
func NewPreSchedule(_ context.Context, name string, url string) plugins.PreSchedule {
	return &PreSchedule{Plugin{name: name, url: url}}
}

// PreSchedule is called when the scheduler receives a new request. It can be used for various initialization work
func (p *PreSchedule) PreSchedule(ctx *types.SchedulingContext) {
	logger := log.FromContext(ctx).WithName(p.Name())
	logger.Info(">>>> external pre schedule called - TODO implement it!", "name", p.Name())

	// TODO - send http request to the url
}
