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
	fwkdl "github.com/llm-d/llm-d-inference-scheduler/pkg/epp/framework/interface/datalayer"
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
// interface type. Exactly one of the *Extractors fields is populated by the
// config loader; the Runtime reads the field matching the source's variant.
type DataSourceConfig struct {
	Plugin fwkdl.DataSource

	// Populated when Plugin is a PollingDataSource.
	PollingExtractors []fwkdl.PollingExtractor
	// Populated when Plugin is a NotificationSource.
	NotificationExtractors []fwkdl.NotificationExtractor
	// Populated when Plugin is an EndpointSource.
	EndpointExtractors []fwkdl.EndpointExtractor
}
