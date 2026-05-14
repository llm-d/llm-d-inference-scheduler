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
	"testing"

	"k8s.io/apimachinery/pkg/types"

	"github.com/stretchr/testify/assert"
)

type opKind int

const (
	opUpsert opKind = iota
	opDelete
)

type op struct {
	kind opKind
	id   types.NamespacedName
}

// trackingStore records all BackendUpsert and BackendDelete calls in arrival order.
type trackingStore struct {
	ops []op
}

func (t *trackingStore) BackendUpsert(_ context.Context, meta *EndpointMetadata) {
	t.ops = append(t.ops, op{opUpsert, meta.NamespacedName})
}

func (t *trackingStore) BackendDelete(id types.NamespacedName) {
	t.ops = append(t.ops, op{opDelete, id})
}

func TestNewDiscoveryNotifier_Upsert(t *testing.T) {
	store := &trackingStore{}
	notifier := NewDiscoveryNotifier(store)

	meta := &EndpointMetadata{
		NamespacedName: types.NamespacedName{Name: "ep-0", Namespace: "default"},
		Address:        "10.0.0.1",
		Port:           "8080",
	}
	notifier.Upsert(meta)

	assert.Len(t, store.ops, 1)
	assert.Equal(t, opUpsert, store.ops[0].kind)
	assert.Equal(t, meta.NamespacedName, store.ops[0].id)
}

func TestNewDiscoveryNotifier_Delete(t *testing.T) {
	store := &trackingStore{}
	notifier := NewDiscoveryNotifier(store)

	id := types.NamespacedName{Name: "ep-0", Namespace: "default"}
	notifier.Delete(id)

	assert.Len(t, store.ops, 1)
	assert.Equal(t, opDelete, store.ops[0].kind)
	assert.Equal(t, id, store.ops[0].id)
}

func TestNewDiscoveryNotifier_Order(t *testing.T) {
	store := &trackingStore{}
	notifier := NewDiscoveryNotifier(store)

	meta := &EndpointMetadata{NamespacedName: types.NamespacedName{Name: "ep-0", Namespace: "default"}}
	id := meta.NamespacedName

	notifier.Upsert(meta)
	notifier.Delete(id)

	// ordering contract: upsert must arrive before delete
	assert.Len(t, store.ops, 2)
	assert.Equal(t, opUpsert, store.ops[0].kind)
	assert.Equal(t, opDelete, store.ops[1].kind)
	assert.Equal(t, id, store.ops[0].id)
	assert.Equal(t, id, store.ops[1].id)
}
