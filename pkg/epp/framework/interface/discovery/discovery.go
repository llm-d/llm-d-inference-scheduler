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

// Package discovery defines the DiscoveryPlugin abstraction for populating
// the datastore with inference server endpoints independently of Kubernetes.
package discovery

import (
	"context"

	"k8s.io/apimachinery/pkg/types"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
)

// DiscoveryPlugin discovers inference endpoints and drives their lifecycle in the datastore.
// Implementations must also implement fwkplugin.Plugin so they can be registered in the
// plugin registry and selected via EndpointPickerConfig.discovery.pluginRef.
type DiscoveryPlugin interface {
	// Start begins discovery. It should:
	//   1. Enumerate all known endpoints, calling notifier.Upsert for each.
	//   2. Continue watching for changes until ctx is cancelled.
	// Blocks until ctx is cancelled or a fatal error occurs.
	Start(ctx context.Context, notifier Notifier) error
}

// Notifier is the callback through which DiscoveryPlugin communicates endpoint state
// to the datastore.
//
// Ordering contract: the datastore processes Upsert and Delete calls in the order
// they are received. Plugin implementations MUST preserve event order — do not
// buffer, coalesce, or dispatch calls concurrently in a way that could reorder
// them. For example, an Upsert followed by a Delete for the same endpoint must
// arrive in that order, or the endpoint will be incorrectly left in the datastore.
type Notifier interface {
	// Upsert adds or updates one or more endpoints in the datastore.
	Upsert(endpoints []*fwkdl.EndpointMetadata)
	// Delete removes an endpoint from the datastore by its namespaced name.
	Delete(id types.NamespacedName)
}
