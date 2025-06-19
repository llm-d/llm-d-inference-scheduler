// Package filter provides filter plugins for the epp.
package filter

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// compile-time type assertion
var _ framework.Filter = &Passthrough{}

// Passthrough filter type
type Passthrough struct{}

// Name returns the filter name
func (p *Passthrough) Name() string {
	return "passthrough-filter"
}

// Filter defines the filtering function. In this case it is a passthrough
func (p *Passthrough) Filter(ctx context.Context, _ *types.LLMRequest, _ *types.CycleState, pods []types.Pod) []types.Pod {
	log.FromContext(ctx).V(logutil.DEBUG).Info(fmt.Sprintf("Passthrough filter called with %d candidates: %+v",
		len(pods), pods))

	return pods
}
