// Package prerequest provides pre-request plugins for GIE.
package prerequest

import (
	"context"
	"encoding/json"
	"net"
	"strconv"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	// PrefillHeaderHandlerType is the type of the PrefillHeaderHandler
	PrefillHeaderHandlerType = "prefill-header"

	prefillPodHeader = "x-prefiller-url" // prefillPodHeader is the HTTP header name used to indicate Prefill worker
)

// compile-time type assertion
var _ requestcontrol.PreRequest = &PrefillHeaderHandler{}

// PrefillHeaderHandlerFactory  defines the factory function for the PrefillHeaderHandler
func PrefillHeaderHandlerFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewPrefillHeaderHandler().WithName(name), nil
}

// NewPrefillHeaderHandler initializes a new PrefillHeaderHandler and returns its pointer.
func NewPrefillHeaderHandler() *PrefillHeaderHandler {
	return &PrefillHeaderHandler{
		name: PrefillHeaderHandlerType,
	}
}

// PrefillHeaderHandler PreRequest plugin
type PrefillHeaderHandler struct {
	name string
}

// Type returns the type of the PreRequest plugin.
func (p *PrefillHeaderHandler) Type() string {
	return PrefillHeaderHandlerType
}

// Name returns the name of the instance of the filter.
func (p *PrefillHeaderHandler) Name() string {
	return p.name
}

// WithName sets the name of the filter.
func (p *PrefillHeaderHandler) WithName(name string) *PrefillHeaderHandler {
	p.name = name
	return p
}

// PreRequest wires prefill SchedulerProfile result into a header to indicate prefill worker
func (p *PrefillHeaderHandler) PreRequest(_ context.Context, request *types.LLMRequest, schedulingResult *types.SchedulingResult, targetPort int) {
	prefillProfileRunResult, exists := schedulingResult.ProfileResults["prefill"]
	if !exists {
		return // prefill profile failed to run or we chose not to run it, no-op in this case
	}

	// TODO: should the scheme be conifgurable (e.g., https://)?
	prefillURL := "http://" + net.JoinHostPort(prefillProfileRunResult.TargetPod.GetPod().Address, strconv.Itoa(targetPort))
	request.Headers[prefillPodHeader] = prefillURL
}
