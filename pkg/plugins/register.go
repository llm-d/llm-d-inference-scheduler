package plugins

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/filter"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/plugins/scorer"
)

// RegisterAllPlugins registers the factory functions of all plugins in this repository.
func RegisterAllPlugins() {
	plugins.Register(filter.ByLabelsFilterType, filter.ByLabelFactory)
	plugins.Register(filter.DecodeFilterType, filter.DecodeFilterFactory)
	plugins.Register(filter.PrefillFilterType, filter.PrefillFilterFactory)
	plugins.Register(scorer.KvCacheAwareScorerType, scorer.KvCacheAwareScorerFactory)
	plugins.Register(scorer.LoadAwareScorerType, scorer.LoadAwareScorerFactory)
	plugins.Register(scorer.PrefixAwareScorerType, scorer.PrefixAwareScorerFactory)
	plugins.Register(scorer.SessionAffinityScorerType, scorer.SessionAffinityScorerFactory)
}
