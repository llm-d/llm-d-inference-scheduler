package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
)

const (
	// E_pdProfileHandlerType is the type of the E_pdProfileHandler.
	E_pdProfileHandlerType = "e-pd-profile-handler"

	defaultE_pdEncodeProfile = "encode"
)

// e_pdDeciderPlugin extends plugin.Plugin with an encode stage decision.
type e_pdDeciderPlugin interface {
	plugin.Plugin
	// disaggregateEncode decides if the encode stage should run for this request.
	// Returns true if encode is needed (e.g. encoding cache miss on a multimodal request).
	disaggregateEncode(ctx context.Context, request *scheduling.LLMRequest, endpoint scheduling.Endpoint) bool
}

type e_pdProfileHandlerParameters struct {
	DecodeProfile     string `json:"decodeProfile"`
	EncodeProfile     string `json:"encodeProfile"`
	DeciderPluginName string `json:"deciderPluginName"`
}

// compile-time type assertion
var _ scheduling.ProfileHandler = &E_pdProfileHandler{}

// E_pdProfileHandlerFactory defines the factory function for the E_pdProfileHandler.
func E_pdProfileHandlerFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := e_pdProfileHandlerParameters{
		DecodeProfile: defaultDecodeProfile,
		EncodeProfile: defaultE_pdEncodeProfile,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", E_pdProfileHandlerType, err)
		}
	}

	var deciderPlugin e_pdDeciderPlugin
	if parameters.DeciderPluginName != "" {
		p := handle.Plugin(parameters.DeciderPluginName)
		if p == nil {
			return nil, fmt.Errorf("invalid decider plugin type: %s", parameters.DeciderPluginName)
		}
		var ok bool
		deciderPlugin, ok = p.(e_pdDeciderPlugin)
		if !ok {
			return nil, fmt.Errorf("decider plugin of type: %s does not implement e_pdDeciderPlugin", parameters.DeciderPluginName)
		}
	}

	handler := NewE_pdProfileHandler(
		parameters.DecodeProfile,
		parameters.EncodeProfile,
		deciderPlugin,
	)

	return handler.WithName(name), nil
}

// NewE_pdProfileHandler initializes a new E_pdProfileHandler and returns its pointer.
func NewE_pdProfileHandler(decodeProfile, encodeProfile string, deciderPlugin e_pdDeciderPlugin) *E_pdProfileHandler {
	return &E_pdProfileHandler{
		typedName:     plugin.TypedName{Type: E_pdProfileHandlerType},
		decodeProfile: decodeProfile,
		encodeProfile: encodeProfile,
		decider:       deciderPlugin,
	}
}

// E_pdProfileHandler handles scheduler profiles for E_PD (Encode → Decode).
// Decode is always scheduled first to determine the target worker.
// Encode is then conditionally scheduled for multimodal content if the decider approves.
type E_pdProfileHandler struct {
	typedName     plugin.TypedName
	decodeProfile string
	encodeProfile string
	decider       e_pdDeciderPlugin
}

// TypedName returns the typed name of the plugin.
func (h *E_pdProfileHandler) TypedName() plugin.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *E_pdProfileHandler) WithName(name string) *E_pdProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
func (h *E_pdProfileHandler) Pick(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest,
	profiles map[string]scheduling.SchedulerProfile,
	profileResults map[string]*scheduling.ProfileRunResult) map[string]scheduling.SchedulerProfile {

	// First find the Decode node
	if _, executed := profileResults[h.decodeProfile]; !executed {
		return map[string]scheduling.SchedulerProfile{
			h.decodeProfile: profiles[h.decodeProfile],
		}
	}

	// Safely verify Decode was successful before moving forward
	decodeRes := profileResults[h.decodeProfile]
	if decodeRes == nil || len(decodeRes.TargetEndpoints) == 0 {
		// If we cannot find a decode pod, scheduling completely fails.
		return map[string]scheduling.SchedulerProfile{}
	}

	if !hasMultimodalContent(request) {
		metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeDecodeOnly)
		return map[string]scheduling.SchedulerProfile{}
	}

	if _, executed := profileResults[h.encodeProfile]; !executed {
		// if encode profile was not executed yet, let the scheduler run the encode profile
		return map[string]scheduling.SchedulerProfile{
			h.encodeProfile: profiles[h.encodeProfile],
		}
	}

	// Safely verify Encode was successful. If not, fallback to single-stage Decode-only.
	encodeRes := profileResults[h.encodeProfile]
	if encodeRes == nil || len(encodeRes.TargetEndpoints) == 0 {
		metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeDecodeOnly)
		return map[string]scheduling.SchedulerProfile{}
	}

	if h.decider != nil && !h.decider.disaggregateEncode(ctx, request, encodeRes.TargetEndpoints[0]) {
		// Decider explicitly rejected the disaggregation
		metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeDecodeOnly)
		delete(profileResults, h.encodeProfile)
		return map[string]scheduling.SchedulerProfile{}
	}

	metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeEncodeDecode)
	return map[string]scheduling.SchedulerProfile{}
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// In case of an error in any of the profiles, the matching entry in the profileResults will contain nil, to indicate there was
// an error while running the profile.
func (h *E_pdProfileHandler) ProcessResults(ctx context.Context, _ *scheduling.CycleState, _ *scheduling.LLMRequest,
	profileResults map[string]*scheduling.ProfileRunResult) (*scheduling.SchedulingResult, error) {

	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil {
		return nil, errors.New("failed to find available decode workers")
	}

	// TODO: handle Data Parallel
	updatedResults := map[string]*scheduling.ProfileRunResult{
		h.decodeProfile: decodeRunResults,
	}

	if encodeRunResult, exists := profileResults[h.encodeProfile]; exists && encodeRunResult != nil {
		updatedResults[h.encodeProfile] = encodeRunResult
	}

	return &scheduling.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}

// hasMultimodalContent returns true if the request contains any image, video, or audio content blocks.
func hasMultimodalContent(request *scheduling.LLMRequest) bool {
	if request == nil || request.Body == nil || request.Body.ChatCompletions == nil {
		return false
	}
	for _, msg := range request.Body.ChatCompletions.Messages {
		// See https://github.com/vllm-project/vllm/blob/main/docs/features/multimodal_inputs.md#online-serving
		for _, block := range msg.Content.Structured {
			if block.Type == "image_url" || block.Type == "video_url" || block.Type == "input_audio" {
				return true
			}
		}
	}
	return false
}
