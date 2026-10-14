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
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/go-logr/logr/funcr"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
	"github.com/llm-d/llm-d-router/pkg/epp/framework/plugins/datalayer/source/notifications"
)

type batchSyncer struct {
	fakeSyncer
	onBatch func(context.Context, fwkdl.CrossReplicaSpec, []string) map[string]error
}

func (s *batchSyncer) SetBatch(ctx context.Context, spec fwkdl.CrossReplicaSpec, ids []string) map[string]error {
	return s.onBatch(ctx, spec, ids)
}

type blockingBatchSyncer struct {
	*blockingSyncer
	batches int
}

func (s *blockingBatchSyncer) SetBatch(ctx context.Context, spec fwkdl.CrossReplicaSpec, ids []string) map[string]error {
	s.batches++
	failures := make(map[string]error)
	for _, id := range ids {
		if err := s.Set(ctx, spec, id); err != nil {
			failures[id] = err
		}
	}
	return failures
}

func TestCrossReplicaPublisher_BatchContributorsShareCycleDeadline(t *testing.T) {
	type call struct {
		ctx context.Context
		key fwkdl.StateKey
		ids []string
		err error
	}
	started := make(chan call, 2)
	syncer := &batchSyncer{onBatch: func(ctx context.Context, spec fwkdl.CrossReplicaSpec, ids []string) map[string]error {
		started <- call{ctx: ctx, key: spec.StateKey, ids: ids, err: ctx.Err()}
		<-ctx.Done()
		return map[string]error{ids[0]: ctx.Err()}
	}}
	pub := testCrossReplicaPublisher(syncer, fakeContributor{key: "inflight:test"}, fakeContributor{key: "queue:test"})
	pub.publishTimeout = 200 * time.Millisecond
	ids := []types.NamespacedName{testEndpoint("ep-a").GetMetadata().GetID(), testEndpoint("ep-b").GetMetadata().GetID()}
	for _, id := range ids {
		require.True(t, pub.registerEndpoint(id))
	}
	before := time.Now()
	done := make(chan struct{})
	go func() { defer close(done); pub.publishAll(context.Background()) }()
	calls := make([]call, 0, 2)
	for range 2 {
		select {
		case c := <-started:
			calls = append(calls, c)
		case <-time.After(time.Second):
			t.Fatal("contributors did not both start")
		}
	}
	require.NotEqual(t, calls[0].key, calls[1].key)
	require.Same(t, calls[0].ctx, calls[1].ctx)
	for _, c := range calls {
		require.NoError(t, c.err, "contributors must start before cycle deadline")
		require.ElementsMatch(t, []string{"ns/ep-a", "ns/ep-b"}, c.ids)
		deadline, ok := c.ctx.Deadline()
		require.True(t, ok)
		require.WithinDuration(t, before.Add(pub.publishTimeout), deadline, 100*time.Millisecond)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cycle deadline did not end publication")
	}
	require.Empty(t, syncer.sets)
}

func TestCrossReplicaPublisher_BatchFiltersDeletedSnapshot(t *testing.T) {
	var got []string
	syncer := &batchSyncer{onBatch: func(_ context.Context, _ fwkdl.CrossReplicaSpec, ids []string) map[string]error {
		got = append(got, ids...)
		return nil
	}}
	pub := testCrossReplicaPublisher(syncer, fakeContributor{key: "inflight:test"})
	gone := testEndpoint("ep-gone").GetMetadata().GetID()
	kept := testEndpoint("ep-kept").GetMetadata().GetID()
	require.True(t, pub.registerEndpoint(gone))
	require.True(t, pub.registerEndpoint(kept))
	snapshot := pub.endpointSnapshot()
	removed, err := pub.delete(context.Background(), gone)
	require.NoError(t, err)
	require.True(t, removed)
	pub.publishBatch(context.Background(), syncer, snapshot)
	require.Equal(t, []string{"ns/ep-kept"}, got)
	require.Empty(t, syncer.sets)
}

func TestCrossReplicaPublisher_BatchAggregatesFailureLogs(t *testing.T) {
	syncer := &batchSyncer{onBatch: func(_ context.Context, _ fwkdl.CrossReplicaSpec, ids []string) map[string]error {
		failures := make(map[string]error)
		for _, id := range ids {
			failures[id] = errors.New("Redis unavailable")
		}
		return failures
	}}
	pub := testCrossReplicaPublisher(syncer, fakeContributor{key: "inflight:test"})
	for _, name := range []string{"ep-a", "ep-b"} {
		require.True(t, pub.registerEndpoint(testEndpoint(name).GetMetadata().GetID()))
	}
	var lines []string
	ctx := log.IntoContext(context.Background(), funcr.New(func(_, line string) { lines = append(lines, line) }, funcr.Options{}))
	pub.publishAll(ctx)
	pub.publishAll(ctx)
	require.Len(t, lines, 1)
	require.True(t, strings.Contains(lines[0], `"failedEndpoints"=2`), lines[0])
}

func TestCrossReplicaPublisher_NonBatchPublishesEachEndpoint(t *testing.T) {
	syncer := &fakeSyncer{}
	pub := testCrossReplicaPublisher(syncer, fakeContributor{key: "inflight:test"}, fakeContributor{key: "queue:test"})
	for _, name := range []string{"ep-a", "ep-b"} {
		require.True(t, pub.registerEndpoint(testEndpoint(name).GetMetadata().GetID()))
	}
	pub.publishAll(context.Background())
	require.Len(t, syncer.sets, 4)
	for _, key := range []fwkdl.StateKey{"inflight:test", "queue:test"} {
		var ids []string
		for _, call := range syncer.sets {
			if call.key == key {
				ids = append(ids, call.endpointID)
			}
		}
		require.ElementsMatch(t, []string{"ns/ep-a", "ns/ep-b"}, ids)
	}
}

func TestReleaseEndpointWaitsForInFlightBatch(t *testing.T) {
	syncer := &blockingBatchSyncer{blockingSyncer: newBlockingSyncer()}
	contributor := fakeEndpointContributor{fakeContributor: fakeContributor{key: "inflight:test"}}
	source := notifications.NewEndpointDataSource(notifications.EndpointNotificationSourceType, "endpoint-source")
	r := NewRuntime(time.Second)
	r.crossReplicaPub = testCrossReplicaPublisher(syncer, contributor)
	r.endpoint.Set(source)
	r.extractors.Append(source.TypedName().Name, contributor)
	ep := r.NewEndpoint(context.Background(), testEndpoint("ep-gone").GetMetadata())
	require.NotNil(t, ep)
	publishDone := make(chan struct{})
	go func() { defer close(publishDone); r.crossReplicaPub.publishAll(context.Background()) }()
	select {
	case <-syncer.setStarted:
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	releaseDone := make(chan struct{})
	go func() { defer close(releaseDone); r.ReleaseEndpoint(ep) }()
	select {
	case <-releaseDone:
		t.Error("ReleaseEndpoint completed during batch")
	case <-time.After(50 * time.Millisecond):
	}
	close(syncer.allowSet)
	select {
	case <-publishDone:
	case <-time.After(time.Second):
		t.Fatal("batch deadlocked with waiting writer")
	}
	select {
	case <-releaseDone:
	case <-time.After(time.Second):
		t.Fatal("ReleaseEndpoint did not complete")
	}
	require.Empty(t, r.crossReplicaPub.endpointSnapshot())
	r.crossReplicaPub.publishAll(context.Background())
	syncer.mu.Lock()
	defer syncer.mu.Unlock()
	require.NotContains(t, syncer.state, "ns/ep-gone")
	require.Equal(t, []string{"set", "delete"}, syncer.events)
	require.Equal(t, 1, syncer.batches)
}

func TestCrossReplicaPublisher_BatchDoesNotDeadlockWithWaitingWriter(t *testing.T) {
	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	allowFirst := make(chan struct{})
	allowSecondSpec := make(chan struct{})
	syncer := &batchSyncer{onBatch: func(ctx context.Context, spec fwkdl.CrossReplicaSpec, _ []string) map[string]error {
		if spec.StateKey == "inflight:test" {
			close(firstStarted)
			select {
			case <-allowFirst:
			case <-ctx.Done():
			}
		} else {
			close(secondStarted)
		}
		return nil
	}}
	second := batchSpecContributor{spec: func() fwkdl.CrossReplicaSpec {
		<-allowSecondSpec
		return fakeContributor{key: "queue:test"}.CrossReplicaState()
	}}
	pub := testCrossReplicaPublisher(syncer, fakeContributor{key: "inflight:test"}, second)
	id := testEndpoint("ep-a").GetMetadata().GetID()
	require.True(t, pub.registerEndpoint(id))
	done := make(chan struct{})
	go func() { defer close(done); pub.publishAll(context.Background()) }()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("batch did not start")
	}
	writerDone := make(chan struct{})
	go func() { defer close(writerDone); pub.registerEndpoint(testEndpoint("ep-b").GetMetadata().GetID()) }()
	require.Eventually(t, func() bool {
		if pub.mu.TryRLock() {
			pub.mu.RUnlock()
			return false
		}
		return true
	}, time.Second, time.Millisecond, "writer must be waiting for batch read lock")
	close(allowSecondSpec)
	select {
	case <-secondStarted:
	case <-time.After(200 * time.Millisecond):
		t.Error("waiting writer blocked second batch contributor")
	}
	close(allowFirst)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("batch deadlocked")
	}
	select {
	case <-writerDone:
	case <-time.After(time.Second):
		t.Fatal("writer deadlocked")
	}
}

type batchSpecContributor struct{ spec func() fwkdl.CrossReplicaSpec }

func (c batchSpecContributor) CrossReplicaState() fwkdl.CrossReplicaSpec { return c.spec() }
