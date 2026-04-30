/*
Copyright 2026 The Kubernetes Authors.

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

package mocks

import (
	"context"
	"sync"

	"k8s.io/apimachinery/pkg/runtime/schema"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

var _ fwkdl.NotificationExtractor = (*NotificationExtractor)(nil)

// NotificationExtractor is a mock NotificationExtractor for testing.
// It records all events it receives and provides helper methods for test assertions.
type NotificationExtractor struct {
	name       string
	gvk        schema.GroupVersionKind
	events     []fwkdl.NotificationEvent
	mu         sync.Mutex
	extractErr error
}

// NewNotificationExtractor creates a new mock extractor with the given name.
func NewNotificationExtractor(name string) *NotificationExtractor {
	return &NotificationExtractor{
		name: name,
		gvk:  schema.GroupVersionKind{Group: "", Version: "v1", Kind: "Pod"},
	}
}

// WithGVK sets the GVK for the mock extractor.
func (m *NotificationExtractor) WithGVK(gvk schema.GroupVersionKind) *NotificationExtractor {
	m.gvk = gvk
	return m
}

// WithExtractError configures the extractor to return an error on Extract.
func (m *NotificationExtractor) WithExtractError(err error) *NotificationExtractor {
	m.extractErr = err
	return m
}

func (m *NotificationExtractor) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: "mock-extractor", Name: m.name}
}

func (m *NotificationExtractor) GVK() schema.GroupVersionKind {
	return m.gvk
}

// Extract records the event and returns any configured error.
func (m *NotificationExtractor) Extract(_ context.Context, event fwkdl.NotificationEvent, _ ...fwkdl.ExtractOption) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = append(m.events, event)
	return m.extractErr
}

// GetEvents returns a copy of all recorded events.
func (m *NotificationExtractor) GetEvents() []fwkdl.NotificationEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]fwkdl.NotificationEvent, len(m.events))
	copy(result, m.events)
	return result
}

// Reset clears all recorded events.
func (m *NotificationExtractor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.events = nil
}

// Extractor is a generic mock for the polling Extractor interface.
// It records call counts and optionally updates endpoint metrics.
type Extractor struct {
	name            string
	callCount       int
	mu              sync.Mutex
	extractErr      error
	updateMetrics   bool
	metricsToUpdate *fwkdl.Metrics
}

// NewExtractor creates a new mock polling extractor with the given name.
func NewExtractor(name string) *Extractor {
	return &Extractor{name: name}
}

// NewPollingExtractor is an alias kept for test-callsite stability.
func NewPollingExtractor(name string) *Extractor {
	return NewExtractor(name)
}

// WithExtractError configures the extractor to return an error.
func (m *Extractor) WithExtractError(err error) *Extractor {
	m.extractErr = err
	return m
}

// WithMetricsUpdate configures the extractor to update endpoint metrics.
func (m *Extractor) WithMetricsUpdate(metrics *fwkdl.Metrics) *Extractor {
	m.updateMetrics = true
	m.metricsToUpdate = metrics
	return m
}

func (m *Extractor) TypedName() fwkplugin.TypedName {
	return fwkplugin.TypedName{Type: "mock-extractor", Name: m.name}
}

func (m *Extractor) Extract(_ context.Context, _ any, opts ...fwkdl.ExtractOption) error {
	m.mu.Lock()
	m.callCount++
	m.mu.Unlock()

	if m.updateMetrics && m.metricsToUpdate != nil {
		o := fwkdl.ApplyExtractOptions(opts)
		if o.Endpoint != nil {
			o.Endpoint.UpdateMetrics(m.metricsToUpdate)
		}
	}

	return m.extractErr
}

// CallCount returns the number of times Extract was called.
func (m *Extractor) CallCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.callCount
}

// GetCallCount returns call count (thread-safe).
func (m *Extractor) GetCallCount() int {
	return m.CallCount()
}

// Reset clears the call count.
func (m *Extractor) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callCount = 0
}
