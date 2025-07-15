package plugins

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework/plugins/multi/prefix"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	prerequest "github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/pre-request"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/profile"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugins.Register(filter.ByLabelFilterType, filter.ByLabelFilterFactory)
	plugins.Register(filter.ByLabelSelectorFilterType, filter.ByLabelSelectorFactory)
	plugins.Register(filter.DecodeFilterType, filter.DecodeFilterFactory)
	plugins.Register(filter.PrefillFilterType, filter.PrefillFilterFactory)
	plugins.Register(prerequest.PrefillHeaderHandlerType, prerequest.PrefillHeaderHandlerFactory)
	plugins.Register(profile.PdProfileHandlerType, profile.PdProfileHandlerFactory)
	plugins.Register(prefix.PrefixCachePluginType, scorer.PrefixCachePluginFactory)
	plugins.Register(scorer.LoadAwareScorerType, scorer.LoadAwareScorerFactory)
	plugins.Register(scorer.SessionAffinityScorerType, scorer.SessionAffinityScorerFactory)
}
