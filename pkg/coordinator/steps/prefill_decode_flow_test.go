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

package steps

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/common/usage"
	"github.com/llm-d/llm-d-router/pkg/coordinator/config"
	"github.com/llm-d/llm-d-router/pkg/coordinator/connectors/kv"
	"github.com/llm-d/llm-d-router/pkg/coordinator/gateway"
	"github.com/llm-d/llm-d-router/pkg/coordinator/pipeline"
)

// TestECTransferParams_NotForwardedToDecodeBackend verifies that ec_transfer_params
// accumulated by the encode step are never included in the decode request body.
// The prefill step uses a copy of reqCtx.Body, so it cannot contaminate the shared
// body map; this test guards against regressions where that isolation breaks.
func TestECTransferParams_NotForwardedToDecodeBackend(t *testing.T) {
	var decodeBody map[string]any

	gwServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhaseDecode {
			raw, _ := io.ReadAll(r.Body)
			_ = json.Unmarshal(raw, &decodeBody)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"choices": []map[string]any{
				{"message": map[string]any{"role": "assistant", "content": "ok"}},
			},
		})
	}))
	defer gwServer.Close()

	gwClient := gateway.New(config.GatewayConfig{Address: gwServer.URL})
	decodeStep, _ := NewDecodeStep(gwClient, map[string]any{ParamKVConnector: kv.NIXL})

	reqCtx := &pipeline.RequestContext{
		RequestID:    "test-no-ec",
		OriginalPath: reqcommon.PathChatCompletions,
		Model:        "llama-3",
		Stream:       false,
		// Simulate encode step having populated ECTransferParams.
		ECTransferParams: []map[string]any{
			{"img-hash-1": map[string]any{"peer_host": "10.0.0.5", "peer_port": float64(5500)}},
		},
		KVTransferParams: map[string]any{"block_id": "blk-1"},
		Body: map[string]any{
			"model":    "llama-3",
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		},
	}

	recorder := httptest.NewRecorder()
	reqCtx.ResponseWriter = recorder

	if err := decodeStep.Execute(context.Background(), reqCtx); err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if decodeBody == nil {
		t.Fatal("decode step did not reach the backend")
	}
	if _, present := decodeBody["ec_transfer_params"]; present {
		t.Errorf("ec_transfer_params must not be forwarded to the decode backend, got: %v", decodeBody["ec_transfer_params"])
	}
}

func TestKVTransferParams_FlowFromPrefillToDecode(t *testing.T) {
	const revisionDecisionID = "decision-id"
	expectedKVParams := map[string]any{
		"block_id":  "block-999",
		"peer_host": "10.0.0.42",
		"peer_port": float64(7777),
	}

	var decodeReceivedKVParams map[string]any

	gwServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		phase := r.Header.Get(reqcommon.EPPProfileHeaderKey)
		if got := r.Header.Get(reqcommon.RevisionDecisionIDHeaderKey); got != revisionDecisionID {
			t.Errorf("%s revision decision ID = %q, want %q", phase, got, revisionDecisionID)
		}
		switch phase {
		case gateway.PhasePrefill:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"kv_transfer_params": expectedKVParams,
			})

		case gateway.PhaseDecode:
			body, _ := io.ReadAll(r.Body)
			var parsed map[string]any
			_ = json.Unmarshal(body, &parsed)
			decodeReceivedKVParams, _ = parsed["kv_transfer_params"].(map[string]any)

			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "content": "done"}},
				},
			})

		default:
			http.Error(w, "not found", 404)
		}
	}))
	defer gwServer.Close()

	gwClient := gateway.New(config.GatewayConfig{Address: gwServer.URL})

	// Run prefill step
	prefillStep, _ := NewPrefillStep(gwClient, map[string]any{ParamKVConnector: kv.NIXL})

	reqCtx := &pipeline.RequestContext{
		RequestID:          "test-flow",
		RevisionDecisionID: revisionDecisionID,
		OriginalPath:       reqcommon.PathChatCompletions,
		Model:              "llama-3",
		Stream:             false,
		TokenIDs:           []int{1, 32000, 32000, 32000, 2345},
		MultimodalEntries: []pipeline.MultimodalEntry{
			{Modality: reqcommon.ModalityImage, Hash: "hash-1", Placeholder: pipeline.PlaceholderRange{Offset: 1, Length: 3}},
		},
		ECTransferParams: []map[string]any{
			{"hash-1": map[string]any{"peer_host": "10.0.0.1", "peer_port": 5501}},
		},
		KVTransferParams: make(map[string]any),
		Body: map[string]any{
			"model":  "llama-3",
			"stream": false,
			"messages": []any{
				map[string]any{
					"role": "user",
					"content": []any{
						map[string]any{
							"type":      "image_url",
							"image_url": map[string]any{"url": "https://example.com/img.jpg"},
						},
					},
				},
			},
		},
	}

	err := prefillStep.Execute(context.Background(), reqCtx)
	if err != nil {
		t.Fatalf("prefill failed: %v", err)
	}

	if reqCtx.KVTransferParams["block_id"] != "block-999" {
		t.Fatalf("prefill did not set kv_transfer_params correctly: %v", reqCtx.KVTransferParams)
	}

	// Run decode step
	decodeStep, _ := NewDecodeStep(gwClient, map[string]any{ParamKVConnector: kv.NIXL})

	recorder := httptest.NewRecorder()
	reqCtx.ResponseWriter = recorder

	err = decodeStep.Execute(context.Background(), reqCtx)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if decodeReceivedKVParams == nil {
		t.Fatal("decode did not send kv_transfer_params to gateway")
	}
	if decodeReceivedKVParams["block_id"] != "block-999" {
		t.Fatalf("decode sent wrong block_id: %v", decodeReceivedKVParams["block_id"])
	}
	if decodeReceivedKVParams["peer_host"] != "10.0.0.42" {
		t.Fatalf("decode sent wrong peer_host: %v", decodeReceivedKVParams["peer_host"])
	}
}

func TestResponseHeaders_FlowFromPrefillToDecode(t *testing.T) {
	var decodeHeaders http.Header

	gwServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Header.Get(reqcommon.EPPProfileHeaderKey) {
		case gateway.PhasePrefill:
			w.Header().Set("X-LLM-D-Disagg-Revision", "revision-b")
			w.Header().Set("X-Disagg-Slice", "nvl72-domain-2")
			w.Header().Set("X-Worker-Only", "must-not-be-forwarded")
			_ = json.NewEncoder(w).Encode(map[string]any{"kv_transfer_params": map[string]any{}})

		case gateway.PhaseDecode:
			decodeHeaders = r.Header.Clone()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"choices": []map[string]any{
					{"message": map[string]any{"role": "assistant", "content": "done"}},
				},
			})

		default:
			http.Error(w, "not found", http.StatusNotFound)
		}
	}))
	defer gwServer.Close()

	gwClient := gateway.New(config.GatewayConfig{Address: gwServer.URL})
	prefillStep, err := NewPrefillStep(gwClient, map[string]any{})
	if err != nil {
		t.Fatalf("build prefill step: %v", err)
	}

	reqCtx := &pipeline.RequestContext{
		RequestID:    "test-header-flow",
		OriginalPath: reqcommon.PathChatCompletions,
		OriginalHeaders: http.Header{
			"X-LLM-D-Disagg-Revision": {"client-revision"},
			"X-Disagg-Slice":          {"client-slice"},
		},
		Body: map[string]any{
			"model":    "llama-3",
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		},
	}

	decodeStep, err := NewDecodeStep(gwClient, map[string]any{})
	if err != nil {
		t.Fatalf("build decode step: %v", err)
	}
	reqCtx.ResponseWriter = httptest.NewRecorder()
	p, err := pipeline.NewWithForwardResponseHeaders(
		[]pipeline.Step{prefillStep, decodeStep},
		[]string{"X-LLM-D-Disagg-Revision", "x-disagg-slice"},
	)
	if err != nil {
		t.Fatalf("build pipeline: %v", err)
	}
	if err := p.Execute(context.Background(), reqCtx); err != nil {
		t.Fatalf("pipeline failed: %v", err)
	}

	if got := decodeHeaders.Get("X-LLM-D-Disagg-Revision"); got != "revision-b" {
		t.Errorf("decode revision header = %q, want %q", got, "revision-b")
	}
	if got := decodeHeaders.Get("X-Disagg-Slice"); got != "nvl72-domain-2" {
		t.Errorf("decode slice header = %q, want %q", got, "nvl72-domain-2")
	}
	if got := decodeHeaders.Get("X-Worker-Only"); got != "" {
		t.Errorf("unconfigured response header was forwarded: %q", got)
	}
}

// A decode worker that received KV blocks from prefill counts them as its own
// prompt-cache hits, so the count the client must see is the prefill worker's.
func TestCachedTokens_FlowFromPrefillToDecode(t *testing.T) {
	const (
		prefillCachedTokens = 11
		decodeCachedTokens  = 500
	)

	tests := []struct {
		name   string
		stream bool
	}{
		{name: "non-streaming"},
		{name: "streaming", stream: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gwServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.Header.Get(reqcommon.EPPProfileHeaderKey) {
				case gateway.PhasePrefill:
					_ = json.NewEncoder(w).Encode(map[string]any{
						"kv_transfer_params": map[string]any{"block_id": "block-1"},
						"usage":              usageBlock(prefillCachedTokens),
					})
				case gateway.PhaseDecode:
					if test.stream {
						writeUsageSSE(t, w, decodeCachedTokens)
						return
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"usage": usageBlock(decodeCachedTokens)})
				default:
					http.Error(w, "not found", http.StatusNotFound)
				}
			}))
			defer gwServer.Close()

			gwClient := gateway.New(config.GatewayConfig{Address: gwServer.URL})
			reqCtx := cachedTokensRequestContext(test.stream)

			prefillStep, _ := NewPrefillStep(gwClient, map[string]any{ParamKVConnector: kv.NIXL})
			if err := prefillStep.Execute(context.Background(), reqCtx); err != nil {
				t.Fatalf("prefill failed: %v", err)
			}
			if reqCtx.PrefillCachedTokens == nil || *reqCtx.PrefillCachedTokens != prefillCachedTokens {
				t.Fatalf("prefill cached tokens = %v, want %d", reqCtx.PrefillCachedTokens, prefillCachedTokens)
			}

			decodeStep, _ := NewDecodeStep(gwClient, map[string]any{ParamKVConnector: kv.NIXL})
			recorder := httptest.NewRecorder()
			reqCtx.ResponseWriter = recorder
			if err := decodeStep.Execute(context.Background(), reqCtx); err != nil {
				t.Fatalf("decode failed: %v", err)
			}

			if got := clientCachedTokens(t, recorder.Body.String()); got != prefillCachedTokens {
				t.Errorf("client cached_tokens = %d, want the prefill worker's %d", got, prefillCachedTokens)
			}
			if test.stream && !strings.Contains(recorder.Body.String(), reqcommon.SSEDoneMarker) {
				t.Errorf("rewritten stream dropped the done marker: %s", recorder.Body.String())
			}
		})
	}
}

// Without a prefill phase there is nothing to correct, and the decode worker's
// own count is the one the client must see.
func TestCachedTokens_DecodeOnlyKeepsTheWorkerCount(t *testing.T) {
	const decodeCachedTokens = 500

	gwServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"usage": usageBlock(decodeCachedTokens)})
	}))
	defer gwServer.Close()

	gwClient := gateway.New(config.GatewayConfig{Address: gwServer.URL})
	decodeStep, _ := NewDecodeStep(gwClient, map[string]any{ParamKVConnector: kv.NIXL})

	reqCtx := cachedTokensRequestContext(false)
	recorder := httptest.NewRecorder()
	reqCtx.ResponseWriter = recorder
	if err := decodeStep.Execute(context.Background(), reqCtx); err != nil {
		t.Fatalf("decode failed: %v", err)
	}

	if got := clientCachedTokens(t, recorder.Body.String()); got != decodeCachedTokens {
		t.Errorf("client cached_tokens = %d, want the decode worker's %d", got, decodeCachedTokens)
	}
}

func cachedTokensRequestContext(stream bool) *pipeline.RequestContext {
	return &pipeline.RequestContext{
		RequestID:        "test-cached-tokens",
		OriginalPath:     reqcommon.PathChatCompletions,
		Model:            "llama-3",
		Stream:           stream,
		KVTransferParams: make(map[string]any),
		Body: map[string]any{
			"model":    "llama-3",
			"stream":   stream,
			"messages": []any{map[string]any{"role": "user", "content": "hello"}},
		},
	}
}

func usageBlock(cachedTokens int) map[string]any {
	return map[string]any{
		"prompt_tokens":         64,
		"prompt_tokens_details": map[string]any{"cached_tokens": cachedTokens},
	}
}

// writeUsageSSE sends a content frame, the usage frame, and the done marker, the
// shape vLLM streams for stream_options.include_usage.
func writeUsageSSE(t *testing.T, w http.ResponseWriter, cachedTokens int) {
	t.Helper()
	w.Header().Set("Content-Type", "text/event-stream")
	frames := []map[string]any{
		{"choices": []any{map[string]any{"delta": map[string]any{"content": "hi"}}}},
		{"choices": []any{}, "usage": usageBlock(cachedTokens)},
	}
	for _, frame := range frames {
		encoded, err := json.Marshal(frame)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(reqcommon.SSEDataPrefix + string(encoded) + "\n\n")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := w.Write([]byte(reqcommon.SSEDataPrefix + reqcommon.SSEDoneMarker + "\n\n")); err != nil {
		t.Fatal(err)
	}
}

// clientCachedTokens reads usage.prompt_tokens_details.cached_tokens out of the
// response the client received, whether it is one JSON document or an SSE stream.
func clientCachedTokens(t *testing.T, body string) int {
	t.Helper()
	documents := []string{body}
	if strings.HasPrefix(body, reqcommon.SSEDataPrefix) {
		documents = nil
		for line := range strings.SplitSeq(body, "\n") {
			data, ok := strings.CutPrefix(line, reqcommon.SSEDataPrefix)
			if ok && strings.TrimSpace(data) != reqcommon.SSEDoneMarker {
				documents = append(documents, data)
			}
		}
	}
	for _, document := range documents {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(document), &parsed); err != nil {
			t.Fatalf("unmarshal %s: %v", document, err)
		}
		if cachedTokens, ok := usage.CachedTokens(parsed[reqcommon.FieldUsage]); ok {
			return cachedTokens
		}
	}
	t.Fatalf("no cached_tokens in the response the client received: %s", body)
	return 0
}
