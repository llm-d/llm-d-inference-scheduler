package plugins

import (
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/datalayer/models"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugin.Register(filter.ByLabelType, filter.ByLabelFactory)
	plugin.Register(filter.ByLabelSelectorType, filter.ByLabelSelectorFactory)
	plugin.Register(filter.DecodeRoleType, filter.DecodeRoleFactory)
	plugin.Register(filter.PrefillRoleType, filter.PrefillRoleFactory)
	plugin.Register(prerequest.PrefillHeaderHandlerType, prerequest.PrefillHeaderHandlerFactory)
	plugin.Register(profile.DataParallelProfileHandlerType, profile.DataParallelProfileHandlerFactory)
	plugin.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory)
	plugin.Register(scorer.PrecisePrefixCachePluginType, scorer.PrecisePrefixCachePluginFactory)
	plugin.Register(scorer.LoadAwareType, scorer.LoadAwareFactory)
	plugin.Register(scorer.SessionAffinityType, scorer.SessionAffinityFactory)
	plugin.Register(scorer.ActiveRequestType, scorer.ActiveRequestFactory)
	plugin.Register(scorer.NoHitLRUType, scorer.NoHitLRUFactory)
	plugin.Register(models.ModelsDataSourceType, models.ModelDataSourceFactory)
	plugin.Register(models.ModelsExtractorType, models.ModelServerExtractorFactory)
	// pd decider plugins
	plugin.Register(profile.PrefixBasedPDDeciderPluginType, profile.PrefixBasedPDDeciderPluginFactory)
	plugin.Register(profile.AlwaysDisaggDeciderPluginType, profile.AlwaysDisaggPDDeciderPluginFactory)
	plugin.Register(scorer.PDSLOAwareScorerType, scorer.PDSLOAwareScorerFactory)
}
