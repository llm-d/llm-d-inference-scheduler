package profile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/datalayer/plugins/approximateprefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/types"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

// compile-time type assertion
var _ pdDecider = &PrefixDisaggregationDecider{}

// PrefixDeciderName name of the prefix decider
const PrefixDeciderName = "prefix-disaggregation-decider"

type prefixDisaggregationDeciderParameters struct {
	// NonCachedTokens non cached tokens limit that triggers disaggregated PD
	NonCachedTokens int `json:"nonCachedTokens"`
	// PluginName prefix plugin name, optional, should be defined if prefix plugin name is not a default
	PluginName string `json:"pluginName"`
}

var defaultParams = prefixDisaggregationDeciderParameters{
	NonCachedTokens: 0,
	PluginName:      prefix.PrefixCachePluginType,
}

func (p prefixDisaggregationDeciderParameters) validate() error {
	if p.PluginName == "" {
		return errors.New("pluginName parameter of prefix disaggregation decider cannot be empty string")
	}

	if p.NonCachedTokens < 0 {
		return errors.New("nonCachedTokens parameter of prefix disaggregation decider cannot be negative")
	}

	return nil
}

// NewPdProfileHandler initializes a new PdProfileHandler and returns its pointer.
func newPrefixDisaggregationDecider(rawParameters json.RawMessage) (*PrefixDisaggregationDecider, error) {
	parameters := defaultParams

	if rawParameters != nil {
		if err := json.Unmarshal(rawParameters, &parameters); err != nil {
			return nil, fmt.Errorf("failed to parse the parameters of the prefix disaggregation decider. Error: %s", err)
		}
	}

	if err := parameters.validate(); err != nil {
		return nil, err
	}

	return &PrefixDisaggregationDecider{
		prefixPluginTypedName: plugins.TypedName{Type: prefix.PrefixCachePluginType, Name: parameters.PluginName},
		nonCachedTokens:       parameters.NonCachedTokens,
	}, nil
}

// PrefixDisaggregationDecider handles scheduler profiles for PD.
type PrefixDisaggregationDecider struct {
	prefixPluginTypedName plugins.TypedName
	nonCachedTokens       int
}

// isDisaggregationRequired checks if disaggregated PD is required for the given request and pod.
func (d *PrefixDisaggregationDecider) isDisaggregationRequired(ctx context.Context, inputTokens int, pod types.Pod) bool {
	logger := log.FromContext(ctx)
	debugLogger := log.FromContext(ctx).V(logutil.DEBUG)

	if d.nonCachedTokens <= 0 {
		// always use disaggregation in case of non cached tokens number is 0
		return true
	}
	if pod == nil {
		logger.Error(nil, "prefix decider: pod is nil")
		return false
	}

	// inspect the decode pod to decide if prefill should run or not.
	// if the non-cached part is short enough - no disaggregation.
	prefixInfoRaw, ok := pod.Get(approximateprefix.PrefixCacheMatchInfoKey)
	if !ok || prefixInfoRaw == nil {
		logger.Error(nil, "unable to read prefix cache state")
		return false
	}
	prefixCacheMatchInfo, ok := prefixInfoRaw.(*approximateprefix.PrefixCacheMatchInfo)
	if !ok {
		logger.Error(nil, "wrong type of prefix cache match info")
		return false
	}

	// number of cached tokens
	hitPrefix := float64(prefixCacheMatchInfo.MatchLength())
	// length of the cached part in percentages
	hitPercentagePrefix := hitPrefix / float64(inputTokens)
	debugLogger.Info("Computed hit percentage for prefix cache",
		"hitPercentage", hitPercentagePrefix, "absolute hit prefix len (tokens)", hitPrefix,
		"prompt length (token)", inputTokens)

	if (1.0-hitPercentagePrefix)*float64(inputTokens) < float64(d.nonCachedTokens) {
		debugLogger.Info("Non-cached suffix is smaller than threshold, using decode profile only",
			"hitPercentage", hitPercentagePrefix)
		return false // do not run prefill
	}

	return true
}
