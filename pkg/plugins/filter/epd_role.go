package filter

import (
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

const (
	// RoleEncode set for designated encode workers (first stage in pipeline)
	RoleEncode = "encode"

	// RoleEncodePrefill set for workers that can handle encode and prefill stages
	RoleEncodePrefill = "encode-prefill"

	// RoleEncodeDecode set for workers that can handle encode and decode stages (skipping prefill)
	RoleEncodeDecode = "encode-decode"
	// RoleAll set for workers that can handle all stages (encode, prefill, and decode)
	RoleAll = "all"

	// EncodeRoleType is the type of the EncodeFilter
	EncodeRoleType = "encode-filter"
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
