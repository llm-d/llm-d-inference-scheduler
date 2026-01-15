/*
Copyright 2025 The llm-d Authors.

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

// Package connectors provides protocol implementations for different P/D disaggregation protocols.
package connectors

import (
	"net/http"

	"github.com/go-logr/logr"
)

//go:generate moq -stub -out mock/types.go  -pkg mock . ProtocolRunner

type (
	// ProtocolRunner is the interface for a protocol runner
	ProtocolRunner interface {
		Run(w http.ResponseWriter, r *http.Request, prefillPodHost string, logger logr.Logger)
	}

	// RequestBuilderFactory is the interface for a request builder factory
	RequestBuilderFactory interface {
		New() RequestBuilder
	}

	// RequestBuilder is the interface for a request builder
	RequestBuilder interface {
		PreparePrefillRequest(completionRequest map[string]any) map[string]any
		PrepareDecodeRequest(completionRequest map[string]any, prefillResponse map[string]any) map[string]any
	}
)
