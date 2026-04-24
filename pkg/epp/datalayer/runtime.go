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
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
)

// Runtime manages data sources, extractors, their mapping, and endpoint lifecycle.
type Runtime struct {
	pollingInterval time.Duration // used for polling sources

	pollers         sync.Map // Map of polling sources (key=source name, value=PollingDataSource)
	notifiers       sync.Map // Map of k8s notification sources (key=source name, value=NotificationSource)
	endpointSources sync.Map // Map of endpoint sources (key=source name, value=EndpointSource)

	pollingExtractors      sync.Map // Map of polling extractors (key=source name, value=[]PollingExtractor)
	notificationExtractors sync.Map // Map of notification extractors (key=source name, value=[]NotificationExtractor)
	endpointExtractors     sync.Map // Map of endpoint extractors (key=source name, value=[]EndpointExtractor)

	collectors sync.Map    // Per-endpoint poller (key=namespaced name, value=*Collector)
	logger     logr.Logger // Set in Configure; used where no context is available (e.g. ReleaseEndpoint).
}

const (
	defaultRefreshInterval = 50 * time.Millisecond
)

// NewRuntime creates a new Runtime with the given polling interval.
// If duration is <= 0, uses the defaultRefreshInterval.
func NewRuntime(pollingInterval time.Duration) *Runtime {
	interval := defaultRefreshInterval
	if pollingInterval > 0 {
		interval = pollingInterval
	}
	return &Runtime{
		pollingInterval: interval,
		logger:          logr.Discard(),
	}
}

// Configure is called to transform the configuration information into the Runtime's
// internal fields.
func (r *Runtime) Configure(cfg *Config, enableNewMetrics bool, disallowedExtractorType string, logger logr.Logger) error {
	if cfg == nil || len(cfg.Sources) == 0 {
		if enableNewMetrics {
			return errors.New("data layer enabled but no data sources configured")
		}
		return nil
	}

	r.logger = logger
	logger.Info("Configuring datalayer runtime", "numSources", len(cfg.Sources))

	pollersCount := 0
	notifiersCount := 0
	endpointSourcesCount := 0
	gvkToSource := make(map[string]string, len(cfg.Sources))

	for _, srcCfg := range cfg.Sources {
		src := srcCfg.Plugin
		srcName := src.TypedName().Name

		switch s := src.(type) {
		case fwkdl.PollingDataSource:
			if len(srcCfg.NotificationExtractors) > 0 || len(srcCfg.EndpointExtractors) > 0 {
				return fmt.Errorf("source %s is a PollingDataSource; only PollingExtractors are allowed", srcName)
			}
			if err := r.validatePolling(s, srcCfg.PollingExtractors, disallowedExtractorType); err != nil {
				return err
			}
			r.pollers.Store(srcName, s)
			if len(srcCfg.PollingExtractors) > 0 {
				r.pollingExtractors.Store(srcName, srcCfg.PollingExtractors)
			}
			pollersCount++

		case fwkdl.NotificationSource:
			if len(srcCfg.PollingExtractors) > 0 || len(srcCfg.EndpointExtractors) > 0 {
				return fmt.Errorf("source %s is a NotificationSource; only NotificationExtractors are allowed", srcName)
			}
			gvk := s.GVK().String()
			if existingSource, exists := gvkToSource[gvk]; exists {
				return fmt.Errorf("duplicate notification source GVK %s: already used by source %s, cannot add %s",
					gvk, existingSource, src.TypedName().String())
			}
			if err := r.validateNotification(s, srcCfg.NotificationExtractors, disallowedExtractorType); err != nil {
				return err
			}
			r.notifiers.Store(srcName, s)
			if len(srcCfg.NotificationExtractors) > 0 {
				r.notificationExtractors.Store(srcName, srcCfg.NotificationExtractors)
			}
			gvkToSource[gvk] = srcName
			notifiersCount++

		case fwkdl.EndpointSource:
			if len(srcCfg.PollingExtractors) > 0 || len(srcCfg.NotificationExtractors) > 0 {
				return fmt.Errorf("source %s is an EndpointSource; only EndpointExtractors are allowed", srcName)
			}
			if err := r.validateEndpoint(s, srcCfg.EndpointExtractors, disallowedExtractorType); err != nil {
				return err
			}
			r.endpointSources.Store(srcName, s)
			if len(srcCfg.EndpointExtractors) > 0 {
				r.endpointExtractors.Store(srcName, srcCfg.EndpointExtractors)
			}
			endpointSourcesCount++

		default:
			return fmt.Errorf("unknown datasource plugin type %s", src.TypedName().String())
		}

		logger.V(logging.DEFAULT).Info("Source configured",
			"source", srcName,
			"polling", len(srcCfg.PollingExtractors),
			"notification", len(srcCfg.NotificationExtractors),
			"endpoint", len(srcCfg.EndpointExtractors))
	}

	logger.Info("Datalayer runtime configured",
		"pollers", pollersCount, "notifiers", notifiersCount, "endpointSources", endpointSourcesCount)
	return nil
}

func (r *Runtime) validatePolling(src fwkdl.PollingDataSource, exts []fwkdl.PollingExtractor, disallowedType string) error {
	validator, hasValidator := src.(fwkdl.ValidatingDataSource)
	for _, ext := range exts {
		if disallowedType != "" && ext.TypedName().Type == disallowedType {
			return fmt.Errorf("disallowed Extractor %s is configured for source %s",
				ext.TypedName(), src.TypedName())
		}
		if hasValidator {
			if err := validator.ValidateExtractor(ext); err != nil {
				return fmt.Errorf("extractor %s failed custom validation for datasource %s: %w",
					ext.TypedName(), src.TypedName(), err)
			}
		}
	}
	return nil
}

func (r *Runtime) validateNotification(src fwkdl.NotificationSource, exts []fwkdl.NotificationExtractor, disallowedType string) error {
	srcGVK := src.GVK().String()
	validator, hasValidator := src.(fwkdl.ValidatingDataSource)
	for _, ext := range exts {
		if disallowedType != "" && ext.TypedName().Type == disallowedType {
			return fmt.Errorf("disallowed Extractor %s is configured for source %s",
				ext.TypedName(), src.TypedName())
		}
		if ext.GVK().String() != srcGVK {
			return fmt.Errorf("extractor %s GVK %s does not match source %s GVK %s",
				ext.TypedName(), ext.GVK().String(), src.TypedName(), srcGVK)
		}
		if hasValidator {
			if err := validator.ValidateExtractor(ext); err != nil {
				return fmt.Errorf("extractor %s failed custom validation for datasource %s: %w",
					ext.TypedName(), src.TypedName(), err)
			}
		}
	}
	return nil
}

func (r *Runtime) validateEndpoint(src fwkdl.EndpointSource, exts []fwkdl.EndpointExtractor, disallowedType string) error {
	validator, hasValidator := src.(fwkdl.ValidatingDataSource)
	for _, ext := range exts {
		if disallowedType != "" && ext.TypedName().Type == disallowedType {
			return fmt.Errorf("disallowed Extractor %s is configured for source %s",
				ext.TypedName(), src.TypedName())
		}
		if hasValidator {
			if err := validator.ValidateExtractor(ext); err != nil {
				return fmt.Errorf("extractor %s failed custom validation for datasource %s: %w",
					ext.TypedName(), src.TypedName(), err)
			}
		}
	}
	return nil
}

// Start is called to enable the Runtime to start processing data collection. It wires
// Kubernetes notifications into the manager.
func (r *Runtime) Start(ctx context.Context, mgr ctrl.Manager) error {
	var err error

	r.notifiers.Range(func(key, val any) bool {
		ns := val.(fwkdl.NotificationSource)
		srcName := ns.TypedName().Name

		var extractors []fwkdl.NotificationExtractor
		if rawExts, ok := r.notificationExtractors.Load(srcName); ok {
			extractors = rawExts.([]fwkdl.NotificationExtractor)
		}

		if bindErr := BindNotificationSource(ns, extractors, mgr); bindErr != nil {
			err = fmt.Errorf("failed to bind notification source %s: %w", ns.TypedName(), bindErr)
			return false
		}
		return true
	})
	return err
}

// Stop terminates the Runtime's data collection. It stops all per-endpoint collectors.
func (r *Runtime) Stop() error {
	r.collectors.Range(func(_, val any) bool {
		if c, ok := val.(*Collector); ok {
			_ = c.Stop()
		}
		return true
	})
	return nil
}

// NewEndpoint sets up data polling on the provided endpoint.
func (r *Runtime) NewEndpoint(ctx context.Context, endpointMetadata *fwkdl.EndpointMetadata, _ PoolInfo) fwkdl.Endpoint {
	// TODO: should we cache the sources and map after Configure? Or just replace with maps and Mutex?
	// The code could be simpler and also would benefit from using RLock mutex for concurrent access
	// (no change expected) instead of using sync.Map (avoid use of Range just to count, more idiomatic code, etc.).
	logger, _ := logr.FromContext(ctx)
	logger = logger.WithValues("endpoint", endpointMetadata.GetNamespacedName())

	var pollers []fwkdl.PollingDataSource
	r.pollers.Range(func(_, val any) bool {
		if poller, ok := val.(fwkdl.PollingDataSource); ok {
			pollers = append(pollers, poller)
		}
		return true
	})

	if len(pollers) == 0 {
		logger.Info("No polling sources configured, creating endpoint without collector")
		endpoint := fwkdl.NewEndpoint(endpointMetadata, nil)
		r.dispatchEndpointEvent(ctx, logger, fwkdl.EndpointEvent{Type: fwkdl.EventAddOrUpdate, Endpoint: endpoint})
		return endpoint
	}

	extractors := make(map[string][]fwkdl.PollingExtractor, len(pollers))
	r.pollingExtractors.Range(func(key, val any) bool {
		srcName := key.(string)
		extractors[srcName] = val.([]fwkdl.PollingExtractor)
		return true
	})

	endpoint := fwkdl.NewEndpoint(endpointMetadata, nil)
	collector := NewCollector()

	key := endpointMetadata.GetNamespacedName()
	if _, loaded := r.collectors.LoadOrStore(key, collector); loaded {
		logger.V(logging.DEFAULT).Info("collector already running for endpoint", "endpoint", key)
		return nil
	}

	ticker := NewTimeTicker(r.pollingInterval)
	if err := collector.Start(ctx, ticker, endpoint, pollers, extractors); err != nil {
		logger.Error(err, "failed to start collector for endpoint", "endpoint", key)
		r.collectors.Delete(key)
		return nil
	}

	r.dispatchEndpointEvent(ctx, logger, fwkdl.EndpointEvent{Type: fwkdl.EventAddOrUpdate, Endpoint: endpoint})
	return endpoint
}

// ReleaseEndpoint terminates polling for data on the given endpoint.
func (r *Runtime) ReleaseEndpoint(ep fwkdl.Endpoint) {
	r.dispatchEndpointEvent(context.Background(), r.logger, fwkdl.EndpointEvent{Type: fwkdl.EventDelete, Endpoint: ep})

	key := ep.GetMetadata().GetNamespacedName()
	if value, ok := r.collectors.LoadAndDelete(key); ok {
		collector := value.(*Collector)
		_ = collector.Stop()
	}
}

// dispatchEndpointEvent routes an endpoint lifecycle event to all registered
// EndpointSources and their typed extractors.
func (r *Runtime) dispatchEndpointEvent(ctx context.Context, logger logr.Logger, event fwkdl.EndpointEvent) {
	if isEmpty(&r.endpointSources) {
		return
	}
	r.endpointSources.Range(func(key, val any) bool {
		srcName := key.(string)
		epSrc := val.(fwkdl.EndpointSource)

		processed, err := epSrc.NotifyEndpoint(ctx, event)
		if err != nil {
			logger.Error(err, "endpoint source failed to process event", "source", srcName)
			return true
		}
		if processed == nil {
			return true
		}

		rawExts, ok := r.endpointExtractors.Load(srcName)
		if !ok {
			return true
		}
		for _, ext := range rawExts.([]fwkdl.EndpointExtractor) {
			if err := ext.Extract(ctx, *processed); err != nil {
				logger.Error(err, "endpoint extractor failed", "extractor", ext.TypedName())
			}
		}
		return true
	})
}

// isEmpty reports whether the sync.Map has no entries.
func isEmpty(m *sync.Map) bool {
	empty := true
	m.Range(func(_, _ any) bool {
		empty = false
		return false // stop immediately
	})
	return empty
}

var _ EndpointFactory = (*Runtime)(nil)
