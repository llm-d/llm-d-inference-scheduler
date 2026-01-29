/*
Copyright 2026 The llm-d Authors.

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

// Package types contains common types for the runners package.
package types

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/go-logr/logr"
)

// Connector represents a P/D disaggregation protocol type.
type Connector string

const (
	// ConnectorNIXLV2 enables the P/D NIXL v2 protocol
	ConnectorNIXLV2 Connector = "nixlv2"

	// ConnectorSharedStorage enables (now deprecated) P/D Shared Storage protocol
	ConnectorSharedStorage Connector = "shared-storage"

	// ConnectorSGLang enables SGLang P/D disaggregation protocol
	ConnectorSGLang Connector = "sglang"
)

// allConnectors contains all supported connector types.
var allConnectors = []Connector{
	ConnectorNIXLV2,
	ConnectorSharedStorage,
	ConnectorSGLang,
}

// String returns the string representation of the Connector.
func (c Connector) String() string {
	return string(c)
}

// Validate validates a connector string and returns the corresponding Connector type.
// Returns an error if the connector is not valid.
func (c Connector) Validate() error {
	switch c {
	case ConnectorNIXLV2:
		return nil
	case ConnectorSharedStorage:
		return nil
	case ConnectorSGLang:
		return nil
	default:
		return fmt.Errorf("invalid connector %q: must be one of: %s", c, AllConnectorStrings())
	}
}

// AllConnectorStrings returns a comma-separated string of all supported connector types.
// Useful for CLI help messages.
func AllConnectorStrings() string {
	strs := make([]string, len(allConnectors))
	for i, c := range allConnectors {
		strs[i] = c.String()
	}
	return strings.Join(strs, ", ")
}

//go:generate moq -stub -out mock/types.go  -pkg mock . ProtocolRunner

type (
	// ProtocolRunner executes the P/D disaggregation workflow for inference requests.
	// Different implementations handle different backend types (vLLM, SGLang, etc.).
	ProtocolRunner interface {
		Run(w http.ResponseWriter, r *http.Request, prefillPodHost string, logger logr.Logger)
	}

	// RequestBuilderFactory creates RequestBuilder instances for a specific connector protocol.
	// The factory pattern allows runners to be configured with different P/D protocols.
	RequestBuilderFactory interface {
		New() RequestBuilder
	}

	// RequestBuilder prepares requests for prefill and decode phases according to
	// a specific connector protocol (e.g., NIXL v2, Shared Storage).
	RequestBuilder interface {
		PreparePrefillRequest(completionRequest map[string]any) map[string]any
		PrepareDecodeRequest(completionRequest map[string]any, prefillResponse map[string]any) map[string]any
	}
)
