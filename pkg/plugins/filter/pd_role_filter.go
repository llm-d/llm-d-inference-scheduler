package filter

import (
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/plugins"
)

const (
	// RoleLabel name
	RoleLabel = "llm-d.ai/role"
	// RolePrefill set for designated prefill workers
	RolePrefill = "prefill"
	// RoleDecode set for designated decode workers
	RoleDecode = "decode"
	// RoleBoth set for workers that can act as both prefill and decode
	RoleBoth = "both"

	// DecodeFilterType is the type of the DecodeFilter
	DecodeFilterType = "decode-filter"
	// PrefillFilterType is the type of the PrefillFilter
	PrefillFilterType = "prefill-filter"
)

// PrefillFilterFactory defines the factory function for the PrefillFilter
func PrefillFilterFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewPrefillFilter().WithName(name), nil
}

// NewPrefillFilter creates and returns an instance of the Filter configured for prefill role
func NewPrefillFilter() *ByLabel {
	return NewByLabel(PrefillFilterType, RoleLabel, false, RolePrefill)
}

// DecodeFilterFactory defines the factory function for the DecodeFilter
func DecodeFilterFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewDecodeFilter().WithName(name), nil
}

// NewDecodeFilter creates and returns an instance of the Filter configured for decode role
func NewDecodeFilter() *ByLabel {
	return NewByLabel(DecodeFilterType, RoleLabel, true, RoleDecode, RoleBoth)
}
