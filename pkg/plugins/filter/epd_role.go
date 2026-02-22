package filter

import (
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

const (
	// RoleLabel name
	RoleLabel = "llm-d.ai/role"

	// Single role values
	// Execution order: Encode → Prefill → Decode

	// RoleEncode set for designated encode workers (first stage in pipeline)
	RoleEncode = "encode"
	// RolePrefill set for designated prefill workers (second stage in pipeline)
	RolePrefill = "prefill"
	// RoleDecode set for designated decode workers (third stage in pipeline)
	RoleDecode = "decode"

	// RoleBoth set for workers that can act as both prefill and decode (legacy, equivalent to prefill-decode)
	RoleBoth = "both"
	// RoleEncodePrefill set for workers that can handle encode and prefill stages
	RoleEncodePrefill = "encode-prefill"
	// RolePrefillDecode set for workers that can handle prefill and decode stages
	RolePrefillDecode = "prefill-decode"
	// RoleEncodeDecode set for workers that can handle encode and decode stages (skipping prefill)
	RoleEncodeDecode = "encode-decode"
	// RoleAll set for workers that can handle all stages (encode, prefill, and decode)
	RoleAll = "all"

	// EncodeRoleType is the type of the EncodeFilter
	EncodeRoleType = "encode-filter"
	// PrefillRoleType is the type of the PrefillFilter
	PrefillRoleType = "prefill-filter"
	// DecodeRoleType is the type of the DecodeFilter
	DecodeRoleType = "decode-filter"
)

// EncodeRoleFactory defines the factory function for the Encode filter.
func EncodeRoleFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return NewEncodeRole().WithName(name), nil
}

// NewEncodeRole creates and returns an instance of the Filter configured for encode role.
// Encode is the first stage in the pipeline: Encode → Prefill → Decode
// Accepts pods with roles: encode, encode-prefill, encode-decode, or all.
func NewEncodeRole() *ByLabel {
	return NewByLabel(EncodeRoleType, RoleLabel, false,
		RoleEncode,
		RoleEncodePrefill,
		RoleEncodeDecode,
		RoleAll,
	)
}

// PrefillRoleFactory defines the factory function for the Prefill filter.
func PrefillRoleFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return NewPrefillRole().WithName(name), nil
}

// NewPrefillRole creates and returns an instance of the Filter configured for prefill role.
// Prefill is the second stage in the pipeline: Encode → Prefill → Decode
// Accepts pods with roles: prefill, both (legacy), encode-prefill, prefill-decode, or all.
func NewPrefillRole() *ByLabel {
	return NewByLabel(PrefillRoleType, RoleLabel, false,
		RolePrefill,
		RoleBoth,
		RoleEncodePrefill,
		RolePrefillDecode,
		RoleAll,
	)
}

// DecodeRoleFactory defines the factory function for the Decode filter.
func DecodeRoleFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return NewDecodeRole().WithName(name), nil
}

// NewDecodeRole creates and returns an instance of the Filter configured for decode role.
// Decode is the third stage in the pipeline: Encode → Prefill → Decode
// Accepts pods with roles: decode, both (legacy), prefill-decode, encode-decode, or all.
// Also allows pods without a role label (allowsNoLabel=true) for backward compatibility.
func NewDecodeRole() *ByLabel {
	return NewByLabel(DecodeRoleType, RoleLabel, true,
		RoleDecode,
		RoleBoth,
		RolePrefillDecode,
		RoleEncodeDecode,
		RoleAll,
	)
}
