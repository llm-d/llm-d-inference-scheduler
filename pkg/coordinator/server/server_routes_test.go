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

package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/coordinator/config"
	"github.com/llm-d/llm-d-router/pkg/coordinator/gateway"
	"github.com/llm-d/llm-d-router/pkg/coordinator/pipeline"
)

func TestInferenceRoutesUsePipeline(t *testing.T) {
	upstream, cap := newCapturingUpstream(t, http.StatusAccepted, "passthrough")
	for _, path := range []string{
		reqcommon.PathChatCompletions,
		reqcommon.PathCompletions,
		reqcommon.PathResponses,
		reqcommon.PathMessages,
		reqcommon.PathVLLMGenerate,
	} {
		for _, suffix := range []string{"", "/"} {
			t.Run(path+suffix, func(t *testing.T) {
				var executed []string
				step := &capturingStep{}
				p := pipeline.New([]pipeline.Step{
					stubStep{name: "prefill", fn: func(context.Context, *pipeline.RequestContext) error {
						executed = append(executed, "prefill")
						return nil
					}},
					stubStep{name: "decode", fn: func(context.Context, *pipeline.RequestContext) error {
						executed = append(executed, "decode")
						return nil
					}},
					step,
				})
				srv, err := New(config.ServerConfig{}, p, gateway.NewWithTransport(&http.Transport{}, upstream.URL))
				require.NoError(t, err)

				req := httptest.NewRequest(http.MethodPost, path+suffix, strings.NewReader(`{"model":"m"}`))
				rec := httptest.NewRecorder()
				srv.httpServer.Handler.ServeHTTP(rec, req)

				require.Equal(t, http.StatusOK, rec.Code)
				require.Equal(t, []string{"prefill", "decode"}, executed)
				require.NotNil(t, step.captured)
				require.Equal(t, path, step.captured.OriginalPath)
				require.Equal(t, inferenceRoute(path), step.captured.Route)
			})
		}
	}
	method, _, _, _, _ := cap.get()
	require.Empty(t, method, "inference requests must not reach the passthrough gateway")
}

func TestInferenceRoutesTrailingSlashValidation(t *testing.T) {
	upstream, _ := newCapturingUpstream(t, http.StatusAccepted, "passthrough")
	for _, tc := range []struct {
		path string
		body string
	}{
		{reqcommon.PathChatCompletions, "invalid"},
		{reqcommon.PathCompletions, "invalid"},
		{reqcommon.PathResponses, `{"model":"m","previous_response_id":"resp-123"}`},
		{reqcommon.PathMessages, "invalid"},
		{reqcommon.PathVLLMGenerate, "invalid"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			step := &capturingStep{}
			srv, err := New(config.ServerConfig{}, pipeline.New([]pipeline.Step{step}), gateway.NewWithTransport(&http.Transport{}, upstream.URL))
			require.NoError(t, err)

			req := httptest.NewRequest(http.MethodPost, tc.path+"/", strings.NewReader(tc.body))
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusBadRequest, rec.Code)
			require.Nil(t, step.captured, "invalid requests must not reach the pipeline")
			require.Contains(t, rec.Body.String(), `"type":"invalid_request_error"`)
		})
	}
}

func TestInferenceRoutesTrailingSlashMethodNotAllowed(t *testing.T) {
	upstream, _ := newCapturingUpstream(t, http.StatusAccepted, "passthrough")
	srv := newTestServerWithGateway(nil, upstream.URL)
	for _, path := range []string{
		reqcommon.PathChatCompletions,
		reqcommon.PathCompletions,
		reqcommon.PathResponses,
		reqcommon.PathMessages,
		reqcommon.PathVLLMGenerate,
	} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path+"/", nil)
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusMethodNotAllowed, rec.Code)
		})
	}
}

func TestPassthroughTrailingSlashPreserved(t *testing.T) {
	upstream, cap := newCapturingUpstream(t, http.StatusAccepted, "passthrough")
	srv := newTestServerWithGateway(nil, upstream.URL)
	body := `{"model":"m"}`
	for _, path := range []string{"/v1/models/", "/v1/messages/count_tokens/"} {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, path+"?beta=true", strings.NewReader(body))
			rec := httptest.NewRecorder()
			srv.httpServer.Handler.ServeHTTP(rec, req)

			require.Equal(t, http.StatusAccepted, rec.Code)
			method, gotPath, query, headers, gotBody := cap.get()
			require.Equal(t, http.MethodPost, method)
			require.Equal(t, path, gotPath)
			require.Equal(t, "beta=true", query)
			require.Equal(t, body, string(gotBody))
			require.Equal(t, gateway.PhaseDecode, headers.Get(reqcommon.EPPProfileHeaderKey))
		})
	}
}

// auxRouteStep is a stub step that registers an auxiliary route.
type auxRouteStep struct {
	stubStep
	registered bool
	gotID      string
}

func (s *auxRouteStep) RegisterRoutes(r chi.Router) {
	s.registered = true
	r.Get("/v1/requests/{id}", func(w http.ResponseWriter, r *http.Request) {
		s.gotID = chi.URLParam(r, "id")
		w.WriteHeader(http.StatusOK)
	})
}

func TestServerRegistersStepRoutes(t *testing.T) {
	step := &auxRouteStep{stubStep: stubStep{name: "aux"}}
	p := pipeline.New([]pipeline.Step{stubStep{name: "plain"}, step})
	srv, err := New(config.ServerConfig{}, p, gateway.NewWithTransport(nil, stubGatewayURL))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if !step.registered {
		t.Fatal("RegisterRoutes was not called on the implementing step")
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/requests/abc-123", nil)
	rec := httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from step route, got %d", rec.Code)
	}
	if step.gotID != "abc-123" {
		t.Fatalf("expected path param in handler, got %q", step.gotID)
	}

	// The built-in routes are untouched.
	req = httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec = httptest.NewRecorder()
	srv.httpServer.Handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 from /healthz, got %d", rec.Code)
	}
}
