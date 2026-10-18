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
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	fwkdl "github.com/llm-d/llm-d-router/pkg/epp/framework/interface/datalayer"
)

type cloneableInt int

func (v cloneableInt) Clone() fwkdl.Cloneable { return v }

func TestLocalSyncerReturnsLiveLocalValue(t *testing.T) {
	syncer := NewLocalSyncer("test", "replica-a")
	aggregateCalls := 0
	aggregate := func(values []any) any {
		aggregateCalls++
		return values[0]
	}
	local := 21
	spec := fwkdl.CrossReplicaSpec{
		StateKey:  "load",
		Read:      func(string) fwkdl.Cloneable { return cloneableInt(local) },
		Aggregate: aggregate,
	}

	require.NoError(t, syncer.Set(context.Background(), spec, "default/backend-0"))
	value, ok, err := syncer.Get(context.Background(), spec, "default/backend-0")

	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, cloneableInt(21), value)
	assert.Zero(t, aggregateCalls)

	local = 22
	value, ok, err = syncer.Get(context.Background(), spec, "default/backend-0")
	require.NoError(t, err)
	require.True(t, ok)
	assert.Equal(t, cloneableInt(22), value)
	assert.Zero(t, aggregateCalls)
}

func TestLocalSyncerConcurrentStateOperations(t *testing.T) {
	syncer := NewLocalSyncer("test", "replica-a")
	spec := fwkdl.CrossReplicaSpec{
		StateKey:  "load",
		Read:      func(string) fwkdl.Cloneable { return cloneableInt(1) },
		Aggregate: func(values []any) any { return values[0] },
	}

	const iterations = 100
	errs := make(chan error, 4*iterations)
	var wg sync.WaitGroup
	for range 2 {
		wg.Go(func() {
			for range iterations {
				errs <- syncer.Set(context.Background(), spec, "default/backend-0")
				_, _, err := syncer.Get(context.Background(), spec, "default/backend-0")
				errs <- err
			}
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
}

func TestLocalSyncerGetOrSet(t *testing.T) {
	syncer := NewLocalSyncer("test", "replica-a")

	value, existed, err := syncer.GetOrSet(context.Background(), "request", "request-id", "first")
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, "first", value)

	value, existed, err = syncer.GetOrSet(context.Background(), "request", "request-id", "second")
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, "first", value)
}

// newTestSyncer returns a LocalSyncer whose clock starts at a fixed instant
// and a function advancing that clock.
func newTestSyncer(t *testing.T) (*LocalSyncer, func(time.Duration)) {
	t.Helper()
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	syncer := NewLocalSyncer("test", "replica")
	syncer.now = func() time.Time { return now }
	return syncer, func(d time.Duration) { now = now.Add(d) }
}

func TestLocalSyncerSessionSurvivesIdleWindowWithinTTL(t *testing.T) {
	syncer, advance := newTestSyncer(t)
	ctx := context.Background()
	opts := fwkdl.DecisionOptions{TTL: 10 * time.Minute, RefreshOnAccess: true}

	_, _, err := syncer.GetOrSet(ctx, "sessions", "s1", "v1", opts)
	require.NoError(t, err)

	// Four idle minutes: well inside the ten-minute TTL.
	advance(4 * time.Minute)
	actual, existed, err := syncer.GetOrSet(ctx, "sessions", "s1", "v2", opts)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, "v1", actual)
}

func TestLocalSyncerRegularAccessKeepsSessionAliveBeyondTotalTTL(t *testing.T) {
	syncer, advance := newTestSyncer(t)
	ctx := context.Background()
	opts := fwkdl.DecisionOptions{TTL: 10 * time.Minute, RefreshOnAccess: true}

	_, _, err := syncer.GetOrSet(ctx, "sessions", "s1", "v1", opts)
	require.NoError(t, err)

	// Twenty minutes of two-minute-gapped accesses: each refresh pushes the
	// expiration out, so the decision outlives its nominal TTL.
	for range 10 {
		advance(2 * time.Minute)
		actual, existed, err := syncer.GetOrSet(ctx, "sessions", "s1", "v2", opts)
		require.NoError(t, err)
		assert.True(t, existed)
		assert.Equal(t, "v1", actual)
	}
}

func TestLocalSyncerSessionExpiresAfterTTLInactivity(t *testing.T) {
	syncer, advance := newTestSyncer(t)
	ctx := context.Background()
	opts := fwkdl.DecisionOptions{TTL: 10 * time.Minute, RefreshOnAccess: true}

	_, _, err := syncer.GetOrSet(ctx, "sessions", "s1", "v1", opts)
	require.NoError(t, err)

	advance(10 * time.Minute)
	actual, existed, err := syncer.GetOrSet(ctx, "sessions", "s1", "v2", opts)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Equal(t, "v2", actual)
}

func TestLocalSyncerNoOptionsNeverExpires(t *testing.T) {
	syncer, advance := newTestSyncer(t)
	ctx := context.Background()

	// The zero options keep the implementation default: no expiration, the
	// behavior request-level decisions rely on.
	_, _, err := syncer.GetOrSet(ctx, "requests", "r1", "v1")
	require.NoError(t, err)

	advance(24 * time.Hour)
	actual, existed, err := syncer.GetOrSet(ctx, "requests", "r1", "v2")
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, "v1", actual)
}

func TestLocalSyncerReplaceOverwritesAndReturnsPrevious(t *testing.T) {
	syncer, advance := newTestSyncer(t)
	ctx := context.Background()
	opts := fwkdl.DecisionOptions{TTL: 10 * time.Minute, RefreshOnAccess: true}

	_, _, err := syncer.GetOrSet(ctx, "sessions", "s1", "v1", opts)
	require.NoError(t, err)

	previous, existed, err := syncer.Replace(ctx, "sessions", "s1", "v2", opts)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, "v1", previous)

	actual, existed, err := syncer.GetOrSet(ctx, "sessions", "s1", "v3", opts)
	require.NoError(t, err)
	assert.True(t, existed)
	assert.Equal(t, "v2", actual)

	// A Replace over an expired entry reports nothing previous.
	advance(10 * time.Minute)
	previous, existed, err = syncer.Replace(ctx, "sessions", "s1", "v4", opts)
	require.NoError(t, err)
	assert.False(t, existed)
	assert.Nil(t, previous)
}
