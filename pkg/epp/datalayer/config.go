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
	"fmt"

	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
	fwkplugin "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/plugin"
)

// Config defines the configuration of EPP data layer, as the set of DataSources
// and Extractors defined on them.
type Config struct {
	Sources []DataSourceConfig
}

// DataSourceConfig defines the configuration of a specific DataSource.
//
// Each source pairs with extractors of exactly one variant (polling,
// notification, or endpoint) — the variant is determined by the source's
// interface type. Exactly one of the *Extractors fields is populated by
// ResolveExtractors; the Runtime reads the field matching the source's variant.
type DataSourceConfig struct {
	Plugin fwkdl.DataSource

	// Populated when Plugin is a PollingDataSource.
	PollingExtractors []fwkdl.PollingExtractor
	// Populated when Plugin is a NotificationSource.
	NotificationExtractors []fwkdl.NotificationExtractor
	// Populated when Plugin is an EndpointSource.
	EndpointExtractors []fwkdl.EndpointExtractor
}

// ResolveExtractors routes plugins into the field matching this source's
// variant, type-asserting each to the variant's extractor interface.
func (c *DataSourceConfig) ResolveExtractors(plugins []fwkplugin.Plugin) error {
	var err error
	switch c.Plugin.(type) {
	case fwkdl.PollingDataSource:
		c.PollingExtractors, err = assertAll[fwkdl.PollingExtractor](plugins, "PollingExtractor")
	case fwkdl.NotificationSource:
		c.NotificationExtractors, err = assertAll[fwkdl.NotificationExtractor](plugins, "NotificationExtractor")
	case fwkdl.EndpointSource:
		c.EndpointExtractors, err = assertAll[fwkdl.EndpointExtractor](plugins, "EndpointExtractor")
	default:
		return fmt.Errorf("source %s does not implement a known DataSource variant (polling/notification/endpoint)",
			c.Plugin.TypedName())
	}
	return err
}

// assertAll type-asserts each plugin to the variant interface E and returns
// the typed slice. Returns an error naming the first plugin that doesn't
// implement T.
func assertAll[T fwkplugin.Plugin](plugins []fwkplugin.Plugin, variant string) ([]T, error) {
	out := make([]T, 0, len(plugins))
	for _, p := range plugins {
		e, ok := p.(T)
		if !ok {
			return nil, fmt.Errorf("plugin %s is not a %s", p.TypedName(), variant)
		}
		out = append(out, e)
	}
	return out, nil
}
