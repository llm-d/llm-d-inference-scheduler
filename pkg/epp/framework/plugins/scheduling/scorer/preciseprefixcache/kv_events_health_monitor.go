package preciseprefixcache

import (
	"sync"
	"time"
)

// KVEventsHealthMonitor tracks per-endpoint KV events pipeline health.
// It records when confirmed (non-speculative) entries were last observed
// in index lookups and when requests were last routed, allowing the scorer
// to distinguish between a broken pipeline (routing but no confirmed events)
// and normal idle (no routing, no events).
//
// This component is data-collection only — it does not modify TTL behavior.
// Dynamic TTL adjustment will be added in a subsequent PR.
type KVEventsHealthMonitor struct {
	mu       sync.RWMutex
	state    map[string]*endpointHealth // key: endpoint identifier (e.g. "ip:port")
	hasKVCfg bool                       // whether kvEventsConfig is present
}

// endpointHealth holds per-endpoint health data.
type endpointHealth struct {
	// lastConfirmedTime is the last time a confirmed (non-speculative) entry
	// was observed in an index lookup for this endpoint. This serves as a
	// proxy for "KV events are arriving" without requiring changes to the
	// kv-cache library.
	lastConfirmedTime time.Time

	// lastRoutedTime is the last time we routed a request to this endpoint
	// via PreRequest.
	lastRoutedTime time.Time
}

// NewKVEventsHealthMonitor creates a new health monitor.
// hasKVEventsConfig indicates whether KV events are configured at all.
func NewKVEventsHealthMonitor(hasKVEventsConfig bool) *KVEventsHealthMonitor {
	return &KVEventsHealthMonitor{
		state:    make(map[string]*endpointHealth),
		hasKVCfg: hasKVEventsConfig,
	}
}

// RecordConfirmedEntry is called when a confirmed (non-speculative) entry
// is observed in an index lookup for an endpoint. This indicates that
// KV events are flowing for this endpoint.
func (m *KVEventsHealthMonitor) RecordConfirmedEntry(endpointKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h := m.getOrCreate(endpointKey)
	h.lastConfirmedTime = time.Now()
}

// RecordRouting is called when a request is routed to an endpoint (PreRequest).
func (m *KVEventsHealthMonitor) RecordRouting(endpointKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	h := m.getOrCreate(endpointKey)
	h.lastRoutedTime = time.Now()
}

// GetHealthStatus returns the health status for an endpoint.
// Returns lastConfirmedTime, lastRoutedTime, and whether the endpoint is known.
func (m *KVEventsHealthMonitor) GetHealthStatus(endpointKey string) (lastConfirmed, lastRouted time.Time, known bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	h, ok := m.state[endpointKey]
	if !ok {
		return time.Time{}, time.Time{}, false
	}
	return h.lastConfirmedTime, h.lastRoutedTime, true
}

// HasKVEventsConfig returns whether KV events are configured.
func (m *KVEventsHealthMonitor) HasKVEventsConfig() bool {
	return m.hasKVCfg
}

// RemoveEndpoint cleans up health state for a removed endpoint.
func (m *KVEventsHealthMonitor) RemoveEndpoint(endpointKey string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.state, endpointKey)
}

// getOrCreate returns the health state for an endpoint, creating it if needed.
// Must be called with mu held.
func (m *KVEventsHealthMonitor) getOrCreate(endpointKey string) *endpointHealth {
	h, ok := m.state[endpointKey]
	if !ok {
		h = &endpointHealth{}
		m.state[endpointKey] = h
	}
	return h
}
