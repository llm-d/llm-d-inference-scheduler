// Package plugins registers all llm-d inference scheduler plugins.
package plugins

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/extractor/models"
	tokenizer "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/preparedata/tokenizer"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/disagg_headers"
	profile "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/disagg_profile"
	by_label_filter "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/filter/by_label"
	activerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/active_request"
	loadaware "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/load_aware"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/multi"
	nohitlru "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/no_hit_lru"
	preciseprefixcache "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/precise_prefix_cache"
	sessionaffinity "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/session_affinity"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugin.Register(by_label_filter.ByLabelType, by_label_filter.ByLabelFactory)
	plugin.Register(by_label_filter.ByLabelSelectorType, by_label_filter.ByLabelSelectorFactory)
	plugin.Register(by_label_filter.EncodeRoleType, by_label_filter.EncodeRoleFactory)
	plugin.Register(by_label_filter.DecodeRoleType, by_label_filter.DecodeRoleFactory)
	plugin.Register(by_label_filter.PrefillRoleType, by_label_filter.PrefillRoleFactory)
	plugin.Register(prerequest.DisaggHeadersHandlerType, prerequest.DisaggHeadersHandlerFactory)
	// Legacy alias - existing YAML configs using prefill-header-handler continue to work.
	plugin.Register(prerequest.PrefillHeaderHandlerType, prerequest.DisaggHeadersHandlerFactory) //nolint:staticcheck // intentional: keep backward compatibility (SA1019)
	plugin.Register(profile.DataParallelProfileHandlerType, profile.DataParallelProfileHandlerFactory)
	plugin.Register(profile.DisaggProfileHandlerType, profile.HandlerFactory)
	// Legacy aliases - existing YAML configs continue to work.
	// golangci-lint v2 only accepts linter names (lowercase) in //nolint directives, not individual check IDs like SA1019
	plugin.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory) //nolint:staticcheck // intentional: keep backward compatibility (SA1019)
	plugin.Register(preciseprefixcache.PrecisePrefixCachePluginType, preciseprefixcache.PluginFactory)
	plugin.Register(loadaware.LoadAwareType, loadaware.Factory)
	plugin.Register(sessionaffinity.SessionAffinityType, sessionaffinity.Factory)
	plugin.Register(activerequest.ActiveRequestType, activerequest.Factory)
	plugin.Register(nohitlru.NoHitLRUType, nohitlru.Factory)
	plugin.Register(models.ModelsDataSourceType, models.ModelDataSourceFactory)
	plugin.Register(models.ModelsExtractorType, models.ModelServerExtractorFactory)
	// pd decider plugins
	plugin.Register(profile.PrefixBasedPDDeciderPluginType, profile.PrefixBasedPDDeciderPluginFactory)
	plugin.Register(profile.AlwaysDisaggPDDeciderPluginType, profile.AlwaysDisaggPDDeciderPluginFactory)
	plugin.Register(tokenizer.PluginType, tokenizer.PluginFactory)
	// ep decider plugins
	plugin.Register(profile.AlwaysDisaggMulimodalPluginType, profile.AlwaysDisaggMulimodalDeciderPluginFactory)
	plugin.Register(multi.ContextLengthAwareType, multi.ContextLengthAwareFactory)
}
