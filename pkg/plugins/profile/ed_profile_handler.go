package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
)

const (
	// EdProfileHandlerType is the type of the EdProfileHandler.
	EdProfileHandlerType = "ed-profile-handler"

	defaultEdEncodeProfile = "encode"
)

// epdDeciderPlugin extends plugin.Plugin with an encode stage decision.
type epdDeciderPlugin interface {
	plugin.Plugin
	// disaggregateEncode decides if the encode stage should run for this request.
	// Returns true if encode is needed (e.g. encoding cache miss on a multimodal request).
	disaggregateEncode(ctx context.Context, request *scheduling.LLMRequest, endpoint scheduling.Endpoint) bool
}

type edProfileHandlerParameters struct {
	DecodeProfile     string `json:"decodeProfile"`
	EncodeProfile     string `json:"encodeProfile"`
	PrimaryPort       int    `json:"primaryPort"`
	DeciderPluginName string `json:"deciderPluginName"`
}

// compile-time type assertion
var _ scheduling.ProfileHandler = &EdProfileHandler{}

// EdProfileHandlerFactory defines the factory function for the EdProfileHandler.
func EdProfileHandlerFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := edProfileHandlerParameters{
		DecodeProfile: defaultDecodeProfile,
		EncodeProfile: defaultEdEncodeProfile,
		PrimaryPort:   0,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", EdProfileHandlerType, err)
		}
	}

	if parameters.PrimaryPort != 0 {
		if parameters.PrimaryPort < 1 || parameters.PrimaryPort > 65535 {
			return nil, fmt.Errorf("invalid primaryPort: must be between 1 and 65535, got %d", parameters.PrimaryPort)
		}
	}

	var deciderPlugin epdDeciderPlugin
	if parameters.DeciderPluginName != "" {
		p := handle.Plugin(parameters.DeciderPluginName)
		if p == nil {
			return nil, fmt.Errorf("invalid decider plugin type: %s", parameters.DeciderPluginName)
		}
		var ok bool
		deciderPlugin, ok = p.(epdDeciderPlugin)
		if !ok {
			return nil, fmt.Errorf("decider plugin of type: %s does not implement epdDeciderPlugin", parameters.DeciderPluginName)
		}
	}

	handler := NewEdProfileHandler(
		parameters.DecodeProfile,
		parameters.EncodeProfile,
		parameters.PrimaryPort,
		deciderPlugin,
	)

	return handler.WithName(name), nil
}

// NewEdProfileHandler initializes a new EdProfileHandler and returns its pointer.
func NewEdProfileHandler(decodeProfile, encodeProfile string, primaryPort int, deciderPlugin epdDeciderPlugin) *EdProfileHandler {
	result := &EdProfileHandler{
		typedName:     plugin.TypedName{Type: EdProfileHandlerType},
		decodeProfile: decodeProfile,
		encodeProfile: encodeProfile,
		decider:       deciderPlugin,
	}
	if primaryPort != 0 {
		result.primaryPort = strconv.Itoa(primaryPort)
	}
	return result
}

// EdProfileHandler handles scheduler profiles for ED (Encode → Decode).
// Decode is always scheduled first to determine the target worker.
// Encode is then conditionally scheduled for multimodal content if the decider approves.
type EdProfileHandler struct {
	typedName     plugin.TypedName
	decodeProfile string
	encodeProfile string
	primaryPort   string
	decider       epdDeciderPlugin
}

// TypedName returns the typed name of the plugin.
func (h *EdProfileHandler) TypedName() plugin.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *EdProfileHandler) WithName(name string) *EdProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run: decode first (always), then encode (conditionally).
func (h *EdProfileHandler) Pick(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest,
	profiles map[string]scheduling.SchedulerProfile,
	profileResults map[string]*scheduling.ProfileRunResult) map[string]scheduling.SchedulerProfile {

	if _, executed := profileResults[h.decodeProfile]; !executed {
		return map[string]scheduling.SchedulerProfile{
			h.decodeProfile: profiles[h.decodeProfile],
		}
	}

	// when a profile run fails its result value is nil. we need to check decode result before continuing
	// check if all configured profiles have been executed, or if decode failed, no need to run more profiles.
	if len(profiles) == len(profileResults) || profileResults[h.decodeProfile] == nil {
		return map[string]scheduling.SchedulerProfile{}
	}

	if h.decider != nil && h.decider.disaggregateEncode(ctx, request, profileResults[h.decodeProfile].TargetEndpoints[0]) {
		metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeEncodeDecode)
		log.FromContext(ctx).V(logutil.DEBUG).Info("ED: encode is required")
		return map[string]scheduling.SchedulerProfile{
			h.encodeProfile: profiles[h.encodeProfile],
		}
	}

	metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeDecodeOnly)
	return map[string]scheduling.SchedulerProfile{}
}

// ProcessResults handles the outcome of the profile runs.
// Decode is always the primary profile. If encode ran, its pod address is injected as a header.
func (h *EdProfileHandler) ProcessResults(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest,
	profileResults map[string]*scheduling.ProfileRunResult) (*scheduling.SchedulingResult, error) {

	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil {
		return nil, errors.New("failed to find available decode workers")
	}

	updatedResults := map[string]*scheduling.ProfileRunResult{}

	if h.primaryPort != "" {
		// TODO: check Data Parallel

		targetEndpoint := decodeRunResults.TargetEndpoints[0].GetMetadata()
		request.Headers[common.DataParallelPodHeader] = net.JoinHostPort(targetEndpoint.Address, targetEndpoint.Port)

		updatedResult := scheduling.ProfileRunResult{
			TargetEndpoints: []scheduling.Endpoint{},
		}
		for _, target := range decodeRunResults.TargetEndpoints {
			updatedEndpointInfo := target.GetMetadata().Clone()
			updatedEndpointInfo.Port = h.primaryPort
			targetEndpoint := scheduling.NewEndpoint(updatedEndpointInfo, target.GetMetrics().Clone(), nil)
			updatedResult.TargetEndpoints = append(updatedResult.TargetEndpoints, targetEndpoint)
		}
		updatedResults[h.decodeProfile] = &updatedResult
	} else {
		updatedResults[h.decodeProfile] = decodeRunResults
	}

	// Add encode result if it ran successfully, and inject the encode pod header.
	if encodeRunResult, exists := profileResults[h.encodeProfile]; exists && encodeRunResult != nil {
		encodePod := encodeRunResult.TargetEndpoints[0].GetMetadata()
		request.Headers[common.EncoderHostsPortsHeader] = net.JoinHostPort(encodePod.Address, encodePod.Port)
		log.FromContext(ctx).V(logutil.DEBUG).Info("ED: encode worker selected, setting encode header",
			"encodeWorker", request.Headers[common.EncoderHostsPortsHeader])
		updatedResults[h.encodeProfile] = encodeRunResult
	}

	return &scheduling.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}
