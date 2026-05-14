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

package preciseprefixcache

import (
	"context"
	"reflect"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrprefix "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	preciseproducer "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/requestcontrol/dataproducer/preciseprefixcache"
)

// legacyScorer wraps a tokens-only Producer + slim Scorer and routes
// no-token requests through the producer's internal-tokenization helpers.
type legacyScorer struct {
	*Scorer
	producer *preciseproducer.Producer
}

var (
	_ scheduling.Scorer           = &legacyScorer{}
	_ requestcontrol.DataProducer = &legacyScorer{}
	_ requestcontrol.PreRequest   = &legacyScorer{}
	_ fwkdl.EndpointExtractor     = &legacyScorer{}
)

func (l *legacyScorer) TypedName() plugin.TypedName {
	return l.Scorer.TypedName()
}

func (l *legacyScorer) Produces() map[string]any {
	return map[string]any{attrprefix.PrefixCacheMatchInfoKey: attrprefix.PrefixCacheMatchInfo{}}
}

// Consumes returns nil so legacy configs without a token-producer don't fail
// the DAG.
func (l *legacyScorer) Consumes() map[string]any {
	return nil
}

func (l *legacyScorer) Produce(ctx context.Context,
	request *scheduling.InferenceRequest, endpoints []scheduling.Endpoint,
) error {
	l.producer.EnsureSubscribersForEndpoints(ctx, endpoints)

	if hasTokens(request) {
		return l.producer.Produce(ctx, request, endpoints)
	}
	blockKeys, err := l.producer.ComputeBlockKeysFromRequest(ctx, request)
	if err != nil {
		return err
	}
	return l.producer.ProduceFromBlockKeys(ctx, request, endpoints, blockKeys)
}

func hasTokens(request *scheduling.InferenceRequest) bool {
	return request != nil && request.Body != nil &&
		request.Body.TokenizedPrompt != nil &&
		len(request.Body.TokenizedPrompt.TokenIDs) > 0
}

func (l *legacyScorer) PreRequest(ctx context.Context,
	request *scheduling.InferenceRequest, schedulingResult *scheduling.SchedulingResult,
) {
	l.producer.PreRequest(ctx, request, schedulingResult)
}

func (l *legacyScorer) ExpectedInputType() reflect.Type {
	return l.producer.ExpectedInputType()
}

func (l *legacyScorer) ExtractEndpoint(ctx context.Context, event fwkdl.EndpointEvent) error {
	return l.producer.ExtractEndpoint(ctx, event)
}
