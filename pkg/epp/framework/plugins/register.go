// Package plugins registers all llm-d inference scheduler plugins.
package plugins

import (
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/extractor/models"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/tokenizer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/filter/bylabel"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/profilehandler/dataparallel"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/profilehandler/disagg"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/activerequest"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/contextlengthaware"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/loadaware"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/nohitlru"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/preciseprefixcache"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/scheduling/scorer/sessionaffinity"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugin.Register(bylabel.ByLabelType, bylabel.Factory)
	plugin.Register(bylabel.ByLabelSelectorType, bylabel.SelectorFactory)
	plugin.Register(bylabel.EncodeRoleType, bylabel.EncodeRoleFactory)
	plugin.Register(bylabel.DecodeRoleType, bylabel.DecodeRoleFactory)
	plugin.Register(bylabel.PrefillRoleType, bylabel.PrefillRoleFactory)
	plugin.Register(disagg.DisaggHeadersHandlerType, disagg.HeadersHandlerFactory)
	// Legacy alias - existing YAML configs using prefill-header-handler continue to work.
	plugin.Register(disagg.PrefillHeaderHandlerType, disagg.HeadersHandlerFactory) //nolint:staticcheck // intentional: keep backward compatibility (SA1019)
	plugin.Register(dataparallel.DataParallelProfileHandlerType, dataparallel.ProfileHandlerFactory)
	plugin.Register(disagg.DisaggProfileHandlerType, disagg.HandlerFactory)
	// Legacy aliases - existing YAML configs continue to work.
	// golangci-lint v2 only accepts linter names (lowercase) in //nolint directives, not individual check IDs like SA1019
	plugin.Register(disagg.PdProfileHandlerType, disagg.PdProfileHandlerFactory) //nolint:staticcheck // intentional: keep backward compatibility (SA1019)
	plugin.Register(preciseprefixcache.PrecisePrefixCachePluginType, preciseprefixcache.PluginFactory)
	plugin.Register(loadaware.LoadAwareType, loadaware.Factory)
	plugin.Register(sessionaffinity.SessionAffinityType, sessionaffinity.Factory)
	plugin.Register(activerequest.ActiveRequestType, activerequest.Factory)
	plugin.Register(nohitlru.NoHitLRUType, nohitlru.Factory)
	plugin.Register(models.ModelsDataSourceType, models.ModelDataSourceFactory)
	plugin.Register(models.ModelsExtractorType, models.ModelServerExtractorFactory)
	// pd decider plugins
	plugin.Register(disagg.PrefixBasedPDDeciderPluginType, disagg.PrefixBasedPDDeciderPluginFactory)
	plugin.Register(disagg.AlwaysDisaggPDDeciderPluginType, disagg.AlwaysDisaggPDDeciderPluginFactory)
	plugin.Register(tokenizer.PluginType, tokenizer.PluginFactory)
	// ep decider plugins
	plugin.Register(disagg.AlwaysDisaggMulimodalPluginType, disagg.AlwaysDisaggMulimodalDeciderPluginFactory)
	plugin.Register(contextlengthaware.ContextLengthAwareType, contextlengthaware.Factory)
}
