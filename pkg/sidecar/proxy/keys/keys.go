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

// Package keys provides constants for HTTP request/response field keys and header names
// used throughout the proxy package for parsing and constructing API requests.
package keys

// Request Header keys
const (
	RequestHeaderRequestID = "x-request-id"
)

// Request Field keys
const (
	RequestFieldKVTransferParams    = "kv_transfer_params"
	RequestFieldMaxTokens           = "max_tokens"
	RequestFieldMaxCompletionTokens = "max_completion_tokens"
	RequestFieldDoRemotePrefill     = "do_remote_prefill"
	RequestFieldDoRemoteDecode      = "do_remote_decode"
	RequestFieldRemoteBlockIDs      = "remote_block_ids"
	RequestFieldRemoteEngineID      = "remote_engine_id"
	RequestFieldRemoteHost          = "remote_host"
	RequestFieldRemotePort          = "remote_port"
	RequestFieldStream              = "stream"
	RequestFieldStreamOptions       = "stream_options"
	RequestFieldCacheHitThreshold   = "cache_hit_threshold"

	// SGLang bootstrap fields
	RequestFieldBootstrapHost = "bootstrap_host"
	RequestFieldBootstrapPort = "bootstrap_port"
	RequestFieldBootstrapRoom = "bootstrap_room"
)

// Response Field keys
const (
	ResponseFieldChoices      = "choices"
	ResponseFieldFinishReason = "finish_reason"
)

// Finish Reason keys
const (
	FinishReasonCacheThreshold = "cache_threshold"
)
