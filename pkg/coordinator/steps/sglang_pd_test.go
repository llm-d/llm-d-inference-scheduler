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
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	logutil "github.com/llm-d/llm-d-router/pkg/common/observability/logging"
	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/common/routing"
	"github.com/llm-d/llm-d-router/pkg/coordinator/connectors/kv"
	"github.com/llm-d/llm-d-router/pkg/coordinator/gateway"
	"github.com/llm-d/llm-d-router/pkg/coordinator/pipeline"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/common/request"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

func TestSGLangPDStartsDecodeBeforePrefillBody(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ctx = log.IntoContext(ctx, funcr.New(func(_, _ string) {}, funcr.Options{Verbosity: logutil.TRACE}))
	wantPort := kv.ResolveSGLangBootstrapPort(ctx)
	releasePrefill := make(chan struct{})
	prefillClosed := make(chan struct{})
	var prefill map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			return
		}
		if body[reqcommon.FieldBootstrapPort] != float64(wantPort) {
			t.Errorf("bootstrap port = %v, want shared SGLang port %d", body[reqcommon.FieldBootstrapPort], wantPort)
		}
		if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhasePrefill {
			prefill = body
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set(routing.PrefillEndpointHeader, "10.0.0.7:8000")
			w.Header().Set(reqcommon.PeerTopologyHeaderKey, "host=node7")
			w.(http.Flusher).Flush()
			defer close(prefillClosed)
			select {
			case <-releasePrefill:
				_, _ = io.WriteString(w, "data: [DONE]\n\n")
			case <-r.Context().Done():
			}
			return
		}
		if body[reqcommon.FieldBootstrapRoom] != prefill[reqcommon.FieldBootstrapRoom] {
			t.Error("bootstrap rooms differ")
		}
		if body[reqcommon.FieldBootstrapHost] != "10.0.0.7" {
			t.Errorf("wrong bootstrap host: %v", body)
		}
		if r.Header.Get(routing.PrefillEndpointHeader) != "10.0.0.7:8000" {
			t.Error("missing endpoint header")
		}
		if r.Header.Get(reqcommon.PeerTopologyHeaderKey) != "host=node7" {
			t.Error("missing topology header")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: decode\n\n")
	}))
	defer gw.Close()
	step, err := NewSGLangPDStep(gateway.NewWithTransport(nil, gw.URL), nil)
	require.NoError(t, err)
	rec := httptest.NewRecorder()
	rc := &pipeline.RequestContext{OriginalPath: reqcommon.PathSGLangGenerate, Stream: true,
		Body: map[string]any{"text": "hello", "stream": true, "sampling_params": map[string]any{"max_new_tokens": 1}}, ResponseWriter: rec}
	err = step.Execute(ctx, rc)
	require.ErrorIs(t, err, pipeline.ErrPipelineDone)
	require.NoError(t, ctx.Err(), "decode waited for prefill body")
	require.Contains(t, rec.Body.String(), "decode")
	select {
	case <-prefillClosed:
		t.Fatal("successful decode cancelled prefill")
	default:
	}
	close(releasePrefill)
	select {
	case <-prefillClosed:
	case <-ctx.Done():
		t.Fatal("prefill did not finish")
	}
	require.NotContains(t, rc.Body, reqcommon.FieldBootstrapRoom)
}

func TestSGLangPDRejectsMissingEndpoint(t *testing.T) {
	var decode bool
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhaseDecode {
			decode = true
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
	}))
	defer gw.Close()
	step, err := NewSGLangPDStep(gateway.NewWithTransport(nil, gw.URL), nil)
	require.NoError(t, err)
	err = step.Execute(t.Context(), &pipeline.RequestContext{OriginalPath: reqcommon.PathSGLangGenerate, Stream: true, Body: map[string]any{"stream": true}})
	require.Error(t, err)
	require.False(t, errors.Is(err, pipeline.ErrPipelineDone))
	require.False(t, decode)
}

func TestSGLangPDPrefillFailureCancelsDecode(t *testing.T) {
	for _, event := range []string{
		"data: {\"error\":{\"message\":\"failed\"}}\n\n",
		"data: {\"meta_info\":{\"finish_reason\":{\"type\":\"abort\"}}}\n\n",
		"data: {}\n\n", // EOF without [DONE].
	} {
		t.Run(event, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			decodeStarted := make(chan struct{})
			decodeCancelled := make(chan struct{})
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhasePrefill {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set(routing.PrefillEndpointHeader, "10.0.0.7:8000")
					w.(http.Flusher).Flush()
					select {
					case <-decodeStarted:
						_, _ = io.WriteString(w, event)
					case <-r.Context().Done():
					}
					return
				}
				close(decodeStarted)
				<-r.Context().Done()
				close(decodeCancelled)
			}))
			defer gw.Close()
			step, err := NewSGLangPDStep(gateway.NewWithTransport(nil, gw.URL), nil)
			require.NoError(t, err)
			rec := httptest.NewRecorder()
			err = step.Execute(ctx, &pipeline.RequestContext{OriginalPath: reqcommon.PathSGLangGenerate, Stream: true,
				Body: map[string]any{"text": "hello", "stream": true}, ResponseWriter: rec})
			var streamed *pipeline.UpstreamStreamedError
			require.Error(t, err)
			require.False(t, errors.As(err, &streamed), "no decode response was written; the server must send an error")
			require.NoError(t, ctx.Err())
			require.Contains(t, err.Error(), "prefill stream")
			select {
			case <-decodeCancelled:
			case <-ctx.Done():
				t.Fatal("decode not cancelled")
			}
		})
	}
}

func TestSGLangPDCancellation(t *testing.T) {
	for _, mode := range []string{"client", "decode failure", "prefill grace"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			prefillClosed := make(chan struct{})
			gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhasePrefill {
					w.Header().Set("Content-Type", "text/event-stream")
					w.Header().Set(routing.PrefillEndpointHeader, "10.0.0.7:8000")
					w.(http.Flusher).Flush()
					<-r.Context().Done()
					close(prefillClosed)
					return
				}
				switch mode {
				case "client":
					cancel()
					<-r.Context().Done()
				case "decode failure":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "prefill grace":
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
				}
			}))
			defer gw.Close()
			step, err := NewSGLangPDStep(gateway.NewWithTransport(nil, gw.URL), map[string]any{"prefill_grace": "10ms"})
			require.NoError(t, err)
			err = step.Execute(ctx, &pipeline.RequestContext{OriginalPath: reqcommon.PathSGLangGenerate, Stream: true,
				Body: map[string]any{"stream": true}, ResponseWriter: httptest.NewRecorder()})
			if mode == "prefill grace" {
				require.ErrorIs(t, err, pipeline.ErrPipelineDone)
			} else {
				require.Error(t, err)
				require.NotErrorIs(t, err, pipeline.ErrPipelineDone)
			}
			select {
			case <-prefillClosed:
			case <-time.After(2 * time.Second):
				t.Fatal("prefill was not cancelled")
			}
		})
	}
}

func TestSGLangPDPrefillFailureAbortsStreamingClient(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	failPrefill := make(chan struct{})
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", request.MediaTypeEventStream)
		if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhasePrefill {
			w.Header().Set(routing.PrefillEndpointHeader, "10.0.0.7:8000")
			w.(http.Flusher).Flush()
			select {
			case <-failPrefill:
				_, _ = io.WriteString(w, "data: {\"error\":\"failed\"}\n\n")
			case <-r.Context().Done():
			}
			return
		}
		_, _ = io.WriteString(w, "data: partial\n\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
	}))
	defer gw.Close()
	step, err := NewSGLangPDStep(gateway.NewWithTransport(nil, gw.URL), nil)
	require.NoError(t, err)
	coordinator := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = step.Execute(r.Context(), &pipeline.RequestContext{OriginalPath: reqcommon.PathSGLangGenerate, Stream: true,
			Body: map[string]any{"stream": true}, ResponseWriter: w})
	}))
	defer coordinator.Close()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, coordinator.URL, nil)
	require.NoError(t, err)
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	reader := bufio.NewReader(resp.Body)
	line, err := reader.ReadString('\n')
	require.NoError(t, err)
	require.Equal(t, "data: partial\n", line)
	close(failPrefill)
	_, err = io.ReadAll(reader)
	require.ErrorIs(t, err, io.ErrUnexpectedEOF)
	require.NoError(t, ctx.Err())
}
