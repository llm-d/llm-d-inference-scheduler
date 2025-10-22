/*
Copyright 2025 The Kubernetes Authors.

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

// Package handlers implements the gRPC server for Envoy's external processing API,
package handlers

import (
	"context"
	"io"

	extProcPb "github.com/envoyproxy/go-control-plane/envoy/service/ext_proc/v3"
	envoyTypePb "github.com/envoyproxy/go-control-plane/envoy/type/v3"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"sigs.k8s.io/controller-runtime/pkg/log"

	v1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	errutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/error"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

var continueHeadersResponse = &extProcPb.ProcessingResponse{
	Response: &extProcPb.ProcessingResponse_RequestHeaders{
		RequestHeaders: &extProcPb.HeadersResponse{
			Response: &extProcPb.CommonResponse{
				Status: extProcPb.CommonResponse_CONTINUE,
			},
		},
	},
}

// NewStreamingServer creates a new StreamingServer with the provided Datastore and Activator.
func NewStreamingServer(datastore Datastore, activator Activator) *StreamingServer {
	return &StreamingServer{
		activator: activator,
		datastore: datastore,
	}
}

// Activator defines the interface for activating replicas based on incoming requests.
type Activator interface {
	MayActivate(ctx context.Context) error
}

// Datastore defines the interface for accessing InferencePool data.
type Datastore interface {
	PoolGet() (*v1.InferencePool, error)
}

// StreamingServer implements the Envoy external processing server.
// https://www.envoyproxy.io/docs/envoy/latest/api-v3/service/ext_proc/v3/external_processor.proto
type StreamingServer struct {
	datastore Datastore
	activator Activator
}

// Process handles the bidirectional streaming RPC for external processing.
func (s *StreamingServer) Process(srv extProcPb.ExternalProcessor_ProcessServer) error {
	ctx := srv.Context()
	logger := log.FromContext(ctx)
	loggerTrace := logger.V(logutil.TRACE)
	loggerTrace.Info("Processing")

	var err error
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		req, recvErr := srv.Recv()
		if recvErr == io.EOF || status.Code(recvErr) == codes.Canceled {
			return nil
		}
		if recvErr != nil {
			return status.Errorf(codes.Unknown, "cannot receive stream request: %v", err)
		}

		switch req.Request.(type) {
		case *extProcPb.ProcessingRequest_RequestHeaders:

			if err := s.activator.MayActivate(ctx); err != nil {
				if logger.V(logutil.DEBUG).Enabled() {
					logger.V(logutil.DEBUG).Error(err, "Failed to process request", "request", req)
				} else {
					logger.V(logutil.DEFAULT).Error(err, "Failed to process request")
				}
				resp, err := buildErrResponse(err)
				if err != nil {
					return err
				}
				if err := srv.Send(resp); err != nil {
					logger.V(logutil.DEFAULT).Error(err, "Send failed")
					return status.Errorf(codes.Unknown, "failed to send response back to Envoy: %v", err)
				}
				return nil
			}

			loggerTrace.Info("Sending request header response")
			if err := srv.Send(continueHeadersResponse); err != nil {
				logger.V(logutil.DEFAULT).Error(err, "error sending response")
				return status.Errorf(codes.Unknown, "failed to send response back to Envoy: %v", err)
			}

		case *extProcPb.ProcessingRequest_RequestBody:
			logger.V(logutil.DEBUG).Info("Error: ProcessingRequest_RequestBody received")
		case *extProcPb.ProcessingRequest_RequestTrailers:
			logger.V(logutil.DEBUG).Info("Error: ProcessingRequest_RequestTrailers received")
		case *extProcPb.ProcessingRequest_ResponseHeaders:
			logger.V(logutil.DEBUG).Info("Error: ProcessingRequest_ResponseHeaders received")
		case *extProcPb.ProcessingRequest_ResponseBody:
			logger.V(logutil.DEBUG).Info("Error: ProcessingRequest_ResponseBody received")
		case *extProcPb.ProcessingRequest_ResponseTrailers:
			logger.V(logutil.DEBUG).Info("Error: ProcessingRequest_ResponseTrailers received")
		}

	}
}

func buildErrResponse(err error) (*extProcPb.ProcessingResponse, error) {
	var resp *extProcPb.ProcessingResponse

	switch errutil.CanonicalCode(err) {
	// This code can be returned by scheduler when there is no capacity for sheddable
	// requests.
	case errutil.InferencePoolResourceExhausted:
		resp = &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcPb.ImmediateResponse{
					Status: &envoyTypePb.HttpStatus{
						Code: envoyTypePb.StatusCode_TooManyRequests,
					},
				},
			},
		}
	// This code can be returned by when EPP processes the request and run into server-side errors.
	case errutil.Internal:
		resp = &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcPb.ImmediateResponse{
					Status: &envoyTypePb.HttpStatus{
						Code: envoyTypePb.StatusCode_InternalServerError,
					},
				},
			},
		}
	// This code can be returned by the director when there are no candidate pods for the request scheduling.
	case errutil.ServiceUnavailable:
		resp = &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcPb.ImmediateResponse{
					Status: &envoyTypePb.HttpStatus{
						Code: envoyTypePb.StatusCode_ServiceUnavailable,
					},
				},
			},
		}
	// This code can be returned when users provide invalid json request.
	case errutil.BadRequest:
		resp = &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcPb.ImmediateResponse{
					Status: &envoyTypePb.HttpStatus{
						Code: envoyTypePb.StatusCode_BadRequest,
					},
				},
			},
		}
	case errutil.BadConfiguration:
		resp = &extProcPb.ProcessingResponse{
			Response: &extProcPb.ProcessingResponse_ImmediateResponse{
				ImmediateResponse: &extProcPb.ImmediateResponse{
					Status: &envoyTypePb.HttpStatus{
						Code: envoyTypePb.StatusCode_NotFound,
					},
				},
			},
		}
	default:
		return nil, status.Errorf(status.Code(err), "failed to handle request: %v", err)
	}

	if err.Error() != "" {
		resp.Response.(*extProcPb.ProcessingResponse_ImmediateResponse).ImmediateResponse.Body = []byte(err.Error())
	}

	return resp, nil
}
