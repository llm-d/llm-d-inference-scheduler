package http

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

// PostSchedule implementation of the external http post-scheduler
type PostSchedule struct {
	Plugin
}

var _ plugins.PostSchedule = &PostSchedule{} // validate interface conformance

// NewPostSchedule creates a new instance of external http post schedule based on the given parameters
func NewPostSchedule(_ context.Context, name string, url string) plugins.PostSchedule {
	return &PostSchedule{Plugin{name: name, url: url}}
}

// PostSchedule is called after scheduler picked the target pod
func (p *PostSchedule) PostSchedule(ctx *types.SchedulingContext, res *types.Result) {
	logger := log.FromContext(ctx).WithName(p.Name())
	logger.Info(">>>> external post schedule called - TODO implement it!", "name", p.Name())

	// TODO - send http request to the url
	fmt.Println(res)
}
