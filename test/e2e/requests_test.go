package e2e

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

func runCompletion(prompt string, theModel openai.CompletionNewParamsModel) (string, string, string) {
	var httpResp *http.Response
	openaiclient := openai.NewClient(
		option.WithBaseURL(fmt.Sprintf("http://localhost:%s/v1", port)))

	completionParams := openai.CompletionNewParams{
		Prompt: openai.CompletionNewParamsPromptUnion{
			OfString: openai.String(prompt),
		},
		Model: theModel,
	}

	ginkgo.By(fmt.Sprintf("Sending Completion Request: (port %s) %#v", port, completionParams))

	resp, err := openaiclient.Completions.New(testConfig.Context, completionParams, option.WithResponseInto(&httpResp), option.WithRequestTimeout(readyTimeout))

	ginkgo.By(fmt.Sprintf("Verifying Completion Response: %#v", resp))

	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Expect(resp.Choices).Should(gomega.HaveLen(1))
	gomega.Expect(resp.Choices[0].FinishReason).Should(gomega.Equal(openai.CompletionChoiceFinishReasonStop))
	gomega.Expect(resp.Choices[0].Text).Should(gomega.Equal(prompt))

	namespaceHeader := httpResp.Header.Get("x-inference-namespace")
	podHeader := httpResp.Header.Get("x-inference-pod")
	podPort := httpResp.Header.Get("x-inference-port")

	return namespaceHeader, podHeader, podPort
}

func runChatCompletion(prompt, modelName string) (string, string, string) {
	var httpResp *http.Response
	openaiclient := openai.NewClient(
		option.WithBaseURL(fmt.Sprintf("http://localhost:%s/v1", port)))

	params := openai.ChatCompletionNewParams{
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage(prompt),
		},
		Model: modelName,
	}
	resp, err := openaiclient.Chat.Completions.New(testConfig.Context, params, option.WithResponseInto(&httpResp))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	gomega.Expect(resp.Choices).Should(gomega.HaveLen(1))
	gomega.Expect(resp.Choices[0].FinishReason).Should(gomega.Equal("stop"))
	gomega.Expect(resp.Choices[0].Message.Content).Should(gomega.Equal(prompt))

	namespaceHeader := httpResp.Header.Get("x-inference-namespace")
	podHeader := httpResp.Header.Get("x-inference-pod")
	podPort := httpResp.Header.Get("x-inference-port")

	return namespaceHeader, podHeader, podPort
}

// runChatCompletionWithImage sends a multimodal chat completion request with an image_url content block.
// Returns the namespace and pod name from the response headers.
func runChatCompletionWithImage() (string, string) {
	ginkgo.By("Sending Multimodal Chat Completion Request with image: " + testImageURL)

	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":%q}},{"type":"text","text":"What is in this image?"}]}]}`,
		simModelName, testImageURL)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()
	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	_, err = io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	return resp.Header.Get("x-inference-namespace"),
		resp.Header.Get("x-inference-pod")
}

// runChatCompletionWithVideo sends a multimodal chat completion request with a video_url content block.
// Returns the namespace and pod name from the response headers.
func runChatCompletionWithVideo() (string, string) {
	ginkgo.By("Sending Multimodal Chat Completion Request with video: " + testVideoURL)

	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":[{"type":"text","text":"What is happening in this video?"},{"type":"video_url","video_url":{"url":%q}}]}]}`,
		simModelName, testVideoURL)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()
	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	_, err = io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	return resp.Header.Get("x-inference-namespace"),
		resp.Header.Get("x-inference-pod")
}

// runChatCompletionWithImageEmbeds sends a chat completion request with an image_embeds content block
// carrying a pre-encoded tensor. image_embeds is not a recognised multimodal type for encode
// disaggregation, so the request routes like a text request (decode-only or prefill-decode).
// Returns the namespace and pod name from the response headers.
func runChatCompletionWithImageEmbeds() (string, string) {
	ginkgo.By("Sending Chat Completion Request with image_embeds")

	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":[{"type":"text","text":"Describe this embedded image:"},{"type":"image_embeds","image_embeds":%q,"uuid":"embedded-image-1"}]}]}`,
		simModelName, testImageEmbeds)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()
	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	_, err = io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	return resp.Header.Get("x-inference-namespace"),
		resp.Header.Get("x-inference-pod")
}

// runChatCompletionWithAudio sends a chat completion request with an input_audio content block.
// input_audio is a recognised multimodal type so it triggers the encode stage.
// Returns the namespace and pod name from the response headers.
func runChatCompletionWithAudio() (string, string) {
	ginkgo.By("Sending Chat Completion Request with input_audio")

	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":[{"type":"text","text":"What is being said in this audio clip?"},{"type":"input_audio","input_audio":{"data":%q,"format":"wav"}}]}],"max_tokens":100}`,
		simModelName, testAudioData)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()
	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	_, err = io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	return resp.Header.Get("x-inference-namespace"),
		resp.Header.Get("x-inference-pod")
}

// runChatCompletionWithMultipleImages sends a multimodal chat completion request with multiple image_url
// content blocks. Each image is assigned a uuid derived from its index. Returns the namespace and pod name.
func runChatCompletionWithMultipleImages(imageURLs []string) (string, string) {
	ginkgo.By(fmt.Sprintf("Sending Multimodal Chat Completion Request with %d images", len(imageURLs)))

	var sb strings.Builder
	for i, url := range imageURLs {
		sb.WriteString(fmt.Sprintf(`{"type":"image_url","image_url":{"url":%q},"uuid":"multi-image-%d"},`, url, i))
	}
	imageBlocks := sb.String()
	body := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":[%s{"type":"text","text":"Compare these images and describe what you see."}]}],"max_tokens":150}`,
		simModelName, imageBlocks)

	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()
	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	_, err = io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	return resp.Header.Get("x-inference-namespace"),
		resp.Header.Get("x-inference-pod")
}

func runStreamingCompletion(prompt string, theModel openai.CompletionNewParamsModel) (string, string) {
	ginkgo.By(fmt.Sprintf("Sending Streaming Completion Request: (port %s) model=%s", port, theModel))

	// Use raw HTTP for streaming to capture headers
	body := fmt.Sprintf(`{"model":"%s","prompt":"%s","max_tokens":50,"stream":true}`, theModel, prompt)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Read and verify the streaming response
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	ginkgo.By(fmt.Sprintf("Streaming Completion received response length: %d bytes", len(respBody)))

	return namespaceHeader, podHeader
}

func runStreamingChatCompletion(prompt string) (string, string) {
	ginkgo.By(fmt.Sprintf("Sending Streaming Chat Completion Request: (port %s)", port))

	// Use raw HTTP for streaming to capture headers
	body := fmt.Sprintf(`{"model":"%s","messages":[{"role":"user","content":"%s"}],"stream":true}`, simModelName, prompt)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/chat/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Read and verify the streaming response
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	ginkgo.By(fmt.Sprintf("Streaming Chat Completion received response length: %d bytes", len(respBody)))

	return namespaceHeader, podHeader
}

// runCompletionWithCacheThreshold sends a completion request with cache_hit_threshold parameter.
// This triggers the decode-first optimization in the shared-storage connector.
// Returns namespace header, pod header, and the finish reason from the response.
func runCompletionWithCacheThreshold(prompt string, cacheHitThreshold float64, forceCacheThresholdFinishReason bool) (string, string, string) {
	ginkgo.By(fmt.Sprintf("Sending Completion Request with cache_hit_threshold=%v, forceCacheThreshold=%v", cacheHitThreshold, forceCacheThresholdFinishReason))

	body := fmt.Sprintf(`{"model":"%s","prompt":"%s","max_tokens":10,"cache_hit_threshold":%v}`, simModelName, prompt, cacheHitThreshold)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	// Add X-Cache-Threshold header to force the simulator to return cache_threshold finish_reason
	if forceCacheThresholdFinishReason {
		req.Header.Set("X-Cache-Threshold-Finish-Reason", "true")
	}

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Parse response to get finish_reason
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	// Extract finish_reason from JSON response
	finishReason := extractFinishReason(string(respBody))

	ginkgo.By(fmt.Sprintf("Completion Response: ns=%s, pod=%s, finish_reason=%s", namespaceHeader, podHeader, finishReason))

	return namespaceHeader, podHeader, finishReason
}

// runStreamingCompletionWithCacheThreshold sends a streaming completion request with cache_hit_threshold.
func runStreamingCompletionWithCacheThreshold(prompt string, cacheHitThreshold float64, forceCacheThresholdFinishReason bool) (string, string, string) {
	ginkgo.By(fmt.Sprintf("Sending Streaming Completion Request with cache_hit_threshold=%v, forceCacheThreshold=%v", cacheHitThreshold, forceCacheThresholdFinishReason))

	body := fmt.Sprintf(`{"model":"%s","prompt":"%s","max_tokens":10,"stream":true,"cache_hit_threshold":%v}`, simModelName, prompt, cacheHitThreshold)
	req, err := http.NewRequest(http.MethodPost, fmt.Sprintf("http://localhost:%s/v1/completions", port), strings.NewReader(body))
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	req.Header.Set("Content-Type", "application/json")

	if forceCacheThresholdFinishReason {
		req.Header.Set("X-Cache-Threshold-Finish-Reason", "true")
	}

	resp, err := http.DefaultClient.Do(req)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())
	defer func() {
		err := resp.Body.Close()
		gomega.Expect(err).ToNot(gomega.HaveOccurred())
	}()

	gomega.Expect(resp.StatusCode).Should(gomega.Equal(http.StatusOK))

	namespaceHeader := resp.Header.Get("x-inference-namespace")
	podHeader := resp.Header.Get("x-inference-pod")

	// Read streaming response and extract finish_reason from the last data chunk
	respBody, err := io.ReadAll(resp.Body)
	gomega.Expect(err).ShouldNot(gomega.HaveOccurred())

	finishReason := extractFinishReasonFromStreaming(string(respBody))

	ginkgo.By(fmt.Sprintf("Streaming Completion Response: ns=%s, pod=%s, finish_reason=%s", namespaceHeader, podHeader, finishReason))

	return namespaceHeader, podHeader, finishReason
}
