package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strconv"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common"
)

const (
	// DataParallelProfileHandlerType is the type of the DataParallelProfileHandler
	DataParallelProfileHandlerType = "data-parallel-profile-handler"
)

type dataParallelProfileHandlerParameters struct {
	PrimaryPort int `json:"primaryPort"`
}

// compile-time type assertion
var _ scheduling.ProfileHandler = &DataParallelProfileHandler{}

// DataParallelProfileHandlerFactory defines the factory function for the DataParallelProfileHandler
func DataParallelProfileHandlerFactory(name string, rawParameters json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	parameters := dataParallelProfileHandlerParameters{
		PrimaryPort: 8000,
	}
	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the '%s' profile handler - %w", PdProfileHandlerType, err)
		}
	}

	if parameters.PrimaryPort != 0 {
		if parameters.PrimaryPort < 1 || parameters.PrimaryPort > 65535 {
			return nil, fmt.Errorf("invalid primaryPort: must be between 1 and 65535, got %d", parameters.PrimaryPort)
		}
	}

	return NewDataParallelProfileHandler(parameters.PrimaryPort).WithName(name), nil
}

// NewDataParallelProfileHandler initializes a new PdProfileHandler and returns its pointer.
func NewDataParallelProfileHandler(primaryPort int) *DataParallelProfileHandler {
	return &DataParallelProfileHandler{
		typedName:   plugin.TypedName{Type: DataParallelProfileHandlerType},
		primaryPort: strconv.Itoa(primaryPort),
	}
}

// DataParallelProfileHandler handles scheduler profiles for Data Parallel.
type DataParallelProfileHandler struct {
	typedName   plugin.TypedName
	primaryPort string
}

// TypedName returns the typed name of the plugin.
func (h *DataParallelProfileHandler) TypedName() plugin.TypedName {
	return h.typedName
}

// WithName sets the name of the plugin.
func (h *DataParallelProfileHandler) WithName(name string) *DataParallelProfileHandler {
	h.typedName.Name = name
	return h
}

// Pick selects the SchedulingProfiles to run from the list of candidate profiles, while taking into consideration the request properties and the
// previously executed cycles along with their results.
func (h *DataParallelProfileHandler) Pick(ctx context.Context, _ *scheduling.CycleState, _ *scheduling.LLMRequest, profiles map[string]scheduling.SchedulerProfile,
	profileResults map[string]*scheduling.ProfileRunResult) map[string]scheduling.SchedulerProfile {
	if len(profiles) == len(profileResults) { // all profiles have been executed already in previous call
		return map[string]scheduling.SchedulerProfile{}
	}
	// Validate that only one profile is configured for Data Parallel mode
	if len(profiles) != 1 {
		log.FromContext(ctx).Error(nil, "Data Parallel profile handler requires exactly one scheduling profile",
			"profileCount", len(profiles),
		)
		return map[string]scheduling.SchedulerProfile{} // return empty map for fast exit in later steps
	}
	// return only one profile
	return profiles
}

// ProcessResults handles the outcome of the profile runs after all profiles ran.
// It may aggregate results, log test profile outputs, or apply custom logic. It specifies in the SchedulingResult the
// key of the primary profile that should be used to get the request selected destination.
// When a profile run fails, its result in the profileResults map is nil.
func (h *DataParallelProfileHandler) ProcessResults(_ context.Context, _ *scheduling.CycleState, request *scheduling.LLMRequest,
	profileResults map[string]*scheduling.ProfileRunResult) (*scheduling.SchedulingResult, error) {
	if len(profileResults) != 1 {
		return nil, errors.New("data parallel profile handler is intended to be used with a single profile, failed to process multiple profiles")
	}

	var singleProfileName string
	for profileName := range profileResults {
		singleProfileName = profileName
		break
	}

	profileResult := profileResults[singleProfileName]
	if profileResult == nil { // there was an error while running the profile
		return nil, fmt.Errorf("failed to run scheduler profile '%s'", singleProfileName)
	}

	newResult := scheduling.ProfileRunResult{
		TargetEndpoints: []scheduling.Endpoint{},
	}

	targetPod := profileResult.TargetEndpoints[0].GetMetadata()

	request.Headers[common.DataParallelPodHeader] = net.JoinHostPort(targetPod.Address, targetPod.Port)

	for _, target := range profileResult.TargetEndpoints {
		newMetadata := target.GetMetadata().Clone()
		newMetadata.Port = h.primaryPort
		targetEndpoint := scheduling.NewEndpoint(newMetadata, target.GetMetrics().Clone(), nil)
		newResult.TargetEndpoints = append(newResult.TargetEndpoints, targetEndpoint)
	}
	modifiedResults := map[string]*scheduling.ProfileRunResult{singleProfileName: &newResult}

	return &scheduling.SchedulingResult{
		ProfileResults:     modifiedResults,
		PrimaryProfileName: singleProfileName,
	}, nil
}
