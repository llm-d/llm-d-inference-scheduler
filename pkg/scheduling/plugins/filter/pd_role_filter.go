package filter

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/gateway-api-inference-extension/pkg/epp/scheduling/plugins"
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
func NewPrefillFilter() (plugins.Filter, error) {
	selector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      RoleLabel,
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{RolePrefill},
		}},
	}
	return NewByLabel("prefill-filter", selector)
}

// NewDecodeFilter creates and returns an instance of the Filter configured for decode role
func NewDecodeFilter() (plugins.Filter, error) {
	selector := &metav1.LabelSelector{
		MatchExpressions: []metav1.LabelSelectorRequirement{{
			Key:      RoleLabel,
			Operator: metav1.LabelSelectorOpIn,
			Values:   []string{RoleDecode, RoleBoth},
		}},
	}
	return NewByLabel("decode-filter", selector)
}
