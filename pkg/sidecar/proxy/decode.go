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

package proxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
)

func (s *Server) sendDecodeRequest(w http.ResponseWriter, r *http.Request, completionRequest map[string]any) {

	if s.forwardDataParallel && s.dataParallelHandler(w, r) {
		return
	}
	if !s.config.EnableChunkedDecode {
		dreq, err := setRequestBody(r, completionRequest)
		if err != nil {
			if err := errorJSONInvalid(err, w); err != nil {
				s.logger.Error(err, "failed to send error response to client")
			}
			return
		}

		s.logger.V(4).Info("sending request to decoder", "to", s.decoderURL.Host)
		s.decoderProxy.ServeHTTP(w, dreq)

		return
	}
	s.sendChunkedDecodeRequest(w, r, completionRequest)
}

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Partial response struct TODO: use openai-go?
type ChatCompletionResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
}

func (s *Server) sendChunkedDecodeRequest(w http.ResponseWriter, r *http.Request, completionRequest map[string]any) {

	if completionRequest == nil {
		var err error
		completionRequest, err = parseCompletionRequest(r)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(err.Error())) //nolint:all
			return
		}
	}

	//TODO: validate if we should run chunked decode for this request
	// based on the request parameters,
	// e.g., continue_final_message, add_generation_prompt, max tokens, etc.

	completionRequest[requestFieldMaxCompletionTokens] = s.config.DecodeChunkSize

	s.logger.V(4).Info("sending chunked decode request", "chunk size", s.config.DecodeChunkSize)

	messagesAny, ok := completionRequest["messages"]
	if !ok {
		s.logger.Error(nil, "chunked decode: missing 'messages' field in decode request")
		return
	}
	messages, ok := messagesAny.([]any)
	if !ok {
		s.logger.Error(nil, "chunked decode: invalid 'messages' field in decode request")
		return
	}

	respBody := []byte{}
	respStatusCode := 0
	var responseMessageContent string

	for {
		dreq, err := setRequestBody(r, completionRequest)
		if err != nil {
			if err := errorJSONInvalid(err, w); err != nil {
				s.logger.Error(err, "failed to send error response to client")
			}
			return
		}

		rec := httptest.NewRecorder()
		s.decoderProxy.ServeHTTP(rec, dreq)
		resp := rec.Result()
		defer resp.Body.Close()

		respBody, _ = io.ReadAll(resp.Body) // TODO: handle error
		var parsed ChatCompletionResponse
		if err := json.Unmarshal(respBody, &parsed); err != nil {
			s.logger.Error(err, "failed to decode response")
			return
		}
		respStatusCode = resp.StatusCode

		if len(parsed.Choices) == 0 {
			s.logger.Error(nil, "no choices in decoder response")
			return
		}

		choice := parsed.Choices[0]
		chunk := choice.Message.Content
		finishReason := choice.FinishReason

		s.logger.V(4).Info("decoder response chunk", "chunk", chunk, "finish_reason", finishReason)
		// Append chunk to build full response content
		responseMessageContent += chunk

		// Prepare for next iteration

		// Append assistant message to continue generation
		messages = append(messages, Message{
			Role:    "assistant",
			Content: chunk,
		})
		completionRequest["messages"] = messages

		// Do not pull KV cache next time
		delete(completionRequest, requestFieldKVTransferParams)

		completionRequest["continue_final_message"] = true
		completionRequest["add_generation_prompt"] = false

		s.logger.V(4).Info("chunked decode combined output", "output", responseMessageContent)

		// Stop unless the model was cut off due to token limit
		if finishReason != "length" {
			break
		}
	}

	// add the combined message to the final response
	var finalResponse ChatCompletionResponse
	if err := json.Unmarshal(respBody, &finalResponse); err != nil {
		s.logger.Error(err, "failed to decode final decoder response")
		return
	}
	if len(finalResponse.Choices) == 0 {
		s.logger.Error(nil, "no choices in final decoder response")
		return
	}
	finalResponse.Choices[0].Message.Content = responseMessageContent

	respBody, err := json.Marshal(finalResponse)
	if err != nil {
		s.logger.Error(err, "failed to marshal final decoder response")
		return
	}
	// Write response back to original writer
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(respStatusCode)
	w.Write(respBody) //nolint:errcheck
}

func setRequestBody(r *http.Request, completionRequest map[string]any) (*http.Request, error) {
	dbody, err := json.Marshal(completionRequest)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(strings.NewReader(string(dbody)))
	r.ContentLength = int64(len(dbody))
	return r, nil
}

func parseCompletionRequest(r *http.Request) (map[string]any, error) {
	defer r.Body.Close() //nolint:all
	original, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	var completionRequest map[string]any
	if err := json.Unmarshal(original, &completionRequest); err != nil { // TODO: use openai-go?
		return nil, err
	}
	return completionRequest, nil
}
