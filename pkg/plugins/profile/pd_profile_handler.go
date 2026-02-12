// Package profile provides profile handler plugin for the epp.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/util/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/scheduling/scorer/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

const (
	// PdProfileHandlerType is the type of the PdProfileHandler
	PdProfileHandlerType = "pd-profile-handler"

	defaultDecodeProfile     = "decode"
	defaultPrefillProfile    = "prefill"
	defaultPrefixPluginType  = prefix.PrefixCachePluginType
	defaultDeciderPluginName = AlwaysDisaggDeciderPluginType

	// AverageCharactersPerToken is an estimated average characters per token, used since the request we cached is not tokenized.
	AverageCharactersPerToken = 4
)

// pdDeciderPlugin interface for pd decider plugins
type pdDeciderPlugin interface {
	plugin.Plugin
	// disaggregate checks if disaggregated PD is required for the given request and endpoint.
	disaggregate(ctx context.Context, inputTokens int, endpoint scheduling.Endpoint) bool
}

type pdProfileHandlerParameters struct {
	DecodeProfile     string `json:"decodeProfile"`
	PrefillProfile    string `json:"prefillProfile"`
	PrefixPluginType  string `json:"prefixPluginType"`
	PrefixPluginName  string `json:"prefixPluginName"`
	PrimaryPort       int    `json:"primaryPort"`
	DeciderPluginName string `json:"deciderPluginName"`
}

// compile-time type assertion
var _ scheduling.ProfileHandler = &PdProfileHandler{}

// PdProfileHandlerFactory defines the factory function for the PdProfileHandler
func PdProfileHandlerFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := pdProfileHandlerParameters{
		DecodeProfile:     defaultDecodeProfile,
		PrefillProfile:    defaultPrefillProfile,
		PrefixPluginType:  defaultPrefixPluginType,
		PrimaryPort:       0,
		DeciderPluginName: defaultDeciderPluginName,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdProfileHandlerType, err)
		}
	}

	if parameters.PrefixPluginName == "" {
		parameters.PrefixPluginName = parameters.PrefixPluginType
	}

	if parameters.PrimaryPort != 0 {
		if parameters.PrimaryPort < 1 || parameters.PrimaryPort > 65535 {
			return nil, fmt.Errorf("invalid primaryPort: must be between 1 and 65535, got %d", parameters.PrimaryPort)
		}
	}

	if parameters.DeciderPluginName == "" {
		return nil, errors.New("decider plugin name is not defined")
	}

	plugin := handle.Plugin(parameters.DeciderPluginName)
	if plugin == nil {
		return nil, fmt.Errorf("invalid decider plugin type: %s", parameters.DeciderPluginName)
	}

	deciderPlugin, ok := plugin.(pdDeciderPlugin)
	if !ok {
		return nil, fmt.Errorf("decider plugin of type: %s does not implement pdDeciderPlugin", parameters.DeciderPluginName)
	}

	handler, err := NewPdProfileHandler(parameters.PrefillProfile, parameters.DecodeProfile, parameters.PrefixPluginType, parameters.PrefixPluginName,
		parameters.PrimaryPort, deciderPlugin)

	if err != nil {
		return nil, err
	}

	return handler.WithName(name), nil

}

// NewPdProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewPdProfileHandler(prefillProfile, decodeProfile, prefixPluginType, prefixPluginName string,
	primaryPort int, deciderPlugin pdDeciderPlugin) (*PdProfileHandler, error) {
	result := &PdProfileHandler{
		typedName:             plugin.TypedName{Type: PdProfileHandlerType},
		prefixPluginTypedName: plugin.TypedName{Type: prefixPluginType, Name: prefixPluginName},
		decodeProfile:         decodeProfile,
		prefillProfile:        prefillProfile,
		decider:               deciderPlugin,
	}
	if primaryPort != 0 {
		result.primaryPort = strconv.Itoa(primaryPort)
	}

	return result, nil
}

// PdProfileHandler handles scheduler profiles for PD.
type PdProfileHandler struct {
	typedName             plugin.TypedName
	prefixPluginTypedName plugin.TypedName
	decodeProfile         string
	prefillProfile        string
	primaryPort           string
	decider               pdDeciderPlugin
}

// TypedName returns the typed name of the plugin.
func (h *PdProfileHandler) TypedName() plugin.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *PdProfileHandler) WithName(name string) *PdProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
func (h *PdProfileHandler) Pick(ctx context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest, profiles map[string]scheduling.SchedulerProfile,
	profileResults map[string]*scheduling.ProfileRunResult) map[string]scheduling.SchedulerProfile {
	// Start tracing span for profile picking operation
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "llm_d.epp.pd.profile_handler.pick",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	// Set initial attributes
	span.SetAttributes(
		attribute.Int("llm_d.profile_handler.total_profiles", len(profiles)),
		attribute.Int("llm_d.profile_handler.executed_profiles", len(profileResults)),
	)

	// Set optional request attributes if request is not nil
	if request != nil {
		if request.TargetModel != "" {
			span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
		}
		if request.RequestId != "" {
			span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestId))
		}
	}

	if _, executed := profileResults[h.decodeProfile]; !executed {
		// if decode profile was not executed yet, first let the scheduler run the decode profile
		span.SetAttributes(
			attribute.String("llm_d.profile_handler.decision", "run_decode"),
			attribute.String("llm_d.profile_handler.selected_profile", h.decodeProfile),
		)
		return map[string]scheduling.SchedulerProfile{
			h.decodeProfile: profiles[h.decodeProfile],
		}
	}
	// otherwise, decode was already executed.

	// when a profile run fails its result value is nil. we need to check decode result before continuing to prefill
	// check if all configured profiles have been executed, or if decode failed, no need to run more profiles.
	if len(profiles) == len(profileResults) || profileResults[h.decodeProfile] == nil {
		span.SetAttributes(
			attribute.String("llm_d.profile_handler.decision", "complete"),
			attribute.Bool("llm_d.profile_handler.decode_failed", profileResults[h.decodeProfile] == nil),
		)
		return map[string]scheduling.SchedulerProfile{}
	}

	inputTokens, err := getUserInputLenInTokens(request)
	if err != nil {
		log.FromContext(ctx).V(logutil.DEBUG).Error(err, "Failed to get user input")
		span.SetStatus(codes.Error, err.Error())
		return nil
	}

	span.SetAttributes(attribute.Int("llm_d.profile_handler.input_tokens", inputTokens))

	if h.decider != nil && h.decider.disaggregate(ctx, inputTokens, profileResults[h.decodeProfile].TargetEndpoints[0]) {
		metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypePrefillDecode)
		// run the prefill profile
		span.SetAttributes(
			attribute.String("llm_d.profile_handler.decision", "prefill_decode"),
			attribute.String("llm_d.profile_handler.selected_profile", h.prefillProfile),
		)
		return map[string]scheduling.SchedulerProfile{
			h.prefillProfile: profiles[h.prefillProfile],
		}
	}

	metrics.RecordPDDecision(request.TargetModel, metrics.DecisionTypeDecodeOnly)
	span.SetAttributes(
		attribute.String("llm_d.profile_handler.decision", "decode_only"),
	)
	return map[string]scheduling.SchedulerProfile{} // do not run prefill
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// In case of an error in any of the profiles, the matching entry in the profileResults will contain nil, to indicate there was
// an error while running the profile.
func (h *PdProfileHandler) ProcessResults(_ context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest,
	profileResults map[string]*scheduling.ProfileRunResult) (*scheduling.SchedulingResult, error) {
	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil { // if decode profile failed to run, we should fail
		return nil, errors.New("failed to find available decode workers")
	}
	// otherwise, decode ran successfully

	updatedResults := map[string]*scheduling.ProfileRunResult{}

	// Add decode profile to result
	if h.primaryPort != "" {
		// Data Parallel is active

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

	// if both prefill and decode ran successfully
	if prefillRunResult, exists := profileResults[h.prefillProfile]; exists && prefillRunResult != nil {
		// Add the prefill profile to the results
		updatedResults[h.prefillProfile] = prefillRunResult
	}

	return &scheduling.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}

// returns length of user input in tokens
func getUserInputLenInTokens(request *scheduling.LLMRequest) (int, error) {
	if request.Body.Completions != nil { // assumed to be valid if not nil
		return len([]byte(request.Body.Completions.Prompt)) / AverageCharactersPerToken, nil
	}

	// must be chat-completions request at this point, return bytes of entire messages
	prompt, err := json.Marshal(request.Body.ChatCompletions.Messages)

	if err != nil {
		return 0, err
	}

	return len(prompt) / AverageCharactersPerToken, nil
}
