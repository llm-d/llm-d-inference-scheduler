// Package filter provides filter plugins for the epp.
package filter

import (
	"context"
	"fmt"
	"math/rand/v2"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// compile-time type assertion
var _ framework.Filter = &Random{}

// Random drop filter type
type Random struct {
	probability float64
}

// Name returns the filter name
func (r *Random) Name() string {
	return "random-drop-filter"
}

// Filter defines the filtering function. In this case it is a passthrough
func (r *Random) Filter(ctx context.Context, _ *types.LLMRequest, _ *types.CycleState, pods []types.Pod) []types.Pod {
	loggerDebug := log.FromContext(ctx).V(logutil.DEBUG)
	loggerDebug.Info(fmt.Sprintf("Random filter called with %d candidates: %+v",
		len(pods), pods))
	filtered := []types.Pod{}

	for _, p := range pods {
		if rand.Float64() >= r.probability {
			filtered = append(filtered, p)
		} else {
			loggerDebug.Info(fmt.Sprintf("%v dropped", p))
		}
	}

	return filtered
}
