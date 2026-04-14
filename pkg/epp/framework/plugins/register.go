package plugins

import (
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/models"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/filter/by_label"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/multi"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/preparedata"
	profile "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/profile/llm-d"
	active_request "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/active_request"
	load_aware "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/load_aware"
	precise_prefix_cache "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/precise_prefix_cache"
	session_affinity "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/session_affinity"
	no_hit_lru "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/no_hit_lru"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugin.Register(filter.ByLabelType, filter.ByLabelFactory)
	plugin.Register(filter.ByLabelSelectorType, filter.ByLabelSelectorFactory)
	plugin.Register(filter.EncodeRoleType, filter.EncodeRoleFactory)
	plugin.Register(filter.DecodeRoleType, filter.DecodeRoleFactory)
	plugin.Register(filter.PrefillRoleType, filter.PrefillRoleFactory)
	plugin.Register(prerequest.DisaggHeadersHandlerType, prerequest.DisaggHeadersHandlerFactory)
	// Legacy alias - existing YAML configs using prefill-header-handler continue to work.
	plugin.Register(prerequest.PrefillHeaderHandlerType, prerequest.DisaggHeadersHandlerFactory) //nolint:staticcheck // intentional: keep backward compatibility (SA1019)
	plugin.Register(profile.DataParallelProfileHandlerType, profile.DataParallelProfileHandlerFactory)
	plugin.Register(profile.DisaggProfileHandlerType, profile.DisaggProfileHandlerFactory)
	// Legacy aliases - existing YAML configs continue to work.
	// golangci-lint v2 only accepts linter names (lowercase) in //nolint directives, not individual check IDs like SA1019
	plugin.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory) //nolint:staticcheck // intentional: keep backward compatibility (SA1019)
	plugin.Register(precise_prefix_cache.PrecisePrefixCachePluginType, precise_prefix_cache.PrecisePrefixCachePluginFactory)
	plugin.Register(load_aware.LoadAwareType, load_aware.LoadAwareFactory)
	plugin.Register(session_affinity.SessionAffinityType, session_affinity.SessionAffinityFactory)
	plugin.Register(active_request.ActiveRequestType, active_request.ActiveRequestFactory)
	plugin.Register(no_hit_lru.NoHitLRUType, no_hit_lru.NoHitLRUFactory)
	plugin.Register(models.ModelsDataSourceType, models.ModelDataSourceFactory)
	plugin.Register(models.ModelsExtractorType, models.ModelServerExtractorFactory)
	// pd decider plugins
	plugin.Register(profile.PrefixBasedPDDeciderPluginType, profile.PrefixBasedPDDeciderPluginFactory)
	plugin.Register(profile.AlwaysDisaggPDDeciderPluginType, profile.AlwaysDisaggPDDeciderPluginFactory)
	plugin.Register(preparedata.TokenizerPluginType, preparedata.TokenizerPluginFactory)
	// ep decider plugins
	plugin.Register(profile.AlwaysDisaggMulimodalPluginType, profile.AlwaysDisaggMulimodalDeciderPluginFactory)
	plugin.Register(multi.ContextLengthAwareType, multi.ContextLengthAwareFactory)
}
