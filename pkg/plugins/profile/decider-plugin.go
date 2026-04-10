// Package profile provides profile handler plugins for the epp.
package profile

import (
	"context"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/igw/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/igw/framework/interface/scheduling"
)

// deciderPlugin decides whether the disaggregated stage should run for the request.
type deciderPlugin interface {
	plugin.Plugin
	disaggregate(ctx context.Context, request *scheduling.LLMRequest, endpoint scheduling.Endpoint) bool
}
