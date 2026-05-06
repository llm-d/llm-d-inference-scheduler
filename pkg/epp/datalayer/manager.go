package datalayer

import (
	"fmt"
	"sort"
	"sync"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// sourceManager owns a variant's typed source + extractor maps under one RWMutex.
type sourceManager[S fwkdl.DataSource, E fwkplugin.Plugin] struct {
	name       string
	mu         sync.RWMutex
	sources    map[string]S
	extractors map[string][]E
}

func newSourceManager[S fwkdl.DataSource, E fwkplugin.Plugin](name string) *sourceManager[S, E] {
	return &sourceManager[S, E]{
		name:       name,
		sources:    make(map[string]S),
		extractors: make(map[string][]E),
	}
}

// Register installs src and exts under src's name. Errors on duplicate name.
func (m *sourceManager[S, E]) Register(src S, exts []E) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	srcName := src.TypedName().Name
	if _, exists := m.sources[srcName]; exists {
		return fmt.Errorf("duplicate %s source name %q", m.name, srcName)
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
		sourceManager: newSourceManager[fwkdl.NotificationSource, fwkdl.NotificationExtractor]("notification"),
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
