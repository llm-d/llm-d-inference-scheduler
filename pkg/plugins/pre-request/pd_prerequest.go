// Package prerequest provides pre-request plugins for GIE.
package prerequest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// PrefillHeaderHandlerType is the type of the PrefillHeaderHandler
	PrefillHeaderHandlerType = "prefill-header-handler"

	defaultPrefillProfile = "prefill"
)

type prefillHeaderHandlerParameters struct {
	PrefillProfile string `json:"prefillProfile"`
}

// compile-time type assertion
var _ requestcontrol.PreRequest = &PrefillHeaderHandler{}

// PrefillHeaderHandlerFactory  defines the factory function for the PrefillHeaderHandler
func PrefillHeaderHandlerFactory(name string, rawParameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
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
		typedName:      plugin.TypedName{Type: PrefillHeaderHandlerType},
		prefillProfile: prefillProfile,
	}
}

// PrefillHeaderHandler PreRequest plugin
type PrefillHeaderHandler struct {
	typedName      plugin.TypedName
	prefillProfile string
}

// TypedName returns the typed name of the plugin.
func (p *PrefillHeaderHandler) TypedName() plugin.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *PrefillHeaderHandler) WithName(name string) *PrefillHeaderHandler {
	p.typedName.Name = name
	return p
}

// PreRequest wires prefill SchedulerProfile result into a header to indicate prefill worker
func (p *PrefillHeaderHandler) PreRequest(ctx context.Context, request *scheduling.LLMRequest, schedulingResult *scheduling.SchedulingResult) {
	tracer := telemetry.Tracer()
	_, span := tracer.Start(ctx, "llm_d.epp.prerequest.pd_disaggregation",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	if request != nil && request.TargetModel != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
	}
	if request != nil && request.RequestId != "" {
		span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestId))
	}
	if _, found := request.Headers[common.PrefillPodHeader]; found {
		request.Headers[common.PrefillPodHeader] = "" // clear header, if already set
	}

	prefillProfileRunResult, exists := schedulingResult.ProfileResults[p.prefillProfile]
	if !exists {
		span.SetAttributes(
			attribute.Bool("llm_d.epp.pd.disaggregation_enabled", false),
			attribute.String("llm_d.epp.pd.reason", "no_prefill_profile_result"),
		)
		return // prefill profile failed to run or we chose not to run it, no-op in this case
	}

	targetPod := prefillProfileRunResult.TargetEndpoints[0].GetMetadata()
	prefillHostPort := net.JoinHostPort(targetPod.Address, targetPod.Port)
	request.Headers[common.PrefillPodHeader] = prefillHostPort // in the form of <ip:port>

	span.SetAttributes(
		attribute.Bool("llm_d.epp.pd.disaggregation_enabled", true),
		attribute.String("llm_d.epp.pd.prefill_pod_address", targetPod.Address),
		attribute.String("llm_d.epp.pd.prefill_pod_port", targetPod.Port),
	)
}
