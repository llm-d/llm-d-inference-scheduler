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

package proxy

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/sidecar/constants"
)

// TestConcurrentPDCancellation covers which request a concurrent P/D failure
// cancels. Decode cannot receive KV after a failed prefill, and SGLang does not
// notify decode of it, so decode would otherwise wait for its KV transfer timeout.
func TestConcurrentPDCancellation(t *testing.T) {
	tests := []struct {
		name string
		// prefillStatus is returned once decode has started; zero blocks prefill until canceled.
		prefillStatus int
		// decode runs on the decode server once prefill has started.
		decode              func(w http.ResponseWriter, r *http.Request, cancelClient context.CancelFunc)
		wantPrefillCanceled bool
	}{
		{
			name:          "prefill failure cancels decode",
			prefillStatus: http.StatusServiceUnavailable,
			decode: func(_ http.ResponseWriter, r *http.Request, _ context.CancelFunc) {
				<-r.Context().Done()
			},
		},
		{
			name: "client disconnect cancels prefill",
			decode: func(_ http.ResponseWriter, _ *http.Request, cancelClient context.CancelFunc) {
				cancelClient()
			},
			wantPrefillCanceled: true,
		},
		{
			name: "successful decode leaves prefill running",
			decode: func(w http.ResponseWriter, _ *http.Request, _ context.CancelFunc) {
				w.WriteHeader(http.StatusOK)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()

			prefillStarted := make(chan struct{})
			prefillCanceled := make(chan struct{})
			decodeStarted := make(chan struct{})
			prefill := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(prefillStarted)
				if tt.prefillStatus != 0 {
					select {
					case <-decodeStarted:
						w.WriteHeader(tt.prefillStatus)
					case <-r.Context().Done():
					}
					return
				}
				select {
				case <-r.Context().Done():
					close(prefillCanceled)
				case <-t.Context().Done():
				}
			}))
			// Cleanup, not defer: t.Context() is canceled before Cleanup runs, which releases prefill.
			t.Cleanup(prefill.Close)
			decode := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				close(decodeStarted)
				select {
				case <-prefillStarted:
					tt.decode(w, r, cancel)
				case <-r.Context().Done():
				}
			}))
			defer decode.Close()

			prefillURL, err := url.Parse(prefill.URL)
			require.NoError(t, err)
			decodeURL, err := url.Parse(decode.URL)
			require.NoError(t, err)
			s := NewProxy(Config{Port: "0", DecoderURL: decodeURL})
			s.decoderProxy = s.createDecoderProxyHandler(decodeURL, false)

			body := []byte(`{"input_ids":[1]}`)
			req := httptest.NewRequest(http.MethodPost, reqcommon.PathSGLangGenerate, bytes.NewReader(body)).WithContext(ctx)
			s.runConcurrentPD(httptest.NewRecorder(), req, body, body, prefillURL.Host, constants.KVConnectorSGLang, nil)
			require.NotErrorIs(t, ctx.Err(), context.DeadlineExceeded, "decode must not wait for the client deadline")

			// net/http cancels the request context after the handler returns.
			cancel()
			if tt.wantPrefillCanceled {
				select {
				case <-prefillCanceled:
				case <-time.After(time.Second):
					t.Fatal("client cancellation did not reach prefill")
				}
				return
			}
			select {
			case <-prefillCanceled:
				t.Fatal("prefill canceled after decode returned")
			case <-time.After(20 * time.Millisecond):
			}
		})
	}
}
