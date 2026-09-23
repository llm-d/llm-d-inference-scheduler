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

// Package toolcalling is a compatibility facade for the EPP tool-calling
// snapshot API. New consumers should import common/observability/toolcalling.
package toolcalling

import (
	common "github.com/llm-d/llm-d-router/pkg/common/observability/toolcalling"
	"go.opentelemetry.io/otel/attribute"
)

const (
	HeaderSnapshotHash      = common.HeaderSnapshotHash
	HeaderToolPresent       = common.HeaderToolPresent
	HeaderToolChoiceKind    = common.HeaderToolChoiceKind
	HeaderToolDefsCount     = common.HeaderToolDefsCount
	HeaderParallelToolCalls = common.HeaderParallelToolCalls

	ChoiceUnset    = common.ChoiceUnset
	ChoiceNone     = common.ChoiceNone
	ChoiceAuto     = common.ChoiceAuto
	ChoiceRequired = common.ChoiceRequired
	ChoiceNamed    = common.ChoiceNamed
	ChoiceUnknown  = common.ChoiceUnknown

	ParallelUnset   = common.ParallelUnset
	ParallelTrue    = common.ParallelTrue
	ParallelFalse   = common.ParallelFalse
	ParallelUnknown = common.ParallelUnknown

	PreservedTrue    = common.PreservedTrue
	PreservedFalse   = common.PreservedFalse
	PreservedUnknown = common.PreservedUnknown
)

type ToolCallingSnapshot = common.ToolCallingSnapshot

var (
	ExtractFromPayloadMap = common.ExtractFromPayloadMap
	PreservationStatus    = common.PreservationStatus
	ToHeaders             = common.ToHeaders
	FromHeaders           = common.FromHeaders
)

func SpanAttributes(s *ToolCallingSnapshot, boundary string) []attribute.KeyValue {
	return common.SpanAttributes(s, boundary)
}
