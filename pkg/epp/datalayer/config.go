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

// Config holds the data layer's configured sources and their extractors.
type Config struct {
	Sources []DataSourceConfig
}

// DataSourceConfig pairs a source plugin with its extractor plugins. Extractors
// are typed at Configure time against Plugin's variant; mismatches error out.
type DataSourceConfig struct {
	Plugin     fwkdl.DataSource
	Extractors []fwkplugin.Plugin
}

// ResolveSource looks up ref and asserts the plugin implements DataSource.
func ResolveSource(handle fwkplugin.Handle, ref string) (fwkdl.DataSource, error) {
	p := handle.Plugin(ref)
	if p == nil {
		return nil, fmt.Errorf("source plugin %q not registered", ref)
	}
	src, ok := p.(fwkdl.DataSource)
	if !ok {
		return nil, fmt.Errorf("source plugin %q does not implement DataSource", ref)
	}
	return src, nil
}

// assertAll types each plugin to T. T is bound at compile time at the call
// site; per-element conformance is a runtime check that returns an error
// rather than panicking, because plugins arrive as the erased plugin.Plugin.
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
