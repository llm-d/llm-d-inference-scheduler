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

package preciseprefixcache

import (
	"context"
	"reflect"
	"testing"

	"github.com/go-logr/logr"
	"github.com/llm-d/llm-d-kv-cache/pkg/kvevents"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	k8stypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/log"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/scheduling"
)

// discardCtx avoids a -race ding: the kvevents subscriber goroutine logs via
// the caller's logger, and a test-bound logger writing after t.Run cleanup
// races with the testing framework.
func discardCtx(t *testing.T) context.Context {
	t.Helper()
	return log.IntoContext(context.Background(), logr.Discard())
}

func newExtractorProducer(discoverPods bool) *Producer {
	cfg := kvevents.DefaultConfig()
	cfg.DiscoverPods = discoverPods
	cfg.PodDiscoveryConfig = kvevents.DefaultPodReconcilerConfig()
	cfg.PodDiscoveryConfig.SocketPort = 5557

	return &Producer{
		typedName:          plugin.TypedName{Type: PluginType, Name: PluginType},
		subscribersManager: kvevents.NewSubscriberManager(kvevents.NewPool(cfg, nil, nil, nil)),
		kvEventsConfig:     cfg,
		subscriberCtx:      context.Background(),
	}
}

func newEndpoint(name, addr string) fwkdl.Endpoint {
	return fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{
		NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: name},
		Address:        addr,
		Port:           "8080",
	}, nil)
}

func TestProducer_EndpointExtractor_InterfaceContract(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(ctx)

	assert.Equal(t, fwkdl.EndpointEventReflectType, p.ExpectedInputType())

	var _ fwkdl.EndpointExtractor = p
	assert.True(t, reflect.TypeOf(p).Implements(reflect.TypeFor[fwkdl.EndpointExtractor]()))
}

func TestProducer_ExtractEndpoint_AddAndDelete(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(ctx)

	ep := newEndpoint("pod-a", "10.0.0.1")
	wantKey := "ns/pod-a"
	wantEndpoint := "tcp://10.0.0.1:5557"

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: ep,
	}))

	ids, endpoints := p.subscribersManager.GetActiveSubscribers()
	require.Equal(t, []string{wantKey}, ids)
	require.Equal(t, []string{wantEndpoint}, endpoints)

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: ep,
	}))
	ids, _ = p.subscribersManager.GetActiveSubscribers()
	assert.Len(t, ids, 1, "duplicate add must not create a second subscriber")

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventDelete,
		Endpoint: ep,
	}))
	ids, _ = p.subscribersManager.GetActiveSubscribers()
	assert.Empty(t, ids)
}

// DiscoverPods=false is global-socket mode: per-pod discovery must be off.
func TestProducer_ExtractEndpoint_DiscoverPodsDisabledIsNoOp(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(false)
	defer p.subscribersManager.Shutdown(ctx)

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: newEndpoint("pod-a", "10.0.0.1"),
	}))

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	assert.Empty(t, ids)
}

func TestProducer_ExtractEndpoint_IgnoresMissingMetadata(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(ctx)

	ep := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{
		NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-a"},
	}, nil)

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: ep,
	}))

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	assert.Empty(t, ids)
}

// Regression: ensureSubscriber must use subscriberCtx, not the caller's
// request-scoped ctx — otherwise subscribers die when the request ends.
func TestProducer_EnsureSubscriber_SurvivesRequestCtxCancel(t *testing.T) {
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(context.Background())

	reqCtx, cancel := context.WithCancel(context.Background())

	require.NoError(t, p.ensureSubscriber(reqCtx, &fwkdl.EndpointMetadata{
		NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-a"},
		Address:        "10.0.0.1", Port: "8080",
	}))

	cancel()

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	assert.ElementsMatch(t, []string{"ns/pod-a"}, ids)
}

func TestProducer_EnsureSubscribersForEndpoints(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(ctx)

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-a"},
			Address:        "10.0.0.1", Port: "8080",
		}, nil, nil),
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-b"},
			Address:        "10.0.0.2", Port: "8080",
		}, nil, nil),
	}

	p.EnsureSubscribersForEndpoints(ctx, endpoints)

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	assert.ElementsMatch(t, []string{"ns/pod-a", "ns/pod-b"}, ids)
}

// Once ExtractEndpoint fires once, the legacy in-Produce discovery must stop
// so the data layer owns subscriber lifecycle.
func TestProducer_LegacyDiscovery_DisabledOnceExtractorObserved(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(ctx)

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventDelete,
		Endpoint: newEndpoint("pod-x", "10.0.0.99"),
	}))

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-a"},
			Address:        "10.0.0.1", Port: "8080",
		}, nil, nil),
	}
	p.EnsureSubscribersForEndpoints(ctx, endpoints)

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	assert.NotContains(t, ids, "ns/pod-a")
}

func TestProducer_EnsureSubscribers_DiscoverPodsDisabled(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(false)
	defer p.subscribersManager.Shutdown(ctx)

	endpoints := []scheduling.Endpoint{
		scheduling.NewEndpoint(&fwkdl.EndpointMetadata{
			NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-a"},
			Address:        "10.0.0.1", Port: "8080",
		}, nil, nil),
	}
	p.EnsureSubscribersForEndpoints(ctx, endpoints)

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	assert.Empty(t, ids)
}

// Delete events may omit address fields. The subscriber is keyed by
// NamespacedName, so delete must still succeed.
func TestProducer_ExtractEndpoint_DeleteWithMissingAddressRemovesExistingSubscriber(t *testing.T) {
	ctx := discardCtx(t)
	p := newExtractorProducer(true)
	defer p.subscribersManager.Shutdown(ctx)

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventAddOrUpdate,
		Endpoint: newEndpoint("pod-a", "10.0.0.1"),
	}))

	ids, _ := p.subscribersManager.GetActiveSubscribers()
	require.Len(t, ids, 1)

	deleteEndpoint := fwkdl.NewEndpoint(&fwkdl.EndpointMetadata{
		NamespacedName: k8stypes.NamespacedName{Namespace: "ns", Name: "pod-a"},
	}, nil)

	require.NoError(t, p.ExtractEndpoint(ctx, fwkdl.EndpointEvent{
		Type:     fwkdl.EventDelete,
		Endpoint: deleteEndpoint,
	}))

	ids, _ = p.subscribersManager.GetActiveSubscribers()
	assert.Empty(t, ids)
}
