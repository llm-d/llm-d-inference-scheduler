package datalayer

import (
	"fmt"
	"sync"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// sourceManager owns a variant's typed source + extractor maps under a single
// RWMutex. Adding a new variant means instantiating a new sourceManager (or
// embedding one in a variant-specific wrapper for extra fields like
// notificationManager's gvkToName).
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

// Register installs src and (if non-empty) exts under src's name.
func (m *sourceManager[S, E]) Register(src S, exts []E) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sources[src.TypedName().Name] = src
	if len(exts) > 0 {
		m.extractors[src.TypedName().Name] = exts
	}
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

// Sources returns a snapshot copy of the source map.
func (m *sourceManager[S, E]) Sources() map[string]S {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string]S, len(m.sources))
	for k, v := range m.sources {
		out[k] = v
	}
	return out
}

// Extractors returns a snapshot copy of the extractor map.
func (m *sourceManager[S, E]) Extractors() map[string][]E {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make(map[string][]E, len(m.extractors))
	for k, v := range m.extractors {
		out[k] = v
	}
	return out
}

// ExtractorsFor returns the extractor slice for a specific source.
func (m *sourceManager[S, E]) ExtractorsFor(srcName string) []E {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.extractors[srcName]
}

// IsEmpty reports whether any source is registered.
func (m *sourceManager[S, E]) IsEmpty() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sources) == 0
}

// FindByType returns the first source whose TypedName.Type matches sourceType.
// match (if non-nil) is an additional filter applied to candidates.
func (m *sourceManager[S, E]) FindByType(sourceType string, match func(S) bool) (string, S, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	for name, src := range m.sources {
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

// notificationManager extends sourceManager with GVK uniqueness tracking.
type notificationManager struct {
	*sourceManager[fwkdl.NotificationSource, fwkdl.NotificationExtractor]
	// gvkToName is protected by the embedded sourceManager's mu (held during Register/Append).
	gvkToName map[string]string
}

func newNotificationManager() *notificationManager {
	return &notificationManager{
		sourceManager: newSourceManager[fwkdl.NotificationSource, fwkdl.NotificationExtractor]("notification"),
		gvkToName:     make(map[string]string),
	}
}

// Register installs src after enforcing GVK uniqueness within this manager.
func (m *notificationManager) Register(src fwkdl.NotificationSource, exts []fwkdl.NotificationExtractor) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	gvk := src.GVK().String()
	if existing, exists := m.gvkToName[gvk]; exists {
		return fmt.Errorf("duplicate notification source GVK %s: already used by source %s, cannot add %s",
			gvk, existing, src.TypedName().String())
	}
	srcName := src.TypedName().Name
	m.sources[srcName] = src
	if len(exts) > 0 {
		m.extractors[srcName] = exts
	}
	m.gvkToName[gvk] = srcName
	return nil
}
