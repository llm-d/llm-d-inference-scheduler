/*
Copyright 2026 The Kubernetes Authors.

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

package inflightload

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"

	"sigs.k8s.io/controller-runtime/pkg/log"

	logutil "github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/requestcontrol"
	fwksched "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
	attrconcurrency "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/concurrency"
	attrprefix "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/attribute/prefix"
	sourcenotifications "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/plugins/datalayer/source/notifications"
)

const (
	InFlightLoadProducerType = "inflight-load-producer"
	profilePrefill           = "prefill"
)

// Config controls optional behaviors of InFlightLoadProducer.
type Config struct {
	// IncludeOutputTokens controls whether estimated output tokens are added to
	// the in-flight token counter. Defaults to true to preserve historical behavior.
	IncludeOutputTokens bool `json:"includeOutputTokens"`
}

func defaultConfig() Config {
	return Config{IncludeOutputTokens: true}
}

func InFlightLoadProducerFactory(name string, rawParameters json.RawMessage, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	cfg := defaultConfig()
	if len(rawParameters) > 0 {
		if err := json.Unmarshal(rawParameters, &cfg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal inflight-load-producer parameters: %w", err)
		}
	}
	return &InFlightLoadProducer{
		typedName:           fwkplugin.TypedName{Type: InFlightLoadProducerType, Name: name},
		requestTracker:      newConcurrencyTracker(),
		tokenTracker:        newConcurrencyTracker(),
		tokenEstimator:      NewSimpleTokenEstimator(),
		includeOutputTokens: cfg.IncludeOutputTokens,
	}, nil
}

var (
	_ requestcontrol.PreRequest            = &InFlightLoadProducer{}
	_ requestcontrol.ResponseBodyProcessor = &InFlightLoadProducer{}
	_ requestcontrol.DataProducer          = &InFlightLoadProducer{}
	_ datalayer.EndpointExtractor          = &InFlightLoadProducer{}
	_ datalayer.Registrant                 = &InFlightLoadProducer{}
)

type InFlightLoadProducer struct {
	typedName           fwkplugin.TypedName
	requestTracker      *concurrencyTracker
	tokenTracker        *concurrencyTracker
	tokenEstimator      TokenEstimator
	includeOutputTokens bool
	// addedTokens tracks the exact token amount added per (requestID, endpointID)
	// so release subtracts the same value (accounting for prefix-cache discount).
	// Key format: "<requestID>|<endpointID>".
	addedTokens sync.Map
}

func (p *InFlightLoadProducer) TypedName() fwkplugin.TypedName {
	return p.typedName
}

// RegisterDependencies declares that this plugin needs an endpoint-notification-source to track
// endpoint lifecycle events. The source is auto-created if not already in the config.
func (p *InFlightLoadProducer) RegisterDependencies(r datalayer.Registrar) error {
	return r.Register(datalayer.PendingRegistration{
		Owner:         p.TypedName(),
		SourceType:    sourcenotifications.EndpointNotificationSourceType,
		Extractor:     p,
		DefaultSource: sourcenotifications.NewEndpointDataSource(sourcenotifications.EndpointNotificationSourceType, sourcenotifications.EndpointNotificationSourceType),
	})
}

// ExpectedInputType defines the type expected by the extractor.
func (p *InFlightLoadProducer) ExpectedInputType() reflect.Type {
	return datalayer.EndpointEventReflectType
}

// ExtractEndpoint handles endpoint deletion events to prune stateful trackers.
func (p *InFlightLoadProducer) ExtractEndpoint(ctx context.Context, event datalayer.EndpointEvent) error {
	if event.Type != datalayer.EventDelete || event.Endpoint == nil {
		return nil
	}

	id := event.Endpoint.GetMetadata().NamespacedName.String()

	p.DeleteEndpoint(id)
	log.FromContext(ctx).V(logutil.DEFAULT).Info("Cleaned up in-flight load for deleted endpoint", "endpoint", id)
	return nil
}

func (p *InFlightLoadProducer) PrepareRequestData(_ context.Context, _ *fwksched.InferenceRequest, endpoints []fwksched.Endpoint) error {
	for _, e := range endpoints {
		endpointID := e.GetMetadata().NamespacedName.String()
		e.Put(attrconcurrency.InFlightLoadKey, &attrconcurrency.InFlightLoad{
			Tokens:   p.tokenTracker.get(endpointID),
			Requests: p.requestTracker.get(endpointID),
		})
	}
	return nil
}

func (p *InFlightLoadProducer) PreRequest(_ context.Context, request *fwksched.InferenceRequest, result *fwksched.SchedulingResult) {
	if result == nil || len(result.ProfileResults) == 0 {
		return
	}

	inputTokens := p.tokenEstimator.EstimateInput(request)

	for _, profileResult := range result.ProfileResults {
		if profileResult == nil || len(profileResult.TargetEndpoints) == 0 {
			continue
		}
		// Only track the first endpoint (the primary target), as requested by reviewers.
		endpoint := profileResult.TargetEndpoints[0]
		if endpoint == nil || endpoint.GetMetadata() == nil {
			continue
		}
		eid := endpoint.GetMetadata().NamespacedName.String()
		p.requestTracker.inc(eid)

		// Subtract approximate prefix-cache hit (in input tokens) for this endpoint.
		cached := cachedInputTokens(endpoint)
		adjustedInput := inputTokens - cached
		if adjustedInput < 0 {
			adjustedInput = 0
		}
		tokens := adjustedInput
		if p.includeOutputTokens {
			// Output tokens are based on the full input, not the cached portion.
			tokens += p.tokenEstimator.EstimateOutput(inputTokens)
		}

		p.tokenTracker.add(eid, tokens)
		if request != nil {
			p.addedTokens.Store(addedTokensKey(request.RequestID, eid), tokens)
		}
	}
}

func (p *InFlightLoadProducer) ResponseBody(
	ctx context.Context,
	request *fwksched.InferenceRequest,
	resp *requestcontrol.Response,
	_ *datalayer.EndpointMetadata,
) {
	if request == nil || resp == nil {
		return
	}

	result := request.SchedulingResult
	if result == nil {
		return
	}

	// When output tokens are excluded, the in-flight token estimate represents only
	// the prompt cost, which is consumed by prefill. As soon as the first chunk
	// arrives (StartOfStream), prefill is done across all profiles, so free the
	// token counters for every targeted endpoint regardless of profile name.
	// Request counters are still released on EndOfStream below.
	if !p.includeOutputTokens && resp.StartOfStream {
		for _, profileResult := range result.ProfileResults {
			if profileResult == nil || len(profileResult.TargetEndpoints) == 0 {
				continue
			}
			p.releaseTokens(profileResult.TargetEndpoints[0], request)
		}
	}

	// 1. Early Prefill Release (on first chunk) — original behavior.
	// Uses the new StartOfStream signal provided by the framework.
	if p.includeOutputTokens && resp.StartOfStream {
		if prefillResult, ok := result.ProfileResults[profilePrefill]; ok && len(prefillResult.TargetEndpoints) > 0 {
			p.release(prefillResult.TargetEndpoints[0], request)
		}
	}

	// 2. Full Cleanup (on completion)
	if resp.EndOfStream {
		for name, profileResult := range result.ProfileResults {
			if profileResult == nil || len(profileResult.TargetEndpoints) == 0 {
				continue
			}
			endpoint := profileResult.TargetEndpoints[0]

			if !p.includeOutputTokens {
				// Tokens were freed at StartOfStream; only release the request counter.
				p.releaseRequest(endpoint)
				continue
			}

			// Skip "prefill" as it was already released in the StartOfStream block.
			// This works perfectly even if StartOfStream and EndOfStream are both true (single chunk).
			if name == profilePrefill {
				continue
			}
			p.release(endpoint, request)
		}
	}
}

func (p *InFlightLoadProducer) release(endpoint fwksched.Endpoint, request *fwksched.InferenceRequest) {
	p.releaseRequest(endpoint)
	p.releaseTokens(endpoint, request)
}

func (p *InFlightLoadProducer) releaseRequest(endpoint fwksched.Endpoint) {
	if endpoint == nil || endpoint.GetMetadata() == nil {
		return
	}
	eid := endpoint.GetMetadata().NamespacedName.String()
	p.requestTracker.dec(eid)
}

func (p *InFlightLoadProducer) releaseTokens(endpoint fwksched.Endpoint, request *fwksched.InferenceRequest) {
	if endpoint == nil || endpoint.GetMetadata() == nil {
		return
	}
	eid := endpoint.GetMetadata().NamespacedName.String()

	// Prefer the exact value stored in PreRequest to keep counters balanced.
	if request != nil {
		key := addedTokensKey(request.RequestID, eid)
		if v, ok := p.addedTokens.LoadAndDelete(key); ok {
			if tokens, ok := v.(int64); ok && tokens != 0 {
				p.tokenTracker.add(eid, -tokens)
			}
			return
		}
	}

	// Fallback: re-estimate (covers tests/legacy paths that bypass PreRequest).
	tokens := p.tokenEstimator.Estimate(request)
	if tokens != 0 {
		p.tokenTracker.add(eid, -tokens)
	}
}

func addedTokensKey(requestID, endpointID string) string {
	return requestID + "|" + endpointID
}

// cachedInputTokens returns the number of input tokens this endpoint already has cached
// from the approximate prefix cache producer, or 0 if not available.
func cachedInputTokens(endpoint fwksched.Endpoint) int64 {
	if endpoint == nil {
		return 0
	}
	raw, ok := endpoint.Get(attrprefix.PrefixCacheMatchInfoKey)
	if !ok {
		return 0
	}
	info, ok := raw.(*attrprefix.PrefixCacheMatchInfo)
	if !ok || info == nil {
		return 0
	}
	cached := int64(info.MatchBlocks()) * int64(info.BlockSizeTokens())
	if cached < 0 {
		return 0
	}
	return cached
}

func (p *InFlightLoadProducer) Produces() map[string]any {
	return map[string]any{
		attrconcurrency.InFlightLoadKey: attrconcurrency.InFlightLoad{},
	}
}

func (p *InFlightLoadProducer) Consumes() map[string]any {
	return map[string]any{
		attrprefix.PrefixCacheMatchInfoKey: (*attrprefix.PrefixCacheMatchInfo)(nil),
	}
}

// DeleteEndpoint removes an endpoint from the concurrency trackers to prevent memory leaks.
// This matches the design of the previous saturation detector and is called by the
// ExtractNotification hook to ensure deterministic cleanup of stateful data.
func (p *InFlightLoadProducer) DeleteEndpoint(endpointID string) {
	p.requestTracker.delete(endpointID)
	p.tokenTracker.delete(endpointID)
}

// concurrencyTracker manages thread-safe counters for inflight requests.
type concurrencyTracker struct {
	mu     sync.RWMutex
	counts map[string]*atomic.Int64
}

func newConcurrencyTracker() *concurrencyTracker {
	return &concurrencyTracker{
		counts: make(map[string]*atomic.Int64),
	}
}

func (ct *concurrencyTracker) get(endpointID string) int64 {
	ct.mu.RLock()
	counter, exists := ct.counts[endpointID]
	ct.mu.RUnlock()

	if !exists {
		return 0
	}
	return counter.Load()
}

func (ct *concurrencyTracker) inc(endpointID string) {
	ct.add(endpointID, 1)
}

func (ct *concurrencyTracker) add(endpointID string, delta int64) {
	ct.mu.RLock()
	counter, exists := ct.counts[endpointID]
	ct.mu.RUnlock()

	if exists {
		counter.Add(delta)
		return
	}

	ct.mu.Lock()
	defer ct.mu.Unlock()

	if counter, exists = ct.counts[endpointID]; exists {
		counter.Add(delta)
		return
	}

	counter = &atomic.Int64{}
	counter.Store(delta)
	ct.counts[endpointID] = counter
}

func (ct *concurrencyTracker) dec(endpointID string) {
	ct.add(endpointID, -1)
}

func (ct *concurrencyTracker) delete(endpointID string) {
	ct.mu.Lock()
	defer ct.mu.Unlock()
	delete(ct.counts, endpointID)
}
