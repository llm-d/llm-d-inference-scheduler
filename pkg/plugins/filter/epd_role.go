package filter

import (
	"encoding/json"

	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/framework/interface/plugin"
)

const (
	// RoleEncode set for designated encode workers (first stage in pipeline)
	RoleEncode = "encode"

	// RoleEncodePrefill set for workers that can handle encode+prefill (EP/D or P/D disaggregation)
	RoleEncodePrefill = "encode-prefill"

	// RoleEncodeDecode set for workers that can handle encode+decode (E/PD disaggregation - rare)
	RoleEncodeDecode = "encode-decode"

	// RoleEncodePrefillDecode set for workers that can handle encode+prefill+decode
	RoleEncodePrefillDecode = "encode-prefill-decode"

	// EncodeRoleType is the type of the EncodeFilter
	EncodeRoleType = "encode-filter"
)

// EncodeRoleFactory defines the factory function for the Encode filter.
func EncodeRoleFactory(name string, _ json.RawMessage, _ plugin.Handle) (plugin.Plugin, error) {
	return NewEncodeRole().WithName(name), nil
}

// NewEncodeRole creates and returns an instance of the Filter configured for encode role.
// Encode is the first stage in the pipeline: Encode → Prefill → Decode
// Accepts pods with roles: encode, encode-prefill, encode-decode, or encode-prefill-decode.
func NewEncodeRole() *ByLabel {
	return NewByLabel(EncodeRoleType, RoleLabel, false,
		RoleEncode,
		RoleEncodePrefill,
		RoleEncodeDecode,
		RoleEncodePrefillDecode,
	)
}
