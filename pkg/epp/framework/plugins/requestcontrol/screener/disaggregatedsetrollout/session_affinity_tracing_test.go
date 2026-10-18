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

package disaggregatedsetrollout

import (
	"context"
	"errors"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/llm-d/llm-d-router/pkg/common/observability/semconv"
	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
)

// setupSpanRecorder installs an in-memory span recorder as the global tracer
// provider and returns it, restoring the previous provider on cleanup.
func setupSpanRecorder(t *testing.T) *tracetest.SpanRecorder {
	t.Helper()
	recorder := tracetest.NewSpanRecorder()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	origTP := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() { otel.SetTracerProvider(origTP) })
	return recorder
}

// screenSpan returns the single screening span recorded during fn, failing
// when none was recorded.
func screenSpan(t *testing.T, recorder *tracetest.SpanRecorder, fn func()) sdktrace.ReadOnlySpan {
	t.Helper()
	fn()
	var spans []sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == "screen_revisions" {
			spans = append(spans, span)
		}
	}
	if len(spans) == 0 {
		t.Fatal("no screen_revisions span was recorded")
	}
	return spans[len(spans)-1]
}

func spanAttributes(span sdktrace.ReadOnlySpan) map[attribute.Key]attribute.Value {
	attrs := make(map[attribute.Key]attribute.Value)
	for _, kv := range span.Attributes() {
		attrs[kv.Key] = kv.Value
	}
	return attrs
}

func attrString(t *testing.T, attrs map[attribute.Key]attribute.Value, key attribute.Key) string {
	t.Helper()
	value, ok := attrs[key]
	if !ok {
		t.Fatalf("span is missing attribute %q", string(key))
	}
	return value.AsString()
}

func TestScreenerTracesStickyReuse(t *testing.T) {
	recorder := setupSpanRecorder(t)
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	candidates := candidatePool(1, 1)

	first := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), sessionRequest("session", 0), candidates)
	})
	firstAttrs := spanAttributes(first)
	if got := attrString(t, firstAttrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "assigned" {
		t.Fatalf("first turn reason: want assigned, got %s", got)
	}
	if got := attrString(t, firstAttrs, semconv.LLMDEPPScreenerSessionKeySourceKey); got != "attribute" {
		t.Fatalf("session key source: want attribute, got %s", got)
	}
	if got := attrString(t, firstAttrs, semconv.LLMDEPPScreenerSessionKeyKey); got != "session" {
		t.Fatalf("session key: want session, got %s", got)
	}

	sticky := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), sessionRequest("session", 1), candidates)
	})
	attrs := spanAttributes(sticky)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "sticky" {
		t.Fatalf("second turn reason: want sticky, got %s", got)
	}
	selected := attrString(t, attrs, semconv.LLMDEPPScreenerSelectedRevisionKey)
	previous := attrString(t, attrs, semconv.LLMDEPPScreenerPreviousRevisionKey)
	if selected == "" || selected != previous {
		t.Fatalf("sticky reuse must keep the session's revision: selected=%s previous=%s", selected, previous)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerCoordinationBackendKey); got != "local" {
		t.Fatalf("coordination backend: want local, got %s", got)
	}
	if _, ok := attrs[semconv.LLMDEPPScreenerCandidateEndpointsKey]; !ok {
		t.Fatal("span must record candidate endpoint count")
	}
	if _, ok := attrs[semconv.LLMDEPPScreenerFilteredEndpointsKey]; !ok {
		t.Fatal("span must record filtered endpoint count")
	}
}

func TestScreenerTracesInvalidation(t *testing.T) {
	recorder := setupSpanRecorder(t)
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	candidates := candidatePool(1, 1)

	pinned := servedRevision(screenCandidates(t, screener, sessionRequest("session", 0), candidates))
	if pinned == "" {
		t.Fatal("first turn screened to no endpoint")
	}
	flipRevisionNotReady(t, screener, pinned)

	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), sessionRequest("session", 1), candidates)
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "invalidated" {
		t.Fatalf("invalidation reason: want invalidated, got %s", got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerPreviousRevisionKey); got != pinned {
		t.Fatalf("previous revision: want the invalidated %s, got %s", pinned, got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerSelectedRevisionKey); got == "" || got == pinned {
		t.Fatalf("selected revision must replace the invalidated one: got %s", got)
	}
}

func TestScreenerTracesCoordinatedOverride(t *testing.T) {
	recorder := setupSpanRecorder(t)
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "v1")
	syncer.seed(screener.revisionDecisionStateKey, "decision-id", "v2")
	screener.handle.SetCrossReplicaSyncer(syncer)

	request := sessionRequest("session", 0)
	request.Headers[reqcommon.RevisionDecisionIDHeaderKey] = "decision-id"
	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), request, candidatePool(1, 1))
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "coordinated" {
		t.Fatalf("override reason: want coordinated, got %s", got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerPreviousRevisionKey); got != "v1" {
		t.Fatalf("previous revision: want the session's v1, got %s", got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerSelectedRevisionKey); got != "v2" {
		t.Fatalf("selected revision: want the coordinated v2, got %s", got)
	}
}

func TestScreenerTracesStorageFailure(t *testing.T) {
	recorder := setupSpanRecorder(t)
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	syncer.seed(screener.sessionStateKey, "session", "v1")
	syncer.err = errors.New("redis unavailable")
	screener.handle.SetCrossReplicaSyncer(syncer)

	// The session decision cannot be read: the request fails closed, and the
	// span records the storage failure even though no revision was served.
	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), sessionRequest("session", 0), candidatePool(1, 1))
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "storage_error" {
		t.Fatalf("failure reason: want storage_error, got %s", got)
	}
	if span.Status().Code != codes.Error {
		t.Fatalf("storage failure must mark the span errored: %v", span.Status())
	}
}

// A session minted by this request's shared-store GetOrSet is an assignment,
// not a sticky hit: the span must not claim a previous revision for it.
func TestScreenerTracesFreshSharedSessionAssignment(t *testing.T) {
	recorder := setupSpanRecorder(t)
	config := validConfig()
	enabled := true
	config.RevisionGating.SessionAffinity = &SessionAffinity{Enabled: enabled}
	screener := newTestScreener(config)
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})
	syncer := &decisionSyncer{}
	screener.handle.SetCrossReplicaSyncer(syncer)

	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), sessionRequest("session", 0), candidatePool(1, 1))
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "assigned" {
		t.Fatalf("fresh shared session reason: want assigned, got %s", got)
	}
	if _, ok := attrs[semconv.LLMDEPPScreenerPreviousRevisionKey]; ok {
		t.Fatal("fresh assignment must not record a previous revision")
	}
}

func TestScreenerTracesStrictHeader(t *testing.T) {
	recorder := setupSpanRecorder(t)
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
	})

	request := sessionRequest("session", 0)
	request.Headers["x-llm-d-disagg-revision"] = "v1"
	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), request, candidatePool(1, 1))
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "strict_header" {
		t.Fatalf("strict header reason: want strict_header, got %s", got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerSelectedRevisionKey); got != "v1" {
		t.Fatalf("selected revision: want v1, got %s", got)
	}
}

func TestScreenerTracesStrictHeaderWithGatingDisabled(t *testing.T) {
	recorder := setupSpanRecorder(t)
	config := validConfig()
	config.RevisionGating = &RevisionGating{Mode: GatingModeDisabled}
	screener := newTestScreener(config)

	request := sessionRequest("session", 0)
	request.Headers["x-llm-d-disagg-revision"] = "v1"
	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), request, candidatePool(1, 1))
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerDecisionReasonKey); got != "strict_header" {
		t.Fatalf("strict header reason with gating disabled: want strict_header, got %s", got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerSelectedRevisionKey); got != "v1" {
		t.Fatalf("selected revision: want v1, got %s", got)
	}
}

func TestScreenerTracesPrefixHashSource(t *testing.T) {
	recorder := setupSpanRecorder(t)
	screener := newTestScreener(sessionAffinityConfig())
	seedCounts(t, screener, map[string]map[string]int{
		"v1": {"prefill": 1, "decode": 1},
		"v2": {"prefill": 1, "decode": 1},
	})

	request := sessionRequest("", 0)
	span := screenSpan(t, recorder, func() {
		screener.Screen(context.Background(), request, candidatePool(1, 1))
	})
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPScreenerSessionKeySourceKey); got != "prefix_hash" {
		t.Fatalf("session key source: want prefix_hash, got %s", got)
	}
}
