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

// DataSource provides raw data to registered Extractors.
// For poll-based sources, use PollingDataSource.
// For event-driven sources, use NotificationSource or EndpointSource.
type DataSource interface {
	plugin.Plugin
}

// PollingDataSource is a poll-based DataSource that fetches data at regular intervals.
type PollingDataSource interface {
	DataSource
	// Poll fetches data for an endpoint and returns it.
	// The Runtime handles calling extractors with the returned data.
	Poll(ctx context.Context, ep Endpoint) (any, error)
}

// Extractor transforms typed input T into endpoint attributes.
// See PollingExtractor, EndpointExtractor, and NotificationExtractor for variants.
type Extractor[T any] interface {
	plugin.Plugin
	Extract(ctx context.Context, input T) error
}

// PollingInput pairs a poll result with the endpoint it belongs to.
// TODO: parametrize PollingDataSource.Poll over T and drop the `any` here so
// polling is typed end-to-end.
type PollingInput struct {
	Data     any
	Endpoint Endpoint
}

// NewPollingInput pairs poll data with the endpoint it belongs to.
func NewPollingInput(data any, ep Endpoint) PollingInput {
	return PollingInput{Data: data, Endpoint: ep}
}

// PollingExtractor is an Extractor paired with a PollingDataSource.
type PollingExtractor = Extractor[PollingInput]

// EndpointExtractor processes endpoint lifecycle events.
type EndpointExtractor = Extractor[EndpointEvent]

// NotificationExtractor processes k8s object events pushed from a NotificationSource.
type NotificationExtractor interface {
	Extractor[NotificationEvent]
	// GVK returns the GroupVersionKind this extractor handles.
	GVK() schema.GroupVersionKind
}

// Validator is an optional interface that a DataSource can implement to
// perform additional validation on extractors of variant T. The runtime
// asserts to Validator[E] only when E is the source's variant, so
// implementers receive a typed extractor without further type assertions.
type Validator[T plugin.Plugin] interface {
	Validate(T) error
}

// EventType identifies the type of mutation that triggered the notification.
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
	// Type is the mutation type.
	Type EventType
	// Object is the current state of the object (for add/update) or the
	// last known state (for delete). Note that for delete notifications
	// only the object's name and namespace can be relied on.
	Object *unstructured.Unstructured
}

// NotificationSource is an event-driven DataSource for a single k8s GVK.
// The framework core owns the k8s notification mechanisms (e.g., watches,
// caches, informers) and calls the source's Notify on events.
type NotificationSource interface {
	DataSource
	// GVK returns the GroupVersionKind this source watches.
	GVK() schema.GroupVersionKind
	// Notify is called by the framework core when a mutation event fires.
	// The event object is already deep-copied.
	// Returns the event (possibly modified) for Runtime to dispatch to extractors.
	// Returns nil event to signal Runtime to skip extractor dispatch.
	// TODO: accept event by value but return *event — should the input be a pointer too?
	Notify(ctx context.Context, event NotificationEvent) (*NotificationEvent, error)
}

// EndpointEvent carries an endpoint lifecycle event.
// Reuses EventType: EventAddOrUpdate signals an endpoint was added to the
// datastore; EventDelete signals an endpoint was removed.
type EndpointEvent struct {
	Type     EventType
	Endpoint Endpoint
}

// EndpointSource is an event-driven DataSource driven by endpoint lifecycle
// changes. The Runtime calls NotifyEndpoint when an endpoint is added to or
// removed from the datastore, then dispatches the (possibly modified) event to
// registered EndpointExtractors. Return nil to suppress extractor dispatch.
type EndpointSource interface {
	DataSource
	// NotifyEndpoint is called by the Runtime on each endpoint lifecycle event.
	// Returns the event (possibly modified) for the Runtime to dispatch to extractors.
	// Returns nil event to signal Runtime to skip extractor dispatch.
	NotifyEndpoint(ctx context.Context, event EndpointEvent) (*EndpointEvent, error)
}
