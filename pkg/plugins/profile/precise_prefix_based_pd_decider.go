package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/common/observability/logging"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/scheduling"
)

const (
	// PrecisePrefixBasedPDDeciderPluginType is the type-name of the precisePrefixBasedPDDecider plugin.
	PrecisePrefixBasedPDDeciderPluginType = "precise-prefix-based-pd-decider"
)

// PrecisePrefixBasedPDDeciderConfig holds the configuration for the precisePrefixBasedPDDecider plugin.
type PrecisePrefixBasedPDDeciderConfig struct {
	// NonCachedTokens non cached minimum tokens that triggers disaggregated PD
	NonCachedTokens int `json:"nonCachedTokens"`
}

func (p PrecisePrefixBasedPDDeciderConfig) validate() error {
	if p.NonCachedTokens < 0 {
		return errors.New("nonCachedTokens parameter of precise prefix disaggregation decider cannot be negative")
	}

	return nil
}

// compile-time type assertion
var _ pdDeciderPlugin = &PrecisePrefixBasedPDDecider{}

// PrecisePrefixBasedPDDecider is a PD decider plugin which decision is based on precise prefix cache awareness
// leveraging llm-d-kv-cache for accurate prefix matching.
type PrecisePrefixBasedPDDecider struct {
	typedName plugin.TypedName
	config    PrecisePrefixBasedPDDeciderConfig
}

// PrecisePrefixBasedPDDeciderPluginFactory defines the factory function for creating
// a new instance of the precisePrefixBasedPDDecider.
func PrecisePrefixBasedPDDeciderPluginFactory(name string, rawParameters json.RawMessage,
	handle plugin.Handle) (plugin.Plugin, error) {
	config := PrecisePrefixBasedPDDeciderConfig{
		NonCachedTokens: 0,
	}

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &config); err != nil {
			return nil, fmt.Errorf("failed to parse %s plugin config: %w", PrecisePrefixBasedPDDeciderPluginType, err)
		}
	}

	decider, err := NewPrecisePrefixBasedPDDecider(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create %s plugin: %w", PrecisePrefixBasedPDDeciderPluginType, err)
	}

	return decider.WithName(name), nil
}

// NewPrecisePrefixBasedPDDecider initializes a NewPrecisePrefixBasedPDDecider precise prefix based PD decider Plugin and returns its pointer.
// If the configuration is invalid an error is returned.
func NewPrecisePrefixBasedPDDecider(config PrecisePrefixBasedPDDeciderConfig) (*PrecisePrefixBasedPDDecider, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	return &PrecisePrefixBasedPDDecider{
		config: config,
	}, nil
}

// TypedName returns the typed name of the plugin.
func (d *PrecisePrefixBasedPDDecider) TypedName() plugin.TypedName {
	return d.typedName
}

// WithName sets the name of the plugin.
func (d *PrecisePrefixBasedPDDecider) WithName(name string) *PrecisePrefixBasedPDDecider {
	d.typedName.Name = name
	return d
}

func (d *PrecisePrefixBasedPDDecider) disaggregate(ctx context.Context, inputTokens int, endpoint scheduling.Endpoint) bool {
	logger := log.FromContext(ctx)
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG)

	if d.config.NonCachedTokens <= 0 { // always use disaggregation in case of non cached tokens number is 0
		return true
	}
	if endpoint == nil {
		logger.Error(nil, "precise prefix decider: endpoint is nil")
		return false
	}
	if inputTokens < d.config.NonCachedTokens {
		debugLogger.Info("Input is shorter than the nonCachedToken, no disaggregated PD")
		return false
	}
	// inspect the decode endpoint to decide if prefill should run or not.
	// if the non-cached part is short enough - no disaggregation.
	// This uses precise prefix cache information from the llm-d-kv-cache index.
	prefixInfoRaw, ok := endpoint.Get(prefix.PrefixCacheMatchInfoKey)
	if !ok || prefixInfoRaw == nil {
		logger.Error(nil, "unable to read precise prefix cache state")
		return false
	}
	prefixCacheMatchInfo, ok := prefixInfoRaw.(*prefix.PrefixCacheMatchInfo)
	if !ok {
		logger.Error(nil, "wrong type of prefix cache match info")
		return false
	}

	// number of cached tokens (from precise KV-cache index)
	hitPrefixTokens := prefixCacheMatchInfo.MatchBlocks() * prefixCacheMatchInfo.BlockSizeTokens()
	// length of non-cached suffix in tokens
	nonCachedTokens := inputTokens - hitPrefixTokens

	debugLogger.Info("Computed hit percentage for precise prefix cache",
		"absolute hit prefix len (tokens)", hitPrefixTokens,
		"prompt length (token)", inputTokens,
		"non-cached tokens", nonCachedTokens)

	if nonCachedTokens < d.config.NonCachedTokens {
		debugLogger.Info("Non-cached suffix is smaller than threshold, using decode profile only")
		return false // do not run prefill
	}

	return true
}

// Consumes defines data types consumed by this plugin
func (*PrecisePrefixBasedPDDecider) Consumes() map[string]any {
	return map[string]any{prefix.PrefixCacheMatchInfoKey: prefix.PrefixCacheMatchInfo{}}
}
