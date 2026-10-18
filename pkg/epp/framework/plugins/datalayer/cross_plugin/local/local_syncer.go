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

package local

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"time"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/plugin"
)

const LocalSyncerType = "local-syncer"

var _ fwkdl.CrossReplicaSyncer = (*LocalSyncer)(nil)

// LocalSyncer is an in-memory CrossReplicaSyncer for single-replica
// deployments and testing. No cross-replica synchronization is performed.
type LocalSyncer struct {
	typedName fwkplugin.TypedName
	replicaID string
	// now is swappable so tests can advance time without sleeping.
	now func() time.Time

	mu   sync.Mutex
	data map[string]localEntry
}

// localEntry is one coordination decision. A zero expiresAt means the entry
// never expires, the behavior of decisions stored without DecisionOptions.
type localEntry struct {
	value     any
	expiresAt time.Time
}

func NewLocalSyncer(name, replicaID string) *LocalSyncer {
	return &LocalSyncer{
		typedName: fwkplugin.TypedName{Type: LocalSyncerType, Name: name},
		replicaID: replicaID,
		now:       time.Now,
		data:      make(map[string]localEntry),
	}
}

func LocalSyncerFactory(name string, _ *json.Decoder, _ fwkplugin.Handle) (fwkplugin.Plugin, error) {
	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "local"
	}
	return NewLocalSyncer(name, hostname), nil
}

func (s *LocalSyncer) TypedName() fwkplugin.TypedName {
	return s.typedName
}

func (s *LocalSyncer) syncKey(key fwkdl.StateKey, id string) string {
	return s.replicaID + ":" + string(key) + ":" + id
}

func (s *LocalSyncer) Set(context.Context, fwkdl.CrossReplicaSpec, string) error {
	return nil
}

func (s *LocalSyncer) Get(_ context.Context, spec fwkdl.CrossReplicaSpec, endpointID string) (any, bool, error) {
	return spec.Read(endpointID), true, nil
}

func (s *LocalSyncer) Delete(context.Context, fwkdl.StateKey, string) error {
	return nil
}

// expired reports whether an entry is unusable at t. Entries without an
// expiration never are.
func (e localEntry) expired(t time.Time) bool {
	return !e.expiresAt.IsZero() && !t.Before(e.expiresAt)
}

func (s *LocalSyncer) GetOrSet(_ context.Context, key fwkdl.StateKey, id string, candidate any, opts ...fwkdl.DecisionOptions) (any, bool, error) {
	options := fwkdl.DecisionOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	syncKey := s.syncKey(key, id)
	entry, found := s.data[syncKey]
	if found && entry.expired(s.now()) {
		delete(s.data, syncKey)
		found = false
	}
	if found {
		if options.RefreshOnAccess {
			s.data[syncKey] = localEntry{value: entry.value, expiresAt: s.expiry(options)}
		}
		return entry.value, true, nil
	}
	s.data[syncKey] = localEntry{value: candidate, expiresAt: s.expiry(options)}
	return candidate, false, nil
}

func (s *LocalSyncer) Replace(_ context.Context, key fwkdl.StateKey, id string, value any, opts ...fwkdl.DecisionOptions) (any, bool, error) {
	options := fwkdl.DecisionOptions{}
	if len(opts) > 0 {
		options = opts[0]
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	syncKey := s.syncKey(key, id)
	entry, found := s.data[syncKey]
	if found && entry.expired(s.now()) {
		found = false
	}
	s.data[syncKey] = localEntry{value: value, expiresAt: s.expiry(options)}
	return valueOrPrevious(entry, found), found, nil
}

func valueOrPrevious(entry localEntry, found bool) any {
	if !found {
		return nil
	}
	return entry.value
}

// expiry returns the expiration an entry stored under options carries. A
// zero options TTL means no expiration, matching the pre-options behavior of
// decisions that live for the syncer's lifetime.
func (s *LocalSyncer) expiry(options fwkdl.DecisionOptions) time.Time {
	if options.TTL <= 0 {
		return time.Time{}
	}
	return s.now().Add(options.TTL)
}
