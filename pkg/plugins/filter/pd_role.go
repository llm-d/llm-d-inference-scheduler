package filter

import (
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
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

	// DecodeRoleType is the type of the DecodeFilter
	DecodeRoleType = "decode-filter"
	// PrefillRoleType is the type of the PrefillFilter
	PrefillRoleType = "prefill-filter"
	// RolePrefillDecode set for workers that can handle prefill and decode stages
	RolePrefillDecode = "prefill-decode"
)

// PrefillRoleFactory defines the factory function for the Prefill filter.
func PrefillRoleFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return NewPrefillRole().WithName(name), nil
}

// NewPrefillRole creates and returns an instance of the Filter configured for prefill role
func NewPrefillRole() *ByLabel {
	return NewByLabel(PrefillRoleType, RoleLabel, false, RolePrefill, RolePrefillDecode, RoleBoth)
}

// DecodeRoleFactory defines the factory function for the Decode filter.
func DecodeRoleFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return NewDecodeRole().WithName(name), nil
}

// NewDecodeRole creates and returns an instance of the Filter configured for decode role
func NewDecodeRole() *ByLabel {
	return NewByLabel(DecodeRoleType, RoleLabel, true, RoleDecode, RolePrefillDecode, RoleBoth)
}
