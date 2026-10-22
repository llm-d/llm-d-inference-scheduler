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
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math/big"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	reqcommon "github.com/llm-d/llm-d-router/pkg/common/request"
	"github.com/llm-d/llm-d-router/pkg/common/routing"
	"github.com/llm-d/llm-d-router/pkg/coordinator/connectors/kv"
	"github.com/llm-d/llm-d-router/pkg/coordinator/gateway"
	coordmetrics "github.com/llm-d/llm-d-router/pkg/coordinator/metrics"
	"github.com/llm-d/llm-d-router/pkg/coordinator/pipeline"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const SGLangPDStepName = "sglang-pd"

func init() { pipeline.Register(SGLangPDStepName, NewSGLangPDStep) }

// SGLangPDStep starts decode when the streaming prefill response headers arrive.
// The prefill EPP must run sglang-bootstrap-handler.
type SGLangPDStep struct {
	gwClient     *gateway.Client
	prefillGrace time.Duration
}

func NewSGLangPDStep(client *gateway.Client, params map[string]any) (pipeline.Step, error) {
	if client == nil {
		return nil, errors.New("sglang-pd: gateway client is required")
	}
	for key := range params {
		if key != "prefill_grace" {
			return nil, fmt.Errorf("sglang-pd: %s is not a valid parameter for this step", key)
		}
	}
	grace, present, err := paramDuration(params, "prefill_grace")
	if err != nil {
		return nil, err
	}
	if !present {
		grace = 30 * time.Second
	}
	if grace <= 0 {
		return nil, errors.New("sglang-pd: prefill_grace must be positive")
	}
	return &SGLangPDStep{gwClient: client, prefillGrace: grace}, nil
}

func (s *SGLangPDStep) Name() string { return SGLangPDStepName }

func (s *SGLangPDStep) Execute(ctx context.Context, rc *pipeline.RequestContext) error {
	if rc.OriginalPath != reqcommon.PathSGLangGenerate || !rc.Stream {
		return fmt.Errorf("sglang-pd requires /generate with stream=true: %w", pipeline.ErrBadRequest)
	}
	if sampling, present := rc.Body[reqcommon.FieldSamplingParams]; present {
		params, ok := sampling.(map[string]any)
		if !ok {
			return fmt.Errorf("sglang-pd requires one sampling_params object: %w", pipeline.ErrBadRequest)
		}
		n, present, err := paramInt(params, "n")
		if err != nil || (present && n != 1) {
			return fmt.Errorf("sglang-pd requires sampling_params.n=1: %w", pipeline.ErrBadRequest)
		}
	}
	// Keep the room exact through gateways that decode JSON numbers as float64.
	room, err := rand.Int(rand.Reader, big.NewInt(1<<53))
	if err != nil {
		return fmt.Errorf("sglang-pd: create bootstrap room: %w", err)
	}
	body := maps.Clone(rc.Body)
	delete(body, reqcommon.FieldBootstrapHost)
	body[reqcommon.FieldBootstrapRoom] = room.Int64()
	body[reqcommon.FieldBootstrapPort] = kv.ResolveSGLangBootstrapPort(ctx)
	body[reqcommon.FieldStream] = true
	prefillBody, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("sglang-pd: marshal prefill: %w", err)
	}
	logger := log.FromContext(ctx).WithName(SGLangPDStepName)
	decodeCtx, cancelDecode := context.WithCancelCause(ctx)
	defer cancelDecode(nil)
	prefillCtx, cancelPrefill := context.WithCancel(context.WithoutCancel(ctx))
	stopCancellation := context.AfterFunc(ctx, cancelPrefill)
	prefillDone := make(chan struct{})
	decodeSucceeded := false
	defer func() {
		stopCancellation()
		if !decodeSucceeded {
			cancelPrefill()
			return
		}
		// Successful decode may finish before prefill's final stream cleanup.
		go func() {
			defer cancelPrefill()
			timer := time.NewTimer(s.prefillGrace)
			defer timer.Stop()
			select {
			case <-prefillDone:
			case <-timer.C:
			}
		}()
	}()
	resp, err := postToGateway(prefillCtx, logger, s.gwClient, gatewayRequest{
		logMsg: "sending prefill", step: SGLangPDStepName, upstream: coordmetrics.UpstreamPrefill,
		path: rc.OriginalPath, body: prefillBody, headers: gatewayHeaders(rc, gateway.PhasePrefill),
	})
	if err != nil {
		return err
	}
	mediaType, _, err := mime.ParseMediaType(resp.Header.Get(reqcommon.HeaderContentType))
	endpoint := resp.Header.Get(routing.PrefillEndpointHeader)
	host, port, endpointErr := net.SplitHostPort(endpoint)
	portNumber, portErr := strconv.Atoi(port)
	if err != nil || mediaType != reqcommon.MediaTypeEventStream || endpointErr != nil || host == "" || portErr != nil || portNumber < 1 || portNumber > 65535 {
		_ = resp.Body.Close()
		return errors.New("sglang-pd: prefill requires an event stream and a valid selected endpoint response header")
	}
	rc.PeerTopology = resp.Header.Get(reqcommon.PeerTopologyHeaderKey)
	rc.CaptureResponseHeaders(resp.Header)
	body[reqcommon.FieldBootstrapHost] = host
	headers := map[string]string{routing.PrefillEndpointHeader: endpoint}
	proxyReq, err := newDecodeProxyRequest(decodeCtx, logger, SGLangPDStepName, rc, s.gwClient, body, headers)
	if err != nil {
		_ = resp.Body.Close()
		return err
	}
	go func() {
		defer close(prefillDone)
		defer resp.Body.Close()
		defer cancelPrefill()
		if err := drainSGLangPrefill(resp.Body); err != nil && prefillCtx.Err() == nil {
			logger.Error(err, "prefill stream failed")
			cancelDecode(fmt.Errorf("sglang-pd: prefill stream: %w", err))
		}
	}()
	out := serveDecode(logger, s.gwClient.Transport(), rc.ResponseWriter, proxyReq, coordmetrics.UpstreamDecode, nil)
	if err := context.Cause(decodeCtx); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// serveDecode suppresses aborts on cancellation. Prefill failure leaves
		// the client connected, so an already-started response must be aborted.
		if out.Status != 0 {
			panic(http.ErrAbortHandler)
		}
		return err
	}
	if err := out.streamedError(SGLangPDStepName); err != nil {
		return err
	}
	decodeSucceeded = true
	return pipeline.ErrPipelineDone
}

// SGLang emits each SSE event as a single data line, followed by [DONE].
func drainSGLangPrefill(body io.Reader) error {
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 4096), 4<<20)
	done := false
	for scanner.Scan() {
		data, ok := strings.CutPrefix(scanner.Text(), reqcommon.SSEDataPrefix)
		if !ok {
			continue
		}
		if strings.TrimSpace(data) == reqcommon.SSEDoneMarker {
			done = true
			continue
		}
		var event struct {
			Error    json.RawMessage `json:"error"`
			MetaInfo struct {
				FinishReason struct {
					Type string `json:"type"`
				} `json:"finish_reason"`
			} `json:"meta_info"`
		}
		if err := json.Unmarshal([]byte(data), &event); err != nil {
			return fmt.Errorf("invalid prefill event: %w", err)
		}
		if len(event.Error) > 0 && string(event.Error) != "null" {
			return errors.New("prefill returned an error event")
		}
		switch event.MetaInfo.FinishReason.Type {
		case "abort", "error", "cancelled":
			return errors.New("prefill generation failed")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if !done {
		return io.ErrUnexpectedEOF
	}
	return nil
}
