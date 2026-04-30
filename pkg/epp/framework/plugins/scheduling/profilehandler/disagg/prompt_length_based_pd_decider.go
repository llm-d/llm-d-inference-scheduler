package disagg

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

const (
	// PromptLengthBasedPDDeciderPluginType is the type-name of the promptLengthBasedPDDecider plugin.
	PromptLengthBasedPDDeciderPluginType = "prompt-length-based-pd-decider"
)

// PromptLengthBasedPDDeciderConfig holds the configuration for the promptLengthBasedPDDecider plugin.
type PromptLengthBasedPDDeciderConfig struct {
	// PromptTokens minimum prompt tokens that triggers disaggregated PD.
	PromptTokens int `json:"promptTokens"`
}

func (p PromptLengthBasedPDDeciderConfig) validate() error {
	if p.PromptTokens < 0 {
		return errors.New("promptTokens parameter of prompt length disaggregation decider cannot be negative")
	}

	return nil
}

// compile-time type assertion
var _ deciderPlugin = &PromptLengthBasedPDDecider{}

// PromptLengthBasedPDDecider is a PD decider plugin which decision is based on prompt length.
type PromptLengthBasedPDDecider struct {
	typedName plugin.TypedName
	config    PromptLengthBasedPDDeciderConfig
}

// PromptLengthBasedPDDeciderPluginFactory defines the factory function for creating
// a new instance of the promptLengthBasedPDDecider.
func PromptLengthBasedPDDeciderPluginFactory(name string, rawParameters json.RawMessage,
	handle plugin.Handle) (plugin.Plugin, error) {
	config := PromptLengthBasedPDDeciderConfig{
		PromptTokens: 0,
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &config); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", PromptLengthBasedPDDeciderPluginType, err)
		}
	}

	decider, err := NewPromptLengthBasedPDDecider(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", PromptLengthBasedPDDeciderPluginType, err)
	}

	return decider.WithName(name), nil
}

// NewPromptLengthBasedPDDecider initializes a prompt length based PD decider Plugin and returns its pointer.
// If the configuration is invalid an error is returned.
func NewPromptLengthBasedPDDecider(config PromptLengthBasedPDDeciderConfig) (*PromptLengthBasedPDDecider, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	if config.PromptTokens == 0 {
		log.Log.Info("Prompt length based PD disabled (PromptTokens=0)")
	}

	return &PromptLengthBasedPDDecider{
		typedName: plugin.TypedName{Type: PromptLengthBasedPDDeciderPluginType},
		config:    config,
	}, nil
}

// TypedName returns the typed name of the plugin.
func (d *PromptLengthBasedPDDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *PromptLengthBasedPDDecider) WithName(name string) *PromptLengthBasedPDDecider {
	d.typedName.Name = name
	return d
}

func (d *PromptLengthBasedPDDecider) disaggregate(ctx context.Context, request *scheduling.InferenceRequest, endpoint scheduling.Endpoint) bool {
	debugLogger := log.FromContext(ctx).V(logging.DEBUG)

	// PromptTokens defines the minimum prompt length in tokens required
	// to trigger disaggregated PD. A value of 0 disables disaggregation.
	if d.config.PromptTokens == 0 {
		return false
	}

	inputTokens, err := getUserInputLenInTokens(request)
	if err != nil {
		log.FromContext(ctx).Error(err, "prompt length decider: failed to get user input length in tokens")
		return false
	}

	debugLogger.Info("Computed prompt length for PD decision",
		"prompt length (token)", inputTokens,
		"threshold (token)", d.config.PromptTokens)

	return inputTokens >= d.config.PromptTokens
}
