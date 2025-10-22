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

// Package datastore provides a local in-memory cache for the activator component,
// storing the current InferencePool and managing a ticker used by background
// goroutines; it exposes thread-safe operations to get/set the pool and control
// the ticker.
package datastore

import (
	"context"
	"errors"
	"sync"
	"time"

	v1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
)

var (
	errPoolNotSynced = errors.New("InferencePool is not initialized in data store")
)

// Datastore is a local cache of relevant data for the given InferencePool (currently all pulled from k8s-api)
type Datastore interface {
	// InferencePool operations
	// PoolSet sets the given pool in datastore.
	PoolSet(pool *v1.InferencePool)
	PoolGet() (*v1.InferencePool, error)
	PoolHasSynced() bool

	GetTicker() *time.Ticker
	ResetTicker(t time.Duration)
	StopTicker()

	// Clears the store state, happens when the pool gets deleted.
	Clear()
}

// NewDatastore creates a new Datastore instance with the provided parent context.
func NewDatastore(parentCtx context.Context) Datastore {
	store := &datastore{
		parentCtx: parentCtx,
		poolMu:    sync.RWMutex{},
		ticker:    time.NewTicker(60 * time.Second),
	}
	return store
}

type datastore struct {
	// parentCtx controls the lifecycle of the background metrics goroutines that spawn up by the datastore.
	parentCtx context.Context
	// poolMu is used to synchronize access to pool map.
	poolMu sync.RWMutex
	pool   *v1.InferencePool
	ticker *time.Ticker
}

// /// InferencePool APIs ///
func (ds *datastore) PoolSet(pool *v1.InferencePool) {
	ds.poolMu.Lock()
	defer ds.poolMu.Unlock()

	ds.pool = pool
}

func (ds *datastore) PoolGet() (*v1.InferencePool, error) {
	ds.poolMu.RLock()
	defer ds.poolMu.RUnlock()
	if !ds.PoolHasSynced() {
		return nil, errPoolNotSynced
	}
	return ds.pool, nil
}

func (ds *datastore) PoolHasSynced() bool {
	ds.poolMu.RLock()
	defer ds.poolMu.RUnlock()
	return ds.pool != nil
}

func (ds *datastore) Clear() {
	ds.PoolSet(nil)
}

func (ds *datastore) ResetTicker(t time.Duration) {
	ds.poolMu.RLock()
	defer ds.poolMu.RUnlock()
	ds.ticker.Reset(t)
}

func (ds *datastore) GetTicker() *time.Ticker {
	ds.poolMu.RLock()
	defer ds.poolMu.RUnlock()
	return ds.ticker
}

func (ds *datastore) StopTicker() {
	ds.poolMu.RLock()
	defer ds.poolMu.RUnlock()
	ds.ticker.Stop()
}
