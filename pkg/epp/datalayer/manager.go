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

package datalayer

import (
	"fmt"
	"sort"
	"sync"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// sourceVariant identifies one of the three DataSource integration kinds the
// datalayer dispatches over. Each variant has its own driver lifecycle,
// extractor signature, and manager instance:
//
//   - variantPolling: ticker-driven, per-endpoint scrapes (e.g. /metrics, /v1/models).
//   - variantNotification: K8s informer-driven watches, with GVK uniqueness
//     enforced across notification sources.
//   - variantEndpoint: per-endpoint lifecycle events (add / update / delete)
//     dispatched without polling.
//
// A new integration kind means a new constant here, a new manager field on
// Runtime, and a new entry in Runtime.buildVariantLookups.
type sourceVariant string

const (
	variantPolling      sourceVariant = "polling"
	variantNotification sourceVariant = "notification"
	variantEndpoint     sourceVariant = "endpoint"
)

// sourceManager owns a variant's typed source + extractor maps under one RWMutex.
type sourceManager[S fwkdl.DataSource, E fwkplugin.Plugin] struct {
	variant    sourceVariant
	mu         sync.RWMutex
	sources    map[string]S
	extractors map[string][]E
}

func newSourceManager[S fwkdl.DataSource, E fwkplugin.Plugin](variant sourceVariant) *sourceManager[S, E] {
	return &sourceManager[S, E]{
		variant:    variant,
		sources:    make(map[string]S),
		extractors: make(map[string][]E),
	}
}

type (
	pollingManager  = sourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor]
	endpointManager = sourceManager[fwkdl.EndpointSource, fwkdl.EndpointExtractor]
)

func newPollingManager() *pollingManager {
	return newSourceManager[fwkdl.PollingDataSource, fwkdl.PollingExtractor](variantPolling)
}

func newEndpointManager() *endpointManager {
	return newSourceManager[fwkdl.EndpointSource, fwkdl.EndpointExtractor](variantEndpoint)
}

// Register installs src and exts under src's name. Errors on duplicate name.
func (m *sourceManager[S, E]) Register(src S, exts []E) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	srcName := src.TypedName().Name
	if _, exists := m.sources[srcName]; exists {
		return fmt.Errorf("duplicate %s source name %q", m.variant, srcName)
	}
	m.sources[srcName] = src
	if len(exts) > 0 {
		m.extractors[srcName] = exts
	}
	return nil
}

// AppendExtractor appends ext to srcName's extractor list, deduping by type.
func (m *sourceManager[S, E]) AppendExtractor(srcName string, ext E) {
	m.mu.Lock()
	defer m.mu.Unlock()
	extType := ext.TypedName().Type
	for _, existing := range m.extractors[srcName] {
		if existing.TypedName().Type == extType {
			return
		}
	}
	m.extractors[srcName] = append(m.extractors[srcName], ext)
}

// Sources returns a snapshot of the source map.
func (m *sourceManager[S, E]) Sources() map[string]S {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]S, len(m.sources))
	for k, v := range m.sources {
		out[k] = v
	}
	return out
}

// Extractors returns a snapshot of the extractor map; slice values are fresh copies.
func (m *sourceManager[S, E]) Extractors() map[string][]E {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][]E, len(m.extractors))
	for k, v := range m.extractors {
		dup := make([]E, len(v))
		copy(dup, v)
		out[k] = dup
	}
	return out
}

// ExtractorsFor returns a copy of the extractor slice for a specific source.
func (m *sourceManager[S, E]) ExtractorsFor(srcName string) []E {
	m.mu.RLock()
	defer m.mu.RUnlock()
	src := m.extractors[srcName]
	if src == nil {
		return nil
	}
	dup := make([]E, len(src))
	copy(dup, src)
	return dup
}

// IsEmpty reports whether any source is registered.
func (m *sourceManager[S, E]) IsEmpty() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sources) == 0
}

// FindByType returns the first source whose TypedName.Type matches; iteration
// is sorted by name so first-match is stable across runs. match (if non-nil)
// further filters candidates.
func (m *sourceManager[S, E]) FindByType(sourceType string, match func(S) bool) (string, S, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.sources))
	for n := range m.sources {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		src := m.sources[name]
		if src.TypedName().Type != sourceType {
			continue
		}
		if match != nil && !match(src) {
			continue
		}
		return name, src, true
	}
	var zero S
	return "", zero, false
}

// Count returns the number of registered sources.
func (m *sourceManager[S, E]) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sources)
}

// notificationManager adds GVK-uniqueness tracking on top of sourceManager.
// gvkToName is guarded by the embedded sourceManager's mu.
type notificationManager struct {
	*sourceManager[fwkdl.NotificationSource, fwkdl.NotificationExtractor]
	gvkToName map[string]string
}

func newNotificationManager() *notificationManager {
	return &notificationManager{
		sourceManager: newSourceManager[fwkdl.NotificationSource, fwkdl.NotificationExtractor](variantNotification),
		gvkToName:     make(map[string]string),
	}
}

// Register installs src after enforcing both name and GVK uniqueness.
func (m *notificationManager) Register(src fwkdl.NotificationSource, exts []fwkdl.NotificationExtractor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	srcName := src.TypedName().Name
	if _, exists := m.sources[srcName]; exists {
		return fmt.Errorf("duplicate notification source name %q", srcName)
	}
	gvk := src.GVK().String()
	if existing, exists := m.gvkToName[gvk]; exists {
		return fmt.Errorf("duplicate notification source GVK %s: already used by source %s, cannot add %s",
			gvk, existing, src.TypedName().String())
	}
	m.sources[srcName] = src
	if len(exts) > 0 {
		m.extractors[srcName] = exts
	}
	m.gvkToName[gvk] = srcName
	return nil
}
