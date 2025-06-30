package filter

import (
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/framework"
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
)

// NewPrefillFilter creates and returns an instance of the Filter configured for prefill role
func NewPrefillFilter() framework.Filter {
	return NewByLabel("prefill-filter", RoleLabel, false, RolePrefill)
}

// NewDecodeFilter creates and returns an instance of the Filter configured for decode role
func NewDecodeFilter() framework.Filter {
	return NewByLabel("decode-filter", RoleLabel, true, RoleDecode, RoleBoth)
}
