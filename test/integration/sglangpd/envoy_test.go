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

package sglangpd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	corepb "github.com/envoyproxy/go-control-plane/envoy/config/core/v3"
	extprocpb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	commonenvoy "github.com/llm-d/llm-d-router/pkg/common/envoy"
	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/common/routing"
	"github.com/llm-d/llm-d-router/pkg/coordinator/gateway"
	"github.com/llm-d/llm-d-router/pkg/coordinator/pipeline"
	"github.com/llm-d/llm-d-router/pkg/coordinator/steps"
	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkrc "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/requestcontrol"
	fwksched "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/scheduling"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/requestcontrol/prerequest/sglangbootstrap"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/requesthandling/parsers/sglanghttp"
	"github.com/llm-d/llm-d-router/pkg/epp/metadata"
)

// The ext_proc harness runs the production parser and bootstrap plugin. Worker
// headers are flushed before its body, matching SGLang's native /generate.
func TestStreamingPrefillThroughEnvoy(t *testing.T) {
	binary := os.Getenv("ENVOY_BINARY")
	if binary == "" {
		t.Skip("set ENVOY_BINARY to an unmodified Envoy executable")
	}
	for _, mode := range []string{"STREAMED", "FULL_DUPLEX_STREAMED"} {
		for _, h2 := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/h2=%v", mode, h2), func(t *testing.T) {
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				defer cancel()
				decodeStarted := make(chan struct{})
				prefillFinished := make(chan struct{})
				var prefill map[string]any
				worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
						return
					}
					if body[reqcommon.FieldBootstrapHost] != "127.0.0.1" {
						t.Errorf("bootstrap host not set by EPP: %v", body)
					}
					w.Header().Set("Content-Type", reqcommon.MediaTypeEventStream)
					if r.Header.Get(reqcommon.EPPProfileHeaderKey) == gateway.PhasePrefill {
						prefill = body
						w.Header().Set(routing.PrefillEndpointHeader, "forged:1")
						w.(http.Flusher).Flush()
						select {
						case <-decodeStarted:
						case <-ctx.Done():
							return
						}
						_, _ = io.WriteString(w, "data: [DONE]\n\n")
						close(prefillFinished)
						return
					}
					if body[reqcommon.FieldBootstrapRoom] != prefill[reqcommon.FieldBootstrapRoom] {
						t.Error("room changed through EPP body mutation")
					}
					close(decodeStarted)
					_, _ = io.WriteString(w, "data: decode\n\n")
				}))
				defer worker.Close()
				listener, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				grpcServer := grpc.NewServer()
				extprocpb.RegisterExternalProcessorServer(grpcServer, &picker{t: t, endpoint: worker.Listener.Addr().String(), duplex: mode == "FULL_DUPLEX_STREAMED"})
				go func() { _ = grpcServer.Serve(listener) }()
				defer grpcServer.Stop()
				reserved, err := net.Listen("tcp", "127.0.0.1:0")
				require.NoError(t, err)
				gatewayPort := reserved.Addr().(*net.TCPAddr).Port
				require.NoError(t, reserved.Close())
				path := filepath.Join(t.TempDir(), "envoy.yaml")
				require.NoError(t, os.WriteFile(path, []byte(fmt.Sprintf(envoyConfig, gatewayPort, mode, listener.Addr().(*net.TCPAddr).Port)), 0600))
				var logs bytes.Buffer
				cmd := exec.CommandContext(ctx, binary, "-c", path, "--concurrency", "1", "--disable-hot-restart", "--log-level", "warning")
				cmd.Stdout, cmd.Stderr = &logs, &logs
				require.NoError(t, cmd.Start())
				defer func() {
					_ = cmd.Process.Kill()
					_ = cmd.Wait()
					if t.Failed() {
						t.Log(logs.String())
					}
				}()
				address := net.JoinHostPort("127.0.0.1", strconv.Itoa(gatewayPort))
				for {
					conn, err := net.DialTimeout("tcp", address, 100*time.Millisecond)
					if err == nil {
						conn.Close()
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal("Envoy did not start")
					case <-time.After(20 * time.Millisecond):
					}
				}
				protocols := new(http.Protocols)
				protocols.SetHTTP1(!h2)
				protocols.SetUnencryptedHTTP2(h2)
				transport := &http.Transport{Protocols: protocols}
				defer transport.CloseIdleConnections()
				step, err := steps.NewSGLangPDStep(gateway.NewWithTransport(transport, "http://"+address), nil)
				require.NoError(t, err)
				rec := httptest.NewRecorder()
				err = step.Execute(ctx, &pipeline.RequestContext{OriginalPath: reqcommon.PathSGLangGenerate, Stream: true,
					Body: map[string]any{"text": "hello", "stream": true, "sampling_params": map[string]any{"max_new_tokens": 1}}, ResponseWriter: rec})
				require.ErrorIs(t, err, pipeline.ErrPipelineDone)
				require.NoError(t, ctx.Err(), "decode waited for prefill body")
				require.Equal(t, "data: decode\n\n", rec.Body.String())
				select {
				case <-prefillFinished:
				case <-ctx.Done():
					t.Fatal("prefill did not finish")
				}
			})
		}
	}
}

type picker struct {
	extprocpb.UnimplementedExternalProcessorServer
	endpoint string
	duplex   bool
	t        *testing.T
}

func (p *picker) Process(stream extprocpb.ExternalProcessor_ProcessServer) (resultErr error) {
	defer func() {
		if resultErr != nil && status.Code(resultErr) != codes.Canceled {
			p.t.Logf("ext_proc: %v", resultErr)
		}
	}()
	plugin, err := sglangbootstrap.Factory("", nil, nil)
	if err != nil {
		return err
	}
	handler := plugin.(*sglangbootstrap.Handler)
	host, port, _ := net.SplitHostPort(p.endpoint)
	endpoint := fwksched.NewEndpoint(&fwkdl.EndpointMetadata{Address: host, Port: port}, &fwkdl.Metrics{}, nil)
	req := &fwksched.InferenceRequest{Headers: map[string]string{}}
	for {
		message, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		response := &extprocpb.ProcessingResponse{}
		switch {
		case message.GetRequestHeaders() != nil:
			for _, header := range message.GetRequestHeaders().Headers.Headers {
				req.Headers[header.Key] = commonenvoy.GetHeaderValue(header)
			}
			phase := req.Headers[reqcommon.EPPProfileHeaderKey]
			req.SchedulingResult = &fwksched.SchedulingResult{PrimaryProfileName: phase, ProfileResults: map[string]*fwksched.ProfileRunResult{phase: {TargetEndpoints: []fwksched.Endpoint{endpoint}}}}
			response.Response = &extprocpb.ProcessingResponse_RequestHeaders{RequestHeaders: &extprocpb.HeadersResponse{}}
		case message.GetRequestBody() != nil:
			parsed, err := sglanghttp.NewSGLangHTTPParser().ParseRequest(stream.Context(), message.GetRequestBody().Body, req.Headers)
			if err != nil {
				return err
			}
			req.Body = parsed.Body
			if err := handler.PreRequest(stream.Context(), req, req.SchedulingResult); err != nil {
				return err
			}
			payload, _ := req.Body.Payload.AsMap()
			body, err := payload.Marshal()
			if err != nil {
				return err
			}
			response.Response = &extprocpb.ProcessingResponse_RequestBody{RequestBody: &extprocpb.BodyResponse{Response: &extprocpb.CommonResponse{
				ClearRouteCache: true,
				HeaderMutation:  &extprocpb.HeaderMutation{RemoveHeaders: []string{"content-length"}, SetHeaders: []*corepb.HeaderValueOption{{Header: &corepb.HeaderValue{Key: metadata.DestinationEndpointKey, RawValue: []byte(p.endpoint)}}}},
				BodyMutation:    &extprocpb.BodyMutation{Mutation: &extprocpb.BodyMutation_Body{Body: body}},
			}}}
		case message.GetResponseHeaders() != nil:
			headers := &fwkrc.Response{Headers: map[string]string{}}
			handler.ResponseHeader(stream.Context(), req, headers, endpoint.GetMetadata())
			mutation := &extprocpb.HeaderMutation{}
			for key, value := range headers.Headers {
				mutation.SetHeaders = append(mutation.SetHeaders, &corepb.HeaderValueOption{Header: &corepb.HeaderValue{Key: key, RawValue: []byte(value)}})
			}
			response.Response = &extprocpb.ProcessingResponse_ResponseHeaders{ResponseHeaders: &extprocpb.HeadersResponse{Response: &extprocpb.CommonResponse{HeaderMutation: mutation}}}
		case message.GetResponseBody() != nil:
			body := message.GetResponseBody()
			common := &extprocpb.CommonResponse{}
			if p.duplex {
				common = commonenvoy.BuildChunkedBodyResponses(body.Body, body.EndOfStream)[0]
			}
			response.Response = &extprocpb.ProcessingResponse_ResponseBody{ResponseBody: &extprocpb.BodyResponse{Response: common}}
		default:
			return errors.New("unexpected ext_proc message")
		}
		if err := stream.Send(response); err != nil {
			return err
		}
	}
}

const envoyConfig = `static_resources:
  listeners:
  - name: gateway
    address:
      socket_address: {address: 127.0.0.1, port_value: %d}
    filter_chains:
    - filters:
      - name: envoy.filters.network.http_connection_manager
        typed_config:
          "@type": type.googleapis.com/envoy.extensions.filters.network.http_connection_manager.v3.HttpConnectionManager
          stat_prefix: gateway
          http2_protocol_options: {}
          route_config:
            name: phases
            virtual_hosts:
            - name: phases
              domains: ["*"]
              routes:
              - match: {prefix: /}
                route: {cluster: workers, timeout: 10s}
          http_filters:
          - name: envoy.filters.http.ext_proc
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.ext_proc.v3.ExternalProcessor
              grpc_service:
                envoy_grpc: {cluster_name: epp}
              failure_mode_allow: false
              metadata_options:
                receiving_namespaces:
                  untyped: [envoy.lb]
              processing_mode:
                request_header_mode: SEND
                request_body_mode: BUFFERED
                response_header_mode: SEND
                response_body_mode: %s
                response_trailer_mode: SEND
          - name: envoy.filters.http.router
            typed_config:
              "@type": type.googleapis.com/envoy.extensions.filters.http.router.v3.Router
  clusters:
  - name: workers
    type: ORIGINAL_DST
    connect_timeout: 1s
    lb_policy: CLUSTER_PROVIDED
    original_dst_lb_config:
      use_http_header: true
      http_header_name: x-gateway-destination-endpoint
  - name: epp
    type: STATIC
    connect_timeout: 1s
    http2_protocol_options: {}
    load_assignment:
      cluster_name: epp
      endpoints:
      - lb_endpoints:
        - endpoint:
            address:
              socket_address: {address: 127.0.0.1, port_value: %d}
`
