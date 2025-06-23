// Package prerequest provides pre-request plugins for GIE.
package prerequest

import (
	"context"
	"net"
	"strconv"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
)

const (
	prefillPodHeader = "x-prefiller-url" // prefillPodHeader is the HTTP header name used to indicate Prefill worker
)

// compile-time type assertion
var _ requestcontrol.PreRequest = &PrefillHeaderHandler{}

// NewPrefillHeaderHandler initializes a new PrefillHeaderHandler and returns its pointer.
func NewPrefillHeaderHandler() *PrefillHeaderHandler {
	return &PrefillHeaderHandler{}
}

// PrefillHeaderHandler PreRequest plugin
type PrefillHeaderHandler struct{}

// Type returns the PreRequest plugin type
func (p *PrefillHeaderHandler) Type() string {
	return "prefill-header"
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
