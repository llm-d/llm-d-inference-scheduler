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

package datalayer

import (
	"context"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// DataSource provides raw data to registered Extractors. Concrete variants
// are PollingDispatcher (poll-driven), NotificationSource (k8s-event-driven),
// and EndpointSource (lifecycle-event-driven).
type DataSource interface {
	plugin.Plugin
}

// Extractor transforms typed input T into endpoint attributes. T pins the
// dispatch payload:
//
//   - Polling extractors:      T = PollInput[D]      (paired with a PollingDispatcher)
//   - Notification extractors: T = NotificationEvent (also implement NotificationExtractor for GVK)
//   - Endpoint extractors:     T = EndpointEvent
type Extractor[T any] interface {
	plugin.Plugin
	Extract(ctx context.Context, in T) error
}

// PollInput pairs the typed poll payload with the endpoint being polled.
// Field name is Payload (not Data) so chained accesses like in.Payload.Data
// stay unambiguous when T itself happens to expose a Data field.
//
// Contract: when delivered via a conforming PollingDispatcher, Payload is
// non-nil for nilable T (pointer, map, slice, chan, func, interface). The
// dispatcher must short-circuit on a nil poll result before calling
// extractors. Extractor implementations may therefore dereference Payload
// without a nil guard.
type PollInput[D any] struct {
	Payload  D
	Endpoint Endpoint
}

// EndpointExtractor is the typed contract for endpoint-lifecycle extractors.
type EndpointExtractor = Extractor[EndpointEvent]

// NotificationExtractor is the typed contract for k8s-event extractors.
// GVK identifies the kind this extractor handles; it must match the paired
// NotificationSource's GVK.
type NotificationExtractor interface {
	Extractor[NotificationEvent]
	GVK() schema.GroupVersionKind
}

// PollingDispatcher is the framework's contract for polling sources. The
// source holds its own extractors and runs them with typed input each tick.
// Erasure of the source's T happens inside Dispatch/AppendExtractor; the
// framework treats every dispatcher uniformly.
//
// Implementation contract:
//   - Dispatch runs every bound extractor in AppendExtractor-insertion order.
//   - Each Poll and each Extract step must run under its own timeout so a
//     slow step cannot starve sibling steps of their tick budget.
//   - A non-nil return from Dispatch indicates a poll-level (fetch) failure.
//     Per-extractor failures must be recorded in DataLayerExtractErrorsTotal
//     and must NOT surface as a returned error. This keeps the runtime's
//     poll/extract error counters cleanly separated.
//   - Dispatch must short-circuit (skip the extractor loop entirely) when
//     the poll result is the typed-nil of a nilable T. This lets extractors
//     skip per-call nil guards on Payload.
//   - AppendExtractor is idempotent on extractor type: a second extractor
//     with the same TypedName().Type as one already bound is silently dropped.
type PollingDispatcher interface {
	plugin.Plugin
	// Dispatch fetches the source's data and runs every bound extractor.
	Dispatch(ctx context.Context, ep Endpoint) error
	// AppendExtractor binds ext into this dispatcher's extractor list.
	// Errors if ext does not match the dispatcher's PollInput[T]. Idempotent
	// on extractor type.
	AppendExtractor(ext plugin.Plugin) error
}

// EventType identifies the type of mutation that triggered a notification.
type EventType int

const (
	// EventAddOrUpdate is fired when a k8s object is created or updated.
	EventAddOrUpdate EventType = iota
	// EventDelete is fired when a k8s object is deleted.
	EventDelete
)

// NotificationEvent carries the event type and the affected object.
// Object is deep-copied by the framework core before delivery.
type NotificationEvent struct {
	Type   EventType
	Object *unstructured.Unstructured
}

// NotificationSource is an event-driven DataSource for a single k8s GVK.
type NotificationSource interface {
	DataSource
	// GVK returns the GroupVersionKind this source watches.
	GVK() schema.GroupVersionKind
	// Notify is called by the framework core when a mutation event fires.
	// Returns nil event to skip extractor dispatch.
	Notify(ctx context.Context, event NotificationEvent) (*NotificationEvent, error)
}

// EndpointEvent carries an endpoint lifecycle event.
type EndpointEvent struct {
	Type     EventType
	Endpoint Endpoint
}

// EndpointSource is an event-driven DataSource driven by endpoint lifecycle changes.
type EndpointSource interface {
	DataSource
	// NotifyEndpoint is called by the Runtime on each endpoint lifecycle event.
	// Returns nil event to skip extractor dispatch.
	NotifyEndpoint(ctx context.Context, event EndpointEvent) (*EndpointEvent, error)
}
