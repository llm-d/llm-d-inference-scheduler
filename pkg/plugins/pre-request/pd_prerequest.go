// Package prerequest provides pre-request plugins for GIE.
package prerequest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/tracing"
)

const (
	// PrefillHeaderHandlerType is the type of the PrefillHeaderHandler
	PrefillHeaderHandlerType = "prefill-header"
	// prefillPodHeader is the HTTP header name used to indicate Prefill worker
	prefillPodHeader = "x-prefiller-url"

	defaultPrefillProfile = "prefill"
)

type prefillHeaderHandlerParameters struct {
	PrefillProfile string `json:"prefillProfile"`
}

// compile-time type assertion
var _ requestcontrol.PreRequest = &PrefillHeaderHandler{}

// PrefillHeaderHandlerFactory  defines the factory function for the PrefillHeaderHandler
func PrefillHeaderHandlerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := prefillHeaderHandlerParameters{
		PrefillProfile: defaultPrefillProfile,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' pre-request plugin - %w", PrefillHeaderHandlerType, err)
		}
	}
	return NewPrefillHeaderHandler(parameters.PrefillProfile).WithName(name), nil
}

// NewPrefillHeaderHandler initializes a new PrefillHeaderHandler and returns its pointer.
func NewPrefillHeaderHandler(prefillProfile string) *PrefillHeaderHandler {
	return &PrefillHeaderHandler{
		typedName:      plugins.TypedName{Type: PrefillHeaderHandlerType},
		prefillProfile: prefillProfile,
	}
}

// PrefillHeaderHandler PreRequest plugin
type PrefillHeaderHandler struct {
	typedName      plugins.TypedName
	prefillProfile string
}

// TypedName returns the typed name of the plugin.
func (p *PrefillHeaderHandler) TypedName() plugins.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *PrefillHeaderHandler) WithName(name string) *PrefillHeaderHandler {
	p.typedName.Name = name
	return p
}

// PreRequest wires prefill SchedulerProfile result into a header to indicate prefill worker
func (p *PrefillHeaderHandler) PreRequest(ctx context.Context, request *types.LLMRequest, schedulingResult *types.SchedulingResult, targetPort int) {
	ctx, span := tracing.StartGatewaySpan(ctx, "epp.pd_prerequest")
	defer span.End()

	prefillProfileRunResult, exists := schedulingResult.ProfileResults[p.prefillProfile]
	if !exists {
		span.SetAttributes(attribute.Bool("llm_d.pd.disaggregation_enabled", false))
		tracing.SetSpanSuccess(span)
		return // prefill profile failed to run or we chose not to run it, no-op in this case
	}

	// TODO: should the scheme be conifgurable (e.g., https://)?
	prefillURL := "http://" + net.JoinHostPort(prefillProfileRunResult.TargetPod.GetPod().Address, strconv.Itoa(targetPort))
	request.Headers[prefillPodHeader] = prefillURL

	span.SetAttributes(
		attribute.Bool("llm_d.pd.disaggregation_enabled", true),
		attribute.String("llm_d.pd.prefill_pod_address", prefillProfileRunResult.TargetPod.GetPod().Address),
	)
	tracing.SetSpanSuccess(span)
}
