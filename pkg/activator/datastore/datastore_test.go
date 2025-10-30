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

package datastore

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"

	v1 "sigs.k8s.io/gateway-api-inference-extension/api/v1"
	testutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/testing"
)

func TestPool(t *testing.T) {
	pool1Selector := map[string]string{"app": "vllm_v1"}
	pool1 := testutil.MakeInferencePool("pool1").
		Namespace("default").
		Selector(pool1Selector).ObjRef()
	tests := []struct {
		name          string
		inferencePool *v1.InferencePool
		wantSynced    bool
		wantPool      *v1.InferencePool
		wantErr       error
	}{
		{
			name:          "Ready when InferencePool exists in data store",
			inferencePool: pool1,
			wantSynced:    true,
			wantPool:      pool1,
		},
		{
			name:          "Labels not matched",
			inferencePool: pool1,
			wantSynced:    true,
			wantPool:      pool1,
		},
		{
			name:       "Not ready when InferencePool is nil in data store",
			wantErr:    errPoolNotSynced,
			wantSynced: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Set up the scheme.
			scheme := runtime.NewScheme()
			_ = clientgoscheme.AddToScheme(scheme)

			datastore := NewDatastore(context.Background())
			datastore.PoolSet(tt.inferencePool)
			gotPool, gotErr := datastore.PoolGet()
			if diff := cmp.Diff(tt.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected error diff (+got/-want): %s", diff)
			}
			if diff := cmp.Diff(tt.wantPool, gotPool); diff != "" {
				t.Errorf("Unexpected pool diff (+got/-want): %s", diff)
			}
			gotSynced := datastore.PoolHasSynced()
			if diff := cmp.Diff(tt.wantSynced, gotSynced); diff != "" {
				t.Errorf("Unexpected synced diff (+got/-want): %s", diff)
			}
		})
	}
}
