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

package http

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/url"
	"reflect"
	"sync"
	"time"

	"sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
)

// defaultStepTimeout bounds each Poll and each Extract independently so a slow
// extractor cannot starve sibling extractors of their tick budget.
const defaultStepTimeout = time.Second

// HTTPDataSource is a typed polling dispatcher. T is the data type the source
// produces; bound extractors must implement Extractor[PollInput[T]].
type HTTPDataSource[T any] struct {
	typedName fwkplugin.TypedName
	scheme    string
	path      string

	client Client
	parser func(io.Reader) (T, error)

	mu   sync.RWMutex
	exts []fwkdl.Extractor[fwkdl.PollInput[T]]
}

// NewHTTPDataSource constructs a typed polling dispatcher.
func NewHTTPDataSource[T any](scheme, path string, skipCertVerification bool,
	pluginType, pluginName string, parser func(io.Reader) (T, error)) (*HTTPDataSource[T], error) {
	if scheme != "http" && scheme != "https" {
		return nil, fmt.Errorf("unsupported scheme: %s", scheme)
	}
	if scheme == "https" {
		httpsTransport := baseTransport.Clone()
		httpsTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: skipCertVerification}
		defaultClient.Transport = httpsTransport
	}
	return &HTTPDataSource[T]{
		typedName: fwkplugin.TypedName{Type: pluginType, Name: pluginName},
		scheme:    scheme,
		path:      path,
		client:    defaultClient,
		parser:    parser,
	}, nil
}

func (s *HTTPDataSource[T]) TypedName() fwkplugin.TypedName { return s.typedName }

// Poll fetches and parses one tick. Exposed for tests; runtime uses Dispatch.
func (s *HTTPDataSource[T]) Poll(ctx context.Context, ep fwkdl.Endpoint) (T, error) {
	target := s.getEndpoint(ep.GetMetadata())
	raw, err := s.client.Get(ctx, target, ep.GetMetadata(), func(r io.Reader) (any, error) {
		return s.parser(r)
	})
	if err != nil {
		var zero T
		return zero, err
	}
	typed, ok := raw.(T)
	if !ok {
		var zero T
		return zero, fmt.Errorf("HTTPDataSource %s: parser returned %T, expected %T", s.typedName, raw, zero)
	}
	return typed, nil
}

// Dispatch polls the endpoint and fans the result out to every bound
// extractor. Each step (Poll and each Extract) runs under its own
// defaultStepTimeout so one slow extractor does not starve siblings.
//
// Return contract: a non-nil return indicates a poll-level failure (the
// dispatcher could not produce data). Per-extractor failures are recorded
// in DataLayerExtractErrorsTotal and do NOT surface as a returned error.
// This keeps the collector's poll/extract counters cleanly separated.
func (s *HTTPDataSource[T]) Dispatch(ctx context.Context, ep fwkdl.Endpoint) error {
	pollCtx, cancelPoll := context.WithTimeout(ctx, defaultStepTimeout)
	data, err := s.Poll(pollCtx, ep)
	cancelPoll()
	if err != nil {
		return err
	}
	// Behavior preservation: the legacy collector short-circuited the extractor
	// loop when Poll returned nil data. Preserve that for nilable T (pointer,
	// map, slice, chan, func, interface). Value T's (int, struct) skip the check.
	if isNilData(data) {
		return nil
	}
	in := fwkdl.PollInput[T]{Payload: data, Endpoint: ep}
	s.mu.RLock()
	exts := s.exts
	s.mu.RUnlock()
	logger := log.FromContext(ctx)
	srcType := s.typedName.Type
	for _, ext := range exts {
		if ctx.Err() != nil {
			return nil
		}
		extCtx, cancelExt := context.WithTimeout(ctx, defaultStepTimeout)
		err := ext.Extract(extCtx, in)
		cancelExt()
		if err != nil {
			metrics.DataLayerExtractErrorsTotal.WithLabelValues(srcType, ext.TypedName().Type).Inc()
			logger.V(logging.DEBUG).Info("extract failed", "source", s.typedName, "extractor", ext.TypedName(), "err", err)
		}
	}
	return nil
}

// AppendExtractor binds ext as a typed Extractor[PollInput[T]]. Idempotent
// on extractor type.
func (s *HTTPDataSource[T]) AppendExtractor(ext fwkplugin.Plugin) error {
	typed, ok := ext.(fwkdl.Extractor[fwkdl.PollInput[T]])
	if !ok {
		var zero T
		return fmt.Errorf("extractor %s: expected Extractor[PollInput[%T]], got %T",
			ext.TypedName(), zero, ext)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	extType := typed.TypedName().Type
	for _, existing := range s.exts {
		if existing.TypedName().Type == extType {
			return nil
		}
	}
	s.exts = append(s.exts, typed)
	return nil
}

func (s *HTTPDataSource[T]) getEndpoint(ep Addressable) *url.URL {
	return &url.URL{Scheme: s.scheme, Host: ep.GetMetricsHost(), Path: s.path}
}

// isNilData reports whether a typed value is the nil of a nilable kind. The
// legacy untyped `if data == nil` check covered the same observable cases.
func isNilData(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Ptr, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return rv.IsNil()
	}
	return false
}
