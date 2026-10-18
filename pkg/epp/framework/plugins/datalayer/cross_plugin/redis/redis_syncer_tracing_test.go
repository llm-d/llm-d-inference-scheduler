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

package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/llm-d/llm-d-router/pkg/common/observability/semconv"
	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
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

// lastSpanNamed returns the most recent recorded span with the given name.
func lastSpanNamed(t *testing.T, recorder *tracetest.SpanRecorder, name string) sdktrace.ReadOnlySpan {
	t.Helper()
	var found sdktrace.ReadOnlySpan
	for _, span := range recorder.Ended() {
		if span.Name() == name {
			found = span
		}
	}
	if found == nil {
		t.Fatalf("no %s span was recorded", name)
	}
	return found
}

func TestGetOrSetTracesStateKeyAndDecisionID(t *testing.T) {
	recorder := setupSpanRecorder(t)
	server := miniredis.RunT(t)
	syncer, _ := newTestSyncer(t, server, testReplicaID)

	_, _, err := syncer.GetOrSet(context.Background(), fwkdl.StateKey("sessions"), "s1", "v1")
	if err != nil {
		t.Fatalf("GetOrSet failed: %v", err)
	}

	span := lastSpanNamed(t, recorder, "redis_syncer.get_or_set")
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPSyncerStateKeyKey); got != "sessions" {
		t.Fatalf("state key: want sessions, got %s", got)
	}
	if got := attrString(t, attrs, semconv.LLMDEPPSyncerDecisionIDKey); got != "s1" {
		t.Fatalf("decision id: want s1, got %s", got)
	}
	if span.Status().Code == codes.Error {
		t.Fatalf("successful operation must not mark the span errored: %v", span.Status())
	}
}

func TestReplaceFailureStaysVisibleInTrace(t *testing.T) {
	recorder := setupSpanRecorder(t)
	server := miniredis.RunT(t)
	syncer, _ := newTestSyncer(t, server, testReplicaID)

	// A closed server makes the swap fail; the span must record the error
	// even though the caller may recover from it.
	server.Close()
	_, _, err := syncer.Replace(context.Background(), fwkdl.StateKey("sessions"), "s1", "v1", "v2")
	if err == nil {
		t.Fatal("Replace against a closed server must fail")
	}

	span := lastSpanNamed(t, recorder, "redis_syncer.replace")
	attrs := spanAttributes(span)
	if got := attrString(t, attrs, semconv.LLMDEPPSyncerDecisionIDKey); got != "s1" {
		t.Fatalf("decision id: want s1, got %s", got)
	}
	if span.Status().Code != codes.Error {
		t.Fatalf("failed operation must mark the span errored: %v", span.Status())
	}
}
