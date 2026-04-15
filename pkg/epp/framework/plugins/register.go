// Package plugins registers all llm-d inference scheduler plugins.
package plugins

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"

	tokenizer "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/dataproducer/tokenizer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/extractor/models"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/disaggheaders"
	by_label_filter "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/filter/bylabel"
	dataparallel "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/profilehandler/dataparallel"
	profile "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/profilehandler/disagg"
	activerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/activerequest"
	contextlengthaware "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/contextlengthaware"
	loadaware "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/loadaware"
	nohitlru "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/nohitlru"
	preciseprefixcache "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/preciseprefixcache"
	sessionaffinity "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/sessionaffinity"
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
	plugin.Register(dataparallel.DataParallelProfileHandlerType, dataparallel.ProfileHandlerFactory)
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
	plugin.Register(contextlengthaware.ContextLengthAwareType, contextlengthaware.Factory)
}
