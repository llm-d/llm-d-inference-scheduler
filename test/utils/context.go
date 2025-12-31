// Package utils contains utilities for testing
//
//revive:disable:var-naming
package utils

//revive:enable:var-naming

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

// NewTestContext creates a new context with a logger associated with the testing.T.
// It simplifies the boilerplate of integrating klog/logr with unit tests.
func NewTestContext(t *testing.T) context.Context {
	t.Helper()

	logger := testr.New(t)
	return log.IntoContext(context.Background(), logger)
}
