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

	// DecodeType is the type of the DecodeFilter
	DecodeType = "decode-filter"
	// PrefillType is the type of the PrefillFilter
	PrefillType = "prefill-filter"
)

// PrefillFactory defines the factory function for the PrefillFilter
func PrefillFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewPrefill().WithName(name), nil
}

// NewPrefill creates and returns an instance of the Filter configured for prefill role
func NewPrefill() *ByLabel {
	return NewByLabel(PrefillType, RoleLabel, false, RolePrefill)
}

// DecodeFactory defines the factory function for the DecodeFilter
func DecodeFactory(name string, _ json.RawMessage, _ plugins.Handle) (plugins.Plugin, error) {
	return NewDecode().WithName(name), nil
}

// NewDecode creates and returns an instance of the Filter configured for decode role
func NewDecode() *ByLabel {
	return NewByLabel(DecodeType, RoleLabel, true, RoleDecode, RoleBoth)
}
