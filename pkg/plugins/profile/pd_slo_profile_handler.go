/*
Copyright 2025 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
)

const (
	// PdSLOProfileHandlerType is the type of the PdSLOProfileHandler
	PdSLOProfileHandlerType = "pd-slo-profile-handler"

	defaultDecodeProfileSLO  = "decode"
	defaultPrefillProfileSLO = "prefill"

	// Cycle state keys for selected pod pair
	selectedPrefillPodKey = "pd-slo-selected-prefill-pod"
	selectedDecodePodKey  = "pd-slo-selected-decode-pod"
)

type pdSLOProfileHandlerParameters struct {
	DecodeProfile      string  `json:"decodeProfile"`
	PrefillProfile     string  `json:"prefillProfile"`
	PrefixPluginName   string  `json:"prefixPluginName"`
	HashBlockSize      int     `json:"hashBlockSize"`
	TransferOverheadMs float64 `json:"transferOverheadMs"`
}

// compile-time type assertion
var _ framework.ProfileHandler = &PdSLOProfileHandler{}

// PdSLOProfileHandlerFactory defines the factory function for the PdSLOProfileHandler
func PdSLOProfileHandlerFactory(name string, rawParameters json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	parameters := pdSLOProfileHandlerParameters{
		DecodeProfile:      defaultDecodeProfileSLO,
		PrefillProfile:     defaultPrefillProfileSLO,
		PrefixPluginName:   "prefix-cache-scorer",
		HashBlockSize:      16,
		TransferOverheadMs: 5.0,
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdSLOProfileHandlerType, err)
		}
	}

	if parameters.HashBlockSize <= 0 {
		return nil, fmt.Errorf("invalid hashBlockSize: must be > 0, got %d", parameters.HashBlockSize)
	}

	if parameters.TransferOverheadMs < 0 {
		return nil, fmt.Errorf("invalid transferOverheadMs: must be >= 0, got %f", parameters.TransferOverheadMs)
	}

	return NewPdSLOProfileHandler(
		parameters.PrefillProfile,
		parameters.DecodeProfile,
		parameters.PrefixPluginName,
		parameters.HashBlockSize,
		parameters.TransferOverheadMs,
	).WithName(name), nil
}

// NewPdSLOProfileHandler initializes a new PdSLOProfileHandler and returns its pointer.
func NewPdSLOProfileHandler(
	prefillProfile string,
	decodeProfile string,
	prefixPluginName string,
	hashBlockSize int,
	transferOverheadMs float64,
) *PdSLOProfileHandler {
	return &PdSLOProfileHandler{
		typedName:          plugins.TypedName{Type: PdSLOProfileHandlerType},
		decodeProfile:      decodeProfile,
		prefillProfile:     prefillProfile,
		prefixPluginName:   prefixPluginName,
		hashBlockSize:      hashBlockSize,
		transferOverheadMs: transferOverheadMs,
	}
}

// PdSLOProfileHandler handles scheduler profiles for PD disaggregation with SLO awareness.
// It coordinates the execution of both prefill and decode profiles and uses the PD-SLO
// pair optimizer to select optimal (prefill, decode) pod pairs.
type PdSLOProfileHandler struct {
	typedName          plugins.TypedName
	decodeProfile      string
	prefillProfile     string
	prefixPluginName   string
	hashBlockSize      int
	transferOverheadMs float64
}

// TypedName returns the typed name of the plugin.
func (h *PdSLOProfileHandler) TypedName() plugins.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *PdSLOProfileHandler) WithName(name string) *PdSLOProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles.
// For SLO-aware PD scheduling, it always runs both prefill and decode profiles
// to enable joint optimization.
func (h *PdSLOProfileHandler) Pick(
	ctx context.Context,
	cycleState *types.CycleState,
	request *types.LLMRequest,
	profiles map[string]*framework.SchedulerProfile,
	profileResults map[string]*types.ProfileRunResult,
) map[string]*framework.SchedulerProfile {
	logger := log.FromContext(ctx)

	// Check if SLO headers are present
	hasSLO := h.hasSLOHeaders(request)

	if !hasSLO {
		logger.V(logutil.DEBUG).Info("No SLO headers found, not using PD-SLO scheduling")
		// TODO: Fallback to non-SLO PD scheduling logic if needed
		return map[string]*framework.SchedulerProfile{}
	}

	// Run decode profile first
	if _, executed := profileResults[h.decodeProfile]; !executed {
		logger.V(logutil.DEBUG).Info("Running decode profile for PD-SLO scheduling")
		return map[string]*framework.SchedulerProfile{
			h.decodeProfile: profiles[h.decodeProfile],
		}
	}

	// Check if decode profile succeeded
	if profileResults[h.decodeProfile] == nil {
		logger.V(logutil.DEBUG).Info("Decode profile failed, not running prefill profile")
		return map[string]*framework.SchedulerProfile{}
	}

	// Run prefill profile
	if _, executed := profileResults[h.prefillProfile]; !executed {
		logger.V(logutil.DEBUG).Info("Running prefill profile for PD-SLO scheduling")
		return map[string]*framework.SchedulerProfile{
			h.prefillProfile: profiles[h.prefillProfile],
		}
	}

	// Both profiles executed, done
	logger.V(logutil.DEBUG).Info("Both prefill and decode profiles executed")
	return map[string]*framework.SchedulerProfile{}
}

// ProcessResults handles the outcome of the profile runs after the selected profiles ran.
// It retrieves the selected pod pair from the cycle state (set by the PD-SLO pair optimizer)
// and constructs the scheduling result with the appropriate headers.
func (h *PdSLOProfileHandler) ProcessResults(
	ctx context.Context,
	cycleState *types.CycleState,
	request *types.LLMRequest,
	profileResults map[string]*types.ProfileRunResult,
) (*types.SchedulingResult, error) {
	logger := log.FromContext(ctx)

	decodeRunResults := profileResults[h.decodeProfile]
	if decodeRunResults == nil {
		return nil, errors.New("failed to find available decode workers")
	}

	prefillRunResults := profileResults[h.prefillProfile]
	if prefillRunResults == nil {
		return nil, errors.New("failed to find available prefill workers")
	}

	// Get selected pod pair from cycle state (set by PD-SLO pair optimizer)
	prefillPodName, decodePodName, err := h.getSelectedPairFromState(cycleState)
	if err != nil {
		logger.V(logutil.DEBUG).Error(err, "Failed to get selected pair from cycle state")
		// Fallback: use first pods from each profile
		if len(prefillRunResults.TargetPods) > 0 && len(decodeRunResults.TargetPods) > 0 {
			prefillPodName = prefillRunResults.TargetPods[0].GetPod().String()
			decodePodName = decodeRunResults.TargetPods[0].GetPod().String()
		} else {
			return nil, errors.New("no pods available in profile results")
		}
	}

	// Find the actual pod objects
	var selectedPrefillPod, selectedDecodePod types.Pod
	for _, pod := range prefillRunResults.TargetPods {
		if pod.GetPod().String() == prefillPodName {
			selectedPrefillPod = pod
			break
		}
	}

	for _, pod := range decodeRunResults.TargetPods {
		if pod.GetPod().String() == decodePodName {
			selectedDecodePod = pod
			break
		}
	}

	if selectedPrefillPod == nil || selectedDecodePod == nil {
		return nil, fmt.Errorf("failed to find selected pods: prefill=%s, decode=%s", prefillPodName, decodePodName)
	}

	// Set prefiller header for the decode pod to use
	prefillPodInfo := selectedPrefillPod.GetPod()
	prefillAddr := net.JoinHostPort(prefillPodInfo.Address, prefillPodInfo.Port)
	request.Headers[common.PrefillPodHeader] = prefillAddr

	logger.Info("Set prefiller header for PD-SLO scheduling",
		"prefillPod", prefillPodName,
		"decodePod", decodePodName,
		"prefillAddr", prefillAddr)

	// Build scheduling result with both profiles
	updatedResults := map[string]*types.ProfileRunResult{
		h.decodeProfile: &types.ProfileRunResult{
			TargetPods: []types.Pod{selectedDecodePod},
		},
		h.prefillProfile: &types.ProfileRunResult{
			TargetPods: []types.Pod{selectedPrefillPod},
		},
	}

	return &types.SchedulingResult{
		PrimaryProfileName: h.decodeProfile,
		ProfileResults:     updatedResults,
	}, nil
}

// Helper methods

func (h *PdSLOProfileHandler) hasSLOHeaders(request *types.LLMRequest) bool {
	_, hasTTFT := request.Headers["x-slo-ttft-ms"]
	_, hasTPOT := request.Headers["x-slo-tpot-ms"]
	return hasTTFT || hasTPOT
}

func (h *PdSLOProfileHandler) getSelectedPairFromState(cycleState *types.CycleState) (prefillPodName, decodePodName string, err error) {
	prefillData, err := cycleState.Read(plugins.StateKey(selectedPrefillPodKey))
	if err != nil {
		return "", "", fmt.Errorf("failed to read selected prefill pod from cycle state: %w", err)
	}

	decodeData, err := cycleState.Read(plugins.StateKey(selectedDecodePodKey))
	if err != nil {
		return "", "", fmt.Errorf("failed to read selected decode pod from cycle state: %w", err)
	}

	prefillPodName, ok := prefillData.(string)
	if !ok {
		return "", "", fmt.Errorf("selected prefill pod is not a string")
	}

	decodePodName, ok = decodeData.(string)
	if !ok {
		return "", "", fmt.Errorf("selected decode pod is not a string")
	}

	return prefillPodName, decodePodName, nil
}
