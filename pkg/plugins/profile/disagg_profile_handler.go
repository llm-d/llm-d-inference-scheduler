// Package profile provides profile handler plugins for the epp.
package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

// ── Constants ───────────────────────────────────────────────────────────────

const (
	// DisaggProfileHandlerType is the canonical type for the unified disaggregation profile handler.
	DisaggProfileHandlerType = "disagg-profile-handler"
	defaultDeciderPluginName = AlwaysDisaggDeciderPluginType

	defaultDecodeProfile  = "decode"
	defaultPrefillProfile = "prefill"
	defaultEncodeProfile  = "encode"

	// AverageCharactersPerToken is an estimated average characters per token,
	// used since the request we cache is not tokenized.
	AverageCharactersPerToken = 4
)

// ── Interfaces ──────────────────────────────────────────────────────────────

// deciderPlugin is the shared interface for all disaggregation deciders.
type deciderPlugin interface {
	plugin.Plugin
	decide(ctx context.Context, request *scheduling.LLMRequest, endpoint scheduling.Endpoint) bool
}

// ── Factory & constructor ────────────────────────────────────────────────────

type disaggProfileHandlerParameters struct {
	DecodeProfile            string `json:"decodeProfile"`
	PrefillProfile           string `json:"prefillProfile"`
	EncodeProfile            string `json:"encodeProfile"`
	PrimaryPort              int    `json:"primaryPort"`
	PrefillDeciderPluginName string `json:"prefillDeciderPluginName"`
	EncodeDeciderPluginName  string `json:"encodeDeciderPluginName"`
}

// DisaggProfileHandlerFactory is the unified factory for all disaggregation
// profile handlers. Active stages are determined by which profiles are configured:
//   - Set prefillProfile + optional deciderPlugin for P/D
//   - Set encodeProfile (+ optional encodeDeciderPluginName) for E/PD
//   - Set both for E/P/D
//   - Omit both for decode-only
func DisaggProfileHandlerFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	parameters := disaggProfileHandlerParameters{
		DecodeProfile: defaultDecodeProfile,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse parameters of the disagg-profile-handler - %w", err)
		}
	}

	if parameters.PrefillProfile == "" {
		parameters.PrefillProfile = defaultPrefillProfile
	}
	if parameters.EncodeProfile == "" {
		parameters.EncodeProfile = defaultEncodeProfile
	}

	if parameters.PrimaryPort < 0 || parameters.PrimaryPort > 65535 {
		return nil, fmt.Errorf("invalid primaryPort: must be between 1 and 65535, got %d", parameters.PrimaryPort)
	}

	// Resolve PD decider (required when prefill is active).
	var pdDecider deciderPlugin
	if parameters.PrefillDeciderPluginName != "" {
		p := handle.Plugin(parameters.PrefillDeciderPluginName)
		if p == nil {
			return nil, fmt.Errorf("prefillDeciderPluginName not found: %s", parameters.PrefillDeciderPluginName)
		}
		var ok bool
		pdDecider, ok = p.(deciderPlugin)
		if !ok {
			return nil, fmt.Errorf("plugin %s does not implement prefillDeciderPlugin", parameters.PrefillDeciderPluginName)
		}
	}
	// Resolve encode decider (optional).
	var encodeDecider deciderPlugin
	if parameters.EncodeDeciderPluginName != "" {
		ep := handle.Plugin(parameters.EncodeDeciderPluginName)
		if ep == nil {
			return nil, fmt.Errorf("encodeDeciderPluginName not found: %s", parameters.EncodeDeciderPluginName)
		}
		var ok bool
		encodeDecider, ok = ep.(deciderPlugin)
		if !ok {
			return nil, fmt.Errorf("plugin %s does not implement encodeDeciderPlugin", parameters.EncodeDeciderPluginName)
		}
	}
	handler := NewDisaggProfileHandler(
		parameters.DecodeProfile, parameters.PrefillProfile, parameters.EncodeProfile,
		parameters.PrimaryPort,
		pdDecider, encodeDecider,
	)
	return handler.WithName(name), nil
}

// NewDisaggProfileHandler creates a DisaggProfileHandler directly.
// Active stages are determined by which profile names are non-empty.
func NewDisaggProfileHandler(
	decodeProfile, prefillProfile, encodeProfile string,
	primaryPort int,
	pdDecider deciderPlugin,
	encodeDecider deciderPlugin,
) *DisaggProfileHandler {
	return newDisaggProfileHandler(
		DisaggProfileHandlerType,
		decodeProfile, prefillProfile, encodeProfile,
		primaryPort,
		pdDecider, encodeDecider,
	)
}

// ── Shared implementation ───────────────────────────────────────────────────

// compile-time assertion
var _ scheduling.ProfileHandler = &DisaggProfileHandler{}

// DisaggProfileHandler is the unified disaggregation profile handler.
// It drives one or more of the following stages, each optional except decode:
//
//   - Encode  (E): schedules encoder pods for multimodal content
//   - Prefill (P): schedules a prefill pod for KV-cache disaggregation
//   - Decode  (D): schedules the decode pod (always runs first)
//
// All three handler types (P/D, E/PD, E/P/D) share this single implementation;
// active stages are selected by setting encodeProfile / prefillProfile.
type DisaggProfileHandler struct {
	typedName      plugin.TypedName
	decodeProfile  string
	prefillProfile string
	encodeProfile  string
	pdDecider      deciderPlugin
	encodeDecider  deciderPlugin
	primaryPort    string
}

// TypedName returns the typed name of the plugin.
func (h *DisaggProfileHandler) TypedName() plugin.TypedName { return h.typedName }

// WithName sets the instance name of the plugin.
func (h *DisaggProfileHandler) WithName(name string) *DisaggProfileHandler {
	h.typedName.Name = name
	return h
}

func newDisaggProfileHandler(
	handlerType string,
	decodeProfile, prefillProfile, encodeProfile string,
	primaryPort int,
	pdDecider deciderPlugin,
	encodeDecider deciderPlugin,
) *DisaggProfileHandler {
	h := &DisaggProfileHandler{
		typedName:      plugin.TypedName{Type: handlerType},
		decodeProfile:  decodeProfile,
		prefillProfile: prefillProfile,
		encodeProfile:  encodeProfile,
		pdDecider:      pdDecider,
		encodeDecider:  encodeDecider,
	}
	if primaryPort != 0 {
		h.primaryPort = strconv.Itoa(primaryPort)
	}
	return h
}

// Pick implements scheduling.ProfileHandler.
// Stages run in order: decode → encode (optional) → prefill (optional).
// Returns the next profile to execute, or an empty map when all stages are done.
func (h *DisaggProfileHandler) Pick(
	ctx context.Context,
	_ *scheduling.CycleState,
	request *scheduling.LLMRequest,
	profiles map[string]scheduling.SchedulerProfile,
	profileResults map[string]*scheduling.ProfileRunResult,
) map[string]scheduling.SchedulerProfile {
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "llm_d.epp.disagg.profile_handler.pick",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	if request == nil {
		span.SetAttributes(attribute.String("llm_d.profile_handler.decision", "complete_nil_request"))
		return map[string]scheduling.SchedulerProfile{}
	}

	if request.TargetModel != "" {
		span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
	}
	if request.RequestId != "" {
		span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestId))
	}

	// ── Stage 1: Decode ────────────────────────────────────────────────────
	if _, executed := profileResults[h.decodeProfile]; !executed {
		span.SetAttributes(attribute.String("llm_d.profile_handler.decision", "run_decode"))
		return map[string]scheduling.SchedulerProfile{h.decodeProfile: profiles[h.decodeProfile]}
	}

	decodeRes := profileResults[h.decodeProfile]
	if decodeRes == nil || len(decodeRes.TargetEndpoints) == 0 {
		span.SetAttributes(
			attribute.String("llm_d.profile_handler.decision", "complete"),
			attribute.Bool("llm_d.profile_handler.decode_failed", true),
		)
		return map[string]scheduling.SchedulerProfile{}
	}

	// ── Stage 2: Encode (optional) ─────────────────────────────────────────
	if _, hasEncodeProfile := profiles[h.encodeProfile]; hasEncodeProfile {
		if _, executed := profileResults[h.encodeProfile]; !executed {
			if h.encodeDecider != nil && h.encodeDecider.decide(ctx, request, decodeRes.TargetEndpoints[0]) {
				span.SetAttributes(attribute.String("llm_d.profile_handler.decision", "run_encode"))
				return map[string]scheduling.SchedulerProfile{h.encodeProfile: profiles[h.encodeProfile]}
			}
			// Decider rejected encode — skip the encode stage entirely.
			span.SetAttributes(attribute.String("llm_d.profile_handler.decision", "skip_encode"))
		}
	}

	// ── Stage 3: Prefill (optional) ────────────────────────────────────────
	if _, hasPrefillProfile := profiles[h.prefillProfile]; hasPrefillProfile {
		if _, executed := profileResults[h.prefillProfile]; !executed {
			if h.pdDecider != nil && h.pdDecider.decide(ctx, request, decodeRes.TargetEndpoints[0]) {
				span.SetAttributes(attribute.String("llm_d.profile_handler.decision", "run_prefill"))
				return map[string]scheduling.SchedulerProfile{h.prefillProfile: profiles[h.prefillProfile]}
			}
		}
	}

	// ── All stages done: record routing decision ───────────────────────────
	encodeUsed := profileResults[h.encodeProfile] != nil
	prefillUsed := profileResults[h.prefillProfile] != nil

	var decision string
	switch {
	case encodeUsed && prefillUsed:
		decision = metrics.DecisionTypeEncodePrefillDecode
	case encodeUsed:
		decision = metrics.DecisionTypeEncodeDecode
	case prefillUsed:
		decision = metrics.DecisionTypePrefillDecode
	default:
		decision = metrics.DecisionTypeDecodeOnly
	}
	metrics.RecordPDDecision(request.TargetModel, decision)
	span.SetAttributes(attribute.String("llm_d.profile_handler.decision", "complete_"+decision))

	return map[string]scheduling.SchedulerProfile{}
}

// ProcessResults implements scheduling.ProfileHandler.
// Builds the final SchedulingResult from whichever stages ran successfully.
func (h *DisaggProfileHandler) ProcessResults(
	_ context.Context,
	_ *scheduling.CycleState,
	request *scheduling.LLMRequest,
	profileResults map[string]*scheduling.ProfileRunResult,
) (*scheduling.SchedulingResult, error) {
	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil {
		return nil, errors.New("failed to find available decode workers")
	}

	updatedResults := map[string]*scheduling.ProfileRunResult{}

	if h.primaryPort != "" {
		// Data-parallel: rewrite decode endpoint port; stash original in a header.
		targetEndpoint := decodeRunResults.TargetEndpoints[0].GetMetadata()
		request.Headers[common.DataParallelEndpointHeader] = net.JoinHostPort(targetEndpoint.Address, targetEndpoint.Port)

		updated := scheduling.ProfileRunResult{}
		for _, target := range decodeRunResults.TargetEndpoints {
			info := target.GetMetadata().Clone()
			info.Port = h.primaryPort
			updated.TargetEndpoints = append(updated.TargetEndpoints,
				scheduling.NewEndpoint(info, target.GetMetrics().Clone(), nil))
		}
		updatedResults[h.decodeProfile] = &updated
	} else {
		updatedResults[h.decodeProfile] = decodeRunResults
	}

	if h.pdDecider != nil {
		if prefillRes, ok := profileResults[h.prefillProfile]; ok && prefillRes != nil {
			updatedResults[h.prefillProfile] = prefillRes
		}
	}

	if h.encodeDecider != nil {
		if encodeRes, ok := profileResults[h.encodeProfile]; ok && encodeRes != nil {
			updatedResults[h.encodeProfile] = encodeRes
		}
	}

	return &scheduling.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}
