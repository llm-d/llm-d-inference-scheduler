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
	// NonCachedTokens is the base threshold for non-cached tokens that triggers disaggregated PD.
	// The threshold is quality-adjusted: adjustedThreshold = NonCachedTokens * cacheQuality
	// where cacheQuality = weightedScore / matchBlocks
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

// PrecisePrefixBasedPDDecider is a PD decider plugin which makes disaggregation decisions based on
// device-tier-aware prefix cache match quality from PrecisePrefixCacheMatchInfo.
// It applies quality-adjusted thresholding: adjustedThreshold = baseThreshold * (weightedScore / matchBlocks)
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

// NewPrecisePrefixBasedPDDecider initializes a new PrecisePrefixBasedPDDecider prefix based PD decider Plugin and returns its pointer.
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

	if d.config.NonCachedTokens <= 0 { // always use disaggregation in case of threshold is 0
		return true
	}
	if endpoint == nil {
		logger.Error(nil, "precise prefix decider: endpoint is nil")
		return false
	}
	if inputTokens < d.config.NonCachedTokens {
		debugLogger.Info("Input is shorter than the base threshold, no disaggregated PD")
		return false
	}

	// Inspect the decode endpoint to decide if prefill should run or not
	prefixInfoRaw, ok := endpoint.Get(prefix.PrecisePrefixCacheMatchInfoKey)
	if !ok || prefixInfoRaw == nil {
		debugLogger.Info("No precise prefix cache state found, falling back to disaggregation")
		return true // Default to disaggregation when no cache info available
	}

	precisePrefixCacheMatchInfo, ok := prefixInfoRaw.(*prefix.PrecisePrefixCacheMatchInfo)
	if !ok {
		logger.Error(nil, "wrong type of precise prefix cache match info")
		return true // Default to disaggregation on error
	}

	matchBlocks := precisePrefixCacheMatchInfo.MatchBlocks()
	blockSizeTokens := precisePrefixCacheMatchInfo.BlockSizeTokens()
	weightedScore := precisePrefixCacheMatchInfo.WeightedScore()

	// Number of cached tokens
	cachedTokens := matchBlocks * blockSizeTokens

	// Calculate cache quality (device-tier-adjusted hit ratio)
	var cacheQuality float64
	if matchBlocks > 0 {
		cacheQuality = weightedScore / float64(matchBlocks)
	} else {
		cacheQuality = 0.0
	}

	// Quality-adjusted threshold
	adjustedThreshold := float64(d.config.NonCachedTokens) * cacheQuality

	// Length of non-cached suffix in tokens
	nonCachedTokens := inputTokens - cachedTokens

	debugLogger.Info("Precise prefix-based disaggregation decision",
		"matchBlocks", matchBlocks,
		"blockSizeTokens", blockSizeTokens,
		"weightedScore", weightedScore,
		"cachedTokens", cachedTokens,
		"inputTokens", inputTokens,
		"nonCachedTokens", nonCachedTokens,
		"cacheQuality", cacheQuality,
		"baseThreshold", d.config.NonCachedTokens,
		"adjustedThreshold", adjustedThreshold)

	if float64(nonCachedTokens) < adjustedThreshold {
		debugLogger.Info("Non-cached suffix is smaller than quality-adjusted threshold, using decode profile only")
		return false // Do not run prefill
	}

	debugLogger.Info("Non-cached suffix exceeds quality-adjusted threshold, using disaggregated PD")
	return true
}

// Consumes defines data types consumed by this plugin
func (*PrecisePrefixBasedPDDecider) Consumes() map[string]any {
	return map[string]any{prefix.PrecisePrefixCacheMatchInfoKey: prefix.PrecisePrefixCacheMatchInfo{}}
}
