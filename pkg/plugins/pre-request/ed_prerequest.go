package prerequest

import (
	"context"
	"encoding/json"
	"fmt"
	"net"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/requestcontrol"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
)

const (
	// EncodeHeaderHandlerType is the type of the EncodeHeaderHandler
	EncodeHeaderHandlerType = "encoder-header-handler"

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
	if _, found := request.Headers[common.EncoderHostsPortsHeader]; found {
		request.Headers[common.EncoderHostsPortsHeader] = "" // clear header, if already set
	}

	encodeProfileRunResult, exists := schedulingResult.ProfileResults[p.encodeProfile]
	if !exists {
		return // encode profile failed to run or we chose not to run it, no-op in this case
	}

	targetPod := encodeProfileRunResult.TargetEndpoints[0].GetMetadata()
	encodeHostPort := net.JoinHostPort(targetPod.Address, targetPod.Port)
	request.Headers[common.EncoderHostsPortsHeader] = encodeHostPort // in the form of <ip:port>
	log.FromContext(ctx).V(logutil.DEBUG).Info("ED: PreRequest", "request", request)
}
