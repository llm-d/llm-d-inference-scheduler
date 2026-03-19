package prerequest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// EncodeHeaderHandlerType is the type of the EncodeHeaderHandler
	EncodeHeaderHandlerType = "encode-header-handler"

	defaultEncodeProfile = "encode"
)

type encodeHeaderHandlerParameters struct {
	EncodeProfile string `json:"encodeProfile"`
}

// compile-time type assertion
var _ requestcontrol.PreRequest = &EncodeHeaderHandler{}

// EncodeHeaderHandlerFactory defines the factory function for the EncodeHeaderHandler
func EncodeHeaderHandlerFactory(name string, rawParameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	parameters := encodeHeaderHandlerParameters{
		EncodeProfile: defaultEncodeProfile,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' pre-request plugin - %w", EncodeHeaderHandlerType, err)
		}
	}
	return NewEncodeHeaderHandler(parameters.EncodeProfile).WithName(name), nil
}

// NewEncodeHeaderHandler initializes a new EncodeHeaderHandler and returns its pointer.
func NewEncodeHeaderHandler(encodeProfile string) *EncodeHeaderHandler {
	return &EncodeHeaderHandler{
		typedName:     plugin.TypedName{Type: EncodeHeaderHandlerType},
		encodeProfile: encodeProfile,
	}
}

// EncodeHeaderHandler PreRequest plugin
type EncodeHeaderHandler struct {
	typedName     plugin.TypedName
	encodeProfile string
}

// TypedName returns the typed name of the plugin.
func (p *EncodeHeaderHandler) TypedName() plugin.TypedName {
	return p.typedName
}

// WithName sets the name of the plugin.
func (p *EncodeHeaderHandler) WithName(name string) *EncodeHeaderHandler {
	p.typedName.Name = name
	return p
}

// PreRequest wires encode SchedulerProfile result into a header to indicate encode worker
func (p *EncodeHeaderHandler) PreRequest(ctx context.Context, request *scheduling.LLMRequest, schedulingResult *scheduling.SchedulingResult) {
	tracer := telemetry.Tracer()
	_, span := tracer.Start(ctx, "llm_d.epp.prerequest.encode_disaggregation",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	if request == nil {
		span.SetAttributes(attribute.String("llm_d.epp.encode.reason", "request_is_nil"))
		return
	}
	if schedulingResult == nil {
		span.SetAttributes(attribute.String("llm_d.epp.encode.reason", "scheduling_result_is_nil"))
		return
	}

	if request.TargetModel != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
	}
	if request.RequestId != "" {
		span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestId))
	}
	if _, found := request.Headers[common.EncoderEndpointsHeader]; found {
		request.Headers[common.EncoderEndpointsHeader] = "" // clear header, if already set
	}

	encodeProfileRunResult, exists := schedulingResult.ProfileResults[p.encodeProfile]
	if !exists {
		span.SetAttributes(
			attribute.Bool("llm_d.epp.encode.disaggregation_used", false),
			attribute.String("llm_d.epp.encode.reason", "no_encode_profile_result"),
		)
		return // encode profile failed to run or we chose not to run it, no-op in this case
	}

	// Collect all target endpoints as comma-separated host:port pairs
	var encodeHostPorts []string
	for _, endpoint := range encodeProfileRunResult.TargetEndpoints {
		targetEndpoint := endpoint.GetMetadata()
		encodeHostPort := net.JoinHostPort(targetEndpoint.Address, targetEndpoint.Port)
		encodeHostPorts = append(encodeHostPorts, encodeHostPort)
	}

	// Join all host:port pairs with commas
	if len(encodeHostPorts) > 0 {
		request.Headers[common.EncoderEndpointsHeader] = strings.Join(encodeHostPorts, ",")
	}

	span.SetAttributes(
		attribute.Bool("llm_d.epp.encode.disaggregation_used", true),
		attribute.String("llm_d.epp.encode.endpoints", strings.Join(encodeHostPorts, ",")),
	)
}
