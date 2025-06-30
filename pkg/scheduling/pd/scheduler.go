package pd

import (
	"context"
	"fmt"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling"
	gieschedulingconfig "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/config"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
	giefilter "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/filter"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/picker"
	gieprofile "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/profile"
	giescorer "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/scorer"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/config"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

// CreatePDSchedulerConfig returns a new disaggregated Prefill/Decode SchedulerConfig, using the provided configuration.
func CreatePDSchedulerConfig(ctx context.Context, pdConfig *config.Config) (*scheduling.SchedulerConfig, error) {
	if !pdConfig.PDEnabled { // if PD is disabled, create scheduler with SingleProfileHandler (handling only decode profile)
		return createDecodeOnlySchedulerConfig(ctx, pdConfig.DecodeSchedulerPlugins, pdConfig)
	}
	// otherwise, PD is enabled.
	prefixScorer := prefix.New(*pdConfig.GIEPrefixConfig) // create prefix scorer instance to be used in both decode and prefill profiles

	// create decode scheduling profile.
	decodeProfile, err := createSchedulerProfile(ctx, filter.NewDecodeFilter(), picker.NewMaxScorePicker(), pdConfig.DecodeSchedulerPlugins, pdConfig, prefixScorer)

	if err != nil {
		return nil, fmt.Errorf("falied to create decode scheduling profile - %w", err)
	}

	// create prefil scheduling profile.
	prefilProfile, err := createSchedulerProfile(ctx, filter.NewPrefillFilter(), picker.NewMaxScorePicker(), pdConfig.PrefillSchedulerPlugins, pdConfig, prefixScorer)

	if err != nil {
		return nil, fmt.Errorf("falied to create prefill scheduling profile - %w", err)
	}

	pdProfileHandler := profile.NewPdProfileHandler(pdConfig)
	return scheduling.NewSchedulerConfig(pdProfileHandler, map[string]*framework.SchedulerProfile{
		"decode":  decodeProfile,
		"prefill": prefilProfile,
	}), nil
}

func createDecodeOnlySchedulerConfig(ctx context.Context, configuredPlugins map[string]int, pdConfig *config.Config) (*scheduling.SchedulerConfig, error) {
	loggerDebug := log.FromContext(ctx).WithName("pd-Scheduler").V(logutil.DEBUG)

	// create decode profile
	decodeProfile, err := createSchedulerProfile(ctx, filter.NewDecodeFilter(), picker.NewMaxScorePicker(), configuredPlugins, pdConfig, prefix.New(*pdConfig.GIEPrefixConfig))

	if err != nil {
		return nil, fmt.Errorf("falied to create decode scheduling profile - %w", err)
	}
	loggerDebug.Info("Disagregated prefill/decode disabled - scheduler configured to work with decode profile only")
	return scheduling.NewSchedulerConfig(gieprofile.NewSingleProfileHandler(), map[string]*framework.SchedulerProfile{
		"decode": decodeProfile}), nil
}

func createSchedulerProfile(ctx context.Context, roleFilter framework.Filter, picker framework.Picker, configuredPlugins map[string]int,
	pdConfig *config.Config, prefixScorer *prefix.Plugin) (*framework.SchedulerProfile, error) {
	plugins := pluginsFromConfig(ctx, configuredPlugins, pdConfig, prefixScorer) // share the same prefix scorer instance

	profile := framework.NewSchedulerProfile().
		WithFilters(roleFilter).
		WithPicker(picker)
	if err := profile.AddPlugins(plugins...); err != nil {
		return nil, fmt.Errorf("falied to create scheduler profile - %w", err)
	}

	return profile, nil
}

func pluginsFromConfig(ctx context.Context, pluginsConfig map[string]int, pdConfig *config.Config, prefixScorer *prefix.Plugin) []plugins.Plugin {
	logger := log.FromContext(ctx)

	plugins := []plugins.Plugin{}
	for pluginName, pluginWeight := range pluginsConfig {
		switch pluginName {
		case config.KVCacheScorerName:
			if scorer, err := scorer.NewKVCacheAwareScorer(ctx); err == nil {
				plugins = append(plugins, framework.NewWeightedScorer(scorer, pluginWeight))
			} else {
				logger.Error(err, "KVCache scorer creation failed")
			}
		case config.LoadAwareScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(scorer.NewLoadAwareScorer(ctx), pluginWeight))
		case config.SessionAwareScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(scorer.NewSessionAffinity(), pluginWeight))

		// Plugins from upstream

		case config.GIELeastKVCacheFilterName:
			plugins = append(plugins, giefilter.NewLeastKVCacheFilter())
		case config.GIELeastQueueFilterName:
			plugins = append(plugins, giefilter.NewLeastQueueFilter())
		case config.GIELoraAffinityFilterName:
			plugins = append(plugins, giefilter.NewLoraAffinityFilter(gieschedulingconfig.Conf.LoraAffinityThreshold))
		case config.GIELowQueueFilterName:
			plugins = append(plugins, giefilter.NewLowQueueFilter(gieschedulingconfig.Conf.QueueingThresholdLoRA))
		case config.GIEKVCacheUtilizationScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(giescorer.NewKVCacheScorer(), pluginWeight))
		case config.GIEPrefixScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(prefixScorer, pluginWeight))
		case config.GIEQueueScorerName:
			plugins = append(plugins, framework.NewWeightedScorer(giescorer.NewQueueScorer(), pluginWeight))
		}
	}

	// in case pd is enabled and prefix scorer was not enabled for the profile
	// add prefix scorer to list of all scorers to collect information used for the decision if prefill should be called.
	if _, exist := pluginsConfig[config.GIEPrefixScorerName]; !exist && pdConfig.PDEnabled {
		plugins = append(plugins, framework.NewWeightedScorer(prefixScorer, 0))
	}

	return plugins
}
