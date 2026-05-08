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
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/common/observability/logging"
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// variantLookup pairs a sourceVariant with its FindByType adapter; r.variants
// (built in NewRuntime) is the single source of truth for variant iteration.
type variantLookup struct {
	variant sourceVariant
	find    func(sourceType string, gvkFilter *schema.GroupVersionKind) (string, fwkdl.DataSource, bool)
}

// Runtime routes per-variant state to the matching manager and owns
// per-endpoint Collectors and pending dependency resolution.
type Runtime struct {
	pollingInterval time.Duration

	polling      *pollingManager
	notification *notificationManager
	endpoint     *endpointManager

	// variants enumerates every (sourceVariant, FindByType-adapter) pair.
	// Registered in NewRuntime; consumed by findSourceByType.
	variants []variantLookup

	pendingMu            sync.Mutex
	pendingRegistrations []fwkdl.PendingRegistration

	// sync.Map: per-pod reconciles write concurrently to this map; the variant
	// maps inside the managers don't (write-once at Configure).
	collectors sync.Map
	logger     logr.Logger
}

const defaultRefreshInterval = 50 * time.Millisecond

// NewRuntime returns a Runtime; non-positive interval falls back to defaultRefreshInterval.
func NewRuntime(pollingInterval time.Duration) *Runtime {
	interval := defaultRefreshInterval
	if pollingInterval > 0 {
		interval = pollingInterval
	}
	r := &Runtime{
		pollingInterval: interval,
		polling:         newPollingManager(),
		notification:    newNotificationManager(),
		endpoint:        newEndpointManager(),
		logger:          logr.Discard(),
	}
	r.variants = r.buildVariantLookups()
	return r
}

// buildVariantLookups registers every variant's FindByType adapter — adding a
// new variant means adding one row here.
func (r *Runtime) buildVariantLookups() []variantLookup {
	return []variantLookup{
		{variantPolling, func(t string, _ *schema.GroupVersionKind) (string, fwkdl.DataSource, bool) {
			n, s, ok := r.polling.FindByType(t, nil)
			return n, s, ok
		}},
		{variantNotification, func(t string, gvkFilter *schema.GroupVersionKind) (string, fwkdl.DataSource, bool) {
			var nfilter func(fwkdl.NotificationSource) bool
			if gvkFilter != nil {
				want := gvkFilter.String()
				nfilter = func(s fwkdl.NotificationSource) bool { return s.GVK().String() == want }
			}
			n, s, ok := r.notification.FindByType(t, nfilter)
			return n, s, ok
		}},
		{variantEndpoint, func(t string, _ *schema.GroupVersionKind) (string, fwkdl.DataSource, bool) {
			n, s, ok := r.endpoint.FindByType(t, nil)
			return n, s, ok
		}},
	}
}

// Configure installs cfg's sources/extractors and resolves pending dependencies.
func (r *Runtime) Configure(cfg *Config, enableNewMetrics bool, disallowedExtractorType string, logger logr.Logger) error {
	hasPending := len(r.pendingRegistrations) > 0
	if (cfg == nil || len(cfg.Sources) == 0) && !hasPending {
		if enableNewMetrics {
			return errors.New("data layer enabled but no data sources configured")
		}
		return nil
	}

	r.logger = logger
	numSources := 0
	if cfg != nil {
		numSources = len(cfg.Sources)
	}
	logger.Info("Configuring datalayer runtime", "numSources", numSources)

	if cfg != nil {
		for _, srcCfg := range cfg.Sources {
			if err := r.addSource(srcCfg.Plugin, srcCfg.Extractors, disallowedExtractorType); err != nil {
				return err
			}
			logger.V(logging.DEFAULT).Info("Source configured",
				"source", srcCfg.Plugin.TypedName().Name,
				"extractors", len(srcCfg.Extractors))
		}
	}

	for _, pending := range r.pendingRegistrations {
		if err := r.resolvePending(pending, disallowedExtractorType, logger); err != nil {
			return err
		}
	}

	logger.Info("Datalayer runtime configured",
		"pollers", r.polling.Count(),
		"notifiers", r.notification.Count(),
		"endpointSources", r.endpoint.Count())
	return nil
}

// Register queues a pending dependency for Configure to resolve.
func (r *Runtime) Register(reg fwkdl.PendingRegistration) error {
	if reg.Extractor == nil {
		return fmt.Errorf("plugin %s: PendingRegistration.Extractor must not be nil", reg.Owner)
	}
	r.pendingMu.Lock()
	r.pendingRegistrations = append(r.pendingRegistrations, reg)
	r.pendingMu.Unlock()
	return nil
}

// addSource dispatches to the matching variant's handler.
func (r *Runtime) addSource(src fwkdl.DataSource, exts []fwkplugin.Plugin, disallowedType string) error {
	switch s := src.(type) {
	case fwkdl.PollingDataSource:
		return r.addPolling(s, exts, disallowedType)
	case fwkdl.NotificationSource:
		return r.addNotification(s, exts, disallowedType)
	case fwkdl.EndpointSource:
		return r.addEndpoint(s, exts, disallowedType)
	default:
		return fmt.Errorf("unknown datasource plugin type %s", src.TypedName().String())
	}
}

func (r *Runtime) addPolling(src fwkdl.PollingDataSource, exts []fwkplugin.Plugin, disallowedType string) error {
	typed, err := assertExtractors[fwkdl.PollingExtractor](src, exts, "PollingExtractor", disallowedType)
	if err != nil {
		return err
	}
	return r.polling.Register(src, typed)
}

func (r *Runtime) addNotification(src fwkdl.NotificationSource, exts []fwkplugin.Plugin, disallowedType string) error {
	typed, err := assertExtractors[fwkdl.NotificationExtractor](src, exts, "NotificationExtractor", disallowedType)
	if err != nil {
		return err
	}
	if err := validateNotificationGVK(src, typed); err != nil {
		return err
	}
	return r.notification.Register(src, typed)
}

func (r *Runtime) addEndpoint(src fwkdl.EndpointSource, exts []fwkplugin.Plugin, disallowedType string) error {
	typed, err := assertExtractors[fwkdl.EndpointExtractor](src, exts, "EndpointExtractor", disallowedType)
	if err != nil {
		return err
	}
	return r.endpoint.Register(src, typed)
}

// appendPendingExtractor dispatches one code-registered extractor to the matching variant.
func (r *Runtime) appendPendingExtractor(srcName string, src fwkdl.DataSource, ext fwkplugin.Plugin, disallowedType string) error {
	switch s := src.(type) {
	case fwkdl.PollingDataSource:
		return r.appendPendingPolling(srcName, s, ext, disallowedType)
	case fwkdl.NotificationSource:
		return r.appendPendingNotification(srcName, s, ext, disallowedType)
	case fwkdl.EndpointSource:
		return r.appendPendingEndpoint(srcName, s, ext, disallowedType)
	default:
		return fmt.Errorf("matched source %s has unknown variant", srcName)
	}
}

func (r *Runtime) appendPendingPolling(srcName string, src fwkdl.PollingDataSource, ext fwkplugin.Plugin, disallowedType string) error {
	typed, err := assertExtractors[fwkdl.PollingExtractor](src, []fwkplugin.Plugin{ext}, "PollingExtractor", disallowedType)
	if err != nil {
		return fmt.Errorf("code-registered for source %s: %w", srcName, err)
	}
	r.polling.AppendExtractor(srcName, typed[0])
	return nil
}

func (r *Runtime) appendPendingNotification(srcName string, src fwkdl.NotificationSource, ext fwkplugin.Plugin, disallowedType string) error {
	typed, err := assertExtractors[fwkdl.NotificationExtractor](src, []fwkplugin.Plugin{ext}, "NotificationExtractor", disallowedType)
	if err != nil {
		return fmt.Errorf("code-registered for source %s: %w", srcName, err)
	}
	if err := validateNotificationGVK(src, typed); err != nil {
		return fmt.Errorf("code-registered for source %s: %w", srcName, err)
	}
	r.notification.AppendExtractor(srcName, typed[0])
	return nil
}

func (r *Runtime) appendPendingEndpoint(srcName string, src fwkdl.EndpointSource, ext fwkplugin.Plugin, disallowedType string) error {
	typed, err := assertExtractors[fwkdl.EndpointExtractor](src, []fwkplugin.Plugin{ext}, "EndpointExtractor", disallowedType)
	if err != nil {
		return fmt.Errorf("code-registered for source %s: %w", srcName, err)
	}
	r.endpoint.AppendExtractor(srcName, typed[0])
	return nil
}

// resolvePending matches a pending dependency to a configured source, falls
// back to its DefaultSource, then appends the extractor.
func (r *Runtime) resolvePending(pending fwkdl.PendingRegistration, disallowedType string, logger logr.Logger) error {
	var gvkFilter *schema.GroupVersionKind
	if ns, ok := pending.DefaultSource.(fwkdl.NotificationSource); ok {
		gvk := ns.GVK()
		gvkFilter = &gvk
	}
	srcName, matchedSrc, err := r.findSourceByType(pending.SourceType, gvkFilter)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", pending.Extractor.TypedName(), err)
	}

	if matchedSrc == nil {
		if pending.DefaultSource == nil {
			msg := fmt.Sprintf("extractor %s requires source type %s, not configured",
				pending.Extractor.TypedName(), pending.SourceType)
			if pending.IfMissing == fwkdl.Warn {
				logger.Info("datalayer: skipping unresolved dependency", "reason", msg)
				return nil
			}
			return errors.New(msg)
		}
		if err := r.addSource(pending.DefaultSource, nil, disallowedType); err != nil {
			return fmt.Errorf("auto-register default source for %s: %w",
				pending.Extractor.TypedName(), err)
		}
		srcName = pending.DefaultSource.TypedName().Name
		matchedSrc = pending.DefaultSource
	}

	return r.appendPendingExtractor(srcName, matchedSrc, pending.Extractor, disallowedType)
}

// findSourceByType walks r.variants; gvkFilter narrows notification matches.
// A type registered across more than one variant is a configuration error.
func (r *Runtime) findSourceByType(sourceType string, gvkFilter *schema.GroupVersionKind) (string, fwkdl.DataSource, error) {
	var (
		matchedName    string
		matchedSrc     fwkdl.DataSource
		matchedVariant sourceVariant
	)
	for _, l := range r.variants {
		name, src, ok := l.find(sourceType, gvkFilter)
		if !ok {
			continue
		}
		if matchedSrc != nil {
			return "", nil, fmt.Errorf("source type %q is registered across variants: %s (%s) and %s (%s)",
				sourceType, matchedVariant, matchedName, l.variant, name)
		}
		matchedVariant, matchedName, matchedSrc = l.variant, name, src
	}
	return matchedName, matchedSrc, nil
}

// Start wires Kubernetes notifications into the manager.
func (r *Runtime) Start(ctx context.Context, mgr ctrl.Manager) error {
	sources := r.notification.Sources()
	extractors := r.notification.Extractors()
	for srcName, ns := range sources {
		if err := BindNotificationSource(ns, extractors[srcName], mgr); err != nil {
			return fmt.Errorf("failed to bind notification source %s: %w", ns.TypedName(), err)
		}
	}
	return nil
}

// Stop halts all per-endpoint collectors.
func (r *Runtime) Stop() {
	r.collectors.Range(func(_, val any) bool {
		if c, ok := val.(*Collector); ok {
			c.Stop()
		}
		return true
	})
}

// NewEndpoint sets up data polling on the provided endpoint.
func (r *Runtime) NewEndpoint(ctx context.Context, endpointMetadata *fwkdl.EndpointMetadata, _ PoolInfo) fwkdl.Endpoint {
	logger, _ := logr.FromContext(ctx)
	logger = logger.WithValues("endpoint", endpointMetadata.GetNamespacedName())

	pollerMap := r.polling.Sources()
	if len(pollerMap) == 0 {
		logger.Info("No polling sources configured, creating endpoint without collector")
		endpoint := fwkdl.NewEndpoint(endpointMetadata, nil)
		r.dispatchEndpointEvent(ctx, logger, fwkdl.EndpointEvent{Type: fwkdl.EventAddOrUpdate, Endpoint: endpoint})
		return endpoint
	}

	pollers := make([]fwkdl.PollingDataSource, 0, len(pollerMap))
	for _, p := range pollerMap {
		pollers = append(pollers, p)
	}
	extractors := r.polling.Extractors()

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

// ReleaseEndpoint terminates polling for the given endpoint.
func (r *Runtime) ReleaseEndpoint(ep fwkdl.Endpoint) {
	r.dispatchEndpointEvent(context.Background(), r.logger, fwkdl.EndpointEvent{Type: fwkdl.EventDelete, Endpoint: ep})
	key := ep.GetMetadata().GetNamespacedName()
	if value, ok := r.collectors.LoadAndDelete(key); ok {
		if c, ok := value.(*Collector); ok {
			c.Stop()
		}
	}
}

// dispatchEndpointEvent fans event out to every EndpointSource and its extractors.
func (r *Runtime) dispatchEndpointEvent(ctx context.Context, logger logr.Logger, event fwkdl.EndpointEvent) {
	if r.endpoint.IsEmpty() {
		return
	}
	sources := r.endpoint.Sources()
	for srcName, epSrc := range sources {
		processed, err := epSrc.NotifyEndpoint(ctx, event)
		if err != nil {
			logger.Error(err, "endpoint source failed to process event", "source", srcName)
			continue
		}
		if processed == nil {
			continue
		}
		for _, ext := range r.endpoint.ExtractorsFor(srcName) {
			if err := ext.Extract(ctx, *processed); err != nil {
				logger.Error(err, "endpoint extractor failed", "extractor", ext.TypedName())
			}
		}
	}
}

// validateExtractors applies the disallowed-type guard and the source's
// optional Validator[E] to each extractor.
func validateExtractors[E fwkplugin.Plugin](src fwkdl.DataSource, exts []E, disallowedType string) error {
	validator, hasValidator := src.(fwkdl.Validator[E])
	for _, ext := range exts {
		if disallowedType != "" && ext.TypedName().Type == disallowedType {
			return fmt.Errorf("disallowed Extractor %s is configured for source %s",
				ext.TypedName(), src.TypedName())
		}
		if hasValidator {
			if err := validator.Validate(ext); err != nil {
				return fmt.Errorf("extractor %s failed custom validation for datasource %s: %w",
					ext.TypedName(), src.TypedName(), err)
			}
		}
	}
	return nil
}

// validateNotificationGVK enforces that every extractor's GVK matches the source's GVK.
func validateNotificationGVK(src fwkdl.NotificationSource, exts []fwkdl.NotificationExtractor) error {
	srcGVK := src.GVK().String()
	for _, ext := range exts {
		if ext.GVK().String() != srcGVK {
			return fmt.Errorf("extractor %s GVK %s does not match source %s GVK %s",
				ext.TypedName(), ext.GVK().String(), src.TypedName(), srcGVK)
		}
	}
	return nil
}

// assertExtractors types exts to []E and runs validateExtractors on the result.
func assertExtractors[E fwkplugin.Plugin](
	src fwkdl.DataSource, exts []fwkplugin.Plugin, variantName, disallowedType string,
) ([]E, error) {
	typed, err := assertAll[E](exts, variantName)
	if err != nil {
		return nil, err
	}
	if err := validateExtractors[E](src, typed, disallowedType); err != nil {
		return nil, err
	}
	return typed, nil
}

var _ EndpointFactory = (*Runtime)(nil)
var _ fwkdl.Registrar = (*Runtime)(nil)
