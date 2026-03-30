package plugins

import (
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/datalayer/models"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/multi"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/preparedata"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
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
	plugin.Register(scorer.PrecisePrefixCachePluginType, scorer.PrecisePrefixCachePluginFactory)
	plugin.Register(scorer.LoadAwareType, scorer.LoadAwareFactory)
	plugin.Register(scorer.SessionAffinityType, scorer.SessionAffinityFactory)
	plugin.Register(scorer.ActiveRequestType, scorer.ActiveRequestFactory)
	plugin.Register(scorer.NoHitLRUType, scorer.NoHitLRUFactory)
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
