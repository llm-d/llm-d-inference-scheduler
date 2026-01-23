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

// Package runners provides runner implementations for P/D disaggregation.
//
// A Runner executes the P/D disaggregation workflow for a specific backend type
// (e.g., vLLM, SGLang). Runners can be configured with different Connectors,
// which define the protocol used for KV-cache transfer between prefill and decode.
//
// Available runners:
//   - VLLMRunner: For vLLM backends, uses a decode-first approach
//   - SGLangRunner: For SGLang backends, uses concurrent prefill/decode
//
// Available connectors (via RequestBuilderFactory):
//   - NIXL v2: Modern KV-cache transfer protocol (NIXLV2RequestBuilderFactory)
//   - Shared Storage: Legacy protocol using shared storage (DefaultRequestBuilderFactory)
//   - SGLang: Built into SGLangRunner
package runners
