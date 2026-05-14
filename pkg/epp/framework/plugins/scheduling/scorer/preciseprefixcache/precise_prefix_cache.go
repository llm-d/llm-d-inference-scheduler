/*
Copyright 2026 The llm-d Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

// Package preciseprefixcache implements the slim precise-prefix-cache-scorer:
// it reads the per-endpoint PrefixCacheMatchInfo attribute written by the
// precise-prefix-cache-producer and returns matchBlocks/totalBlocks.
//
// For backward compatibility the factory accepts the old all-in-one
// parameters; it then instantiates an internal Producer and returns a
// legacy wrapper that fulfills Scorer + DataProducer + PreRequest +
// EndpointExtractor from one plugin instance.
package preciseprefixcache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrprefix "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	preciseproducer "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/preciseprefixcache"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/telemetry"
)

// PrecisePrefixCachePluginType keeps the public type name stable for YAML
// compatibility.
const PrecisePrefixCachePluginType = "precise-prefix-cache-scorer"

var _ scheduling.Scorer = &Scorer{}

func New() *Scorer {
	return &Scorer{typedName: plugin.TypedName{Type: PrecisePrefixCachePluginType}}
}

// Scorer is a pure reader of PrefixCacheMatchInfo. No tokenization, no
// index access — that lives on the precise-prefix-cache-producer.
type Scorer struct {
	typedName plugin.TypedName
}

func (s *Scorer) TypedName() plugin.TypedName {
	return s.typedName
}

func (s *Scorer) WithName(name string) *Scorer {
	s.typedName.Name = name
	return s
}

func (s *Scorer) Category() scheduling.ScorerCategory {
	return scheduling.Affinity
}

func (s *Scorer) Consumes() map[string]any {
	return map[string]any{attrprefix.PrefixCacheMatchInfoKey: attrprefix.PrefixCacheMatchInfo{}}
}

// Score returns matchBlocks/totalBlocks per endpoint. Missing or malformed
// attributes score 0.
func (s *Scorer) Score(ctx context.Context, _ *scheduling.CycleState,
	request *scheduling.InferenceRequest, endpoints []scheduling.Endpoint,
) map[scheduling.Endpoint]float64 {
	tracer := telemetry.Tracer()
	ctx, span := tracer.Start(ctx, "llm_d.epp.scorer.prefix_cache",
		trace.WithSpanKind(trace.SpanKindInternal),
	)
	defer span.End()

	logger := log.FromContext(ctx).WithName(s.typedName.String())
	debugLogger := logger.V(logging.DEBUG)

	span.SetAttributes(attribute.Int("llm_d.scorer.candidate_endpoints", len(endpoints)))
	if request != nil {
		if request.TargetModel != "" {
			span.SetAttributes(attribute.String("gen_ai.request.model", request.TargetModel))
		}
		if request.RequestID != "" {
			span.SetAttributes(attribute.String("gen_ai.request.id", request.RequestID))
		}
	}

	scores := make(map[scheduling.Endpoint]float64, len(endpoints))
	var maxScore, totalScore float64
	for _, endpoint := range endpoints {
		scores[endpoint] = 0.0

		raw, ok := endpoint.Get(attrprefix.PrefixCacheMatchInfoKey)
		if !ok {
			debugLogger.Info("PrefixCacheMatchInfo missing, assigning 0",
				"endpoint", endpointKey(endpoint))
			continue
		}
		info, ok := raw.(*attrprefix.PrefixCacheMatchInfo)
		if !ok {
			logger.Error(nil, "PrefixCacheMatchInfo has unexpected type, assigning 0",
				"endpoint", endpointKey(endpoint))
			continue
		}
		if info.TotalBlocks() == 0 {
			continue
		}
		score := float64(info.MatchBlocks()) / float64(info.TotalBlocks())
		scores[endpoint] = score
		if score > maxScore {
			maxScore = score
		}
		totalScore += score
	}

	if len(endpoints) > 0 {
		span.SetAttributes(
			attribute.Float64("llm_d.scorer.score.max", maxScore),
			attribute.Float64("llm_d.scorer.score.avg", totalScore/float64(len(endpoints))),
			attribute.Int("llm_d.scorer.endpoints_scored", len(endpoints)),
		)
	}

	if debugLogger.Enabled() {
		// Build a string-keyed snapshot — log encoders JSON-marshal values and
		// choke on the live Endpoint interface as a map key.
		loggable := make(map[string]float64, len(scores))
		for ep, score := range scores {
			loggable[endpointKey(ep)] = score
		}
		debugLogger.Info("Computed prefix-cache scores", "scores", loggable)
	}

	return scores
}

func endpointKey(ep scheduling.Endpoint) string {
	md := ep.GetMetadata()
	if md == nil {
		return "<unknown>"
	}
	return fmt.Sprintf("%s:%s", md.Address, md.Port)
}

// PluginFactory routes to either the slim Scorer (no params, split mode) or
// the legacy wrapper (any non-empty params, all-in-one mode). Legacy mode
// will be removed in a future release.
func PluginFactory(name string, rawParameters json.RawMessage, handle plugin.Handle) (plugin.Plugin, error) {
	if !hasConfig(rawParameters) {
		return New().WithName(name), nil
	}

	ctx := handle.Context()
	log.FromContext(ctx).Info(
		"precise-prefix-cache-scorer is configured with parameters; running in legacy all-in-one mode. "+
			"Prefer splitting into a `token-producer` plugin, a `precise-prefix-cache-producer` plugin, and a "+
			"parameter-free `precise-prefix-cache-scorer`. Legacy mode will be removed in a future release.",
		"plugin", name,
	)

	producer, err := preciseproducer.PluginFactory(name+"-legacy-producer", rawParameters, handle)
	if err != nil {
		return nil, fmt.Errorf("legacy %s: %w", PrecisePrefixCachePluginType, err)
	}
	prod, ok := producer.(*preciseproducer.Producer)
	if !ok {
		return nil, fmt.Errorf("legacy %s: unexpected producer type %T", PrecisePrefixCachePluginType, producer)
	}

	slim := New().WithName(name)
	return &legacyScorer{Scorer: slim, producer: prod}, nil
}

// hasConfig reports whether rawParameters carry any non-empty content
// (treat absent, `null`, and `{}` as empty).
func hasConfig(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	switch string(trimmed) {
	case "null", "{}":
		return false
	}
	return true
}
