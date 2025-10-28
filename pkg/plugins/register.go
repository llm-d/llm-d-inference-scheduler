package plugins

import (
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugins.Register(filter.ByLabelType, filter.ByLabelFactory)
	plugins.Register(filter.ByLabelSelectorType, filter.ByLabelSelectorFactory)
	plugins.Register(filter.DecodeRoleType, filter.DecodeRoleFactory)
	plugins.Register(filter.PrefillRoleType, filter.PrefillRoleFactory)
	plugins.Register(prerequest.PrefillHeaderHandlerType, prerequest.PrefillHeaderHandlerFactory)
	plugins.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory)
	plugins.Register(scorer.PrecisePrefixCachePluginType, scorer.PrecisePrefixCachePluginFactory)
	plugins.Register(scorer.LoadAwareType, scorer.LoadAwareFactory)
	plugins.Register(scorer.SessionAffinityType, scorer.SessionAffinityFactory)
	plugins.Register(scorer.ActiveRequestType, scorer.ActiveRequestFactory)
	plugins.Register(scorer.NoHitLRUType, scorer.NoHitLRUFactory)
}
