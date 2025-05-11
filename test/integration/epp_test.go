//go:build integration_tests
// +build integration_tests

package integration_test

import (
	"bytes"
	"fmt"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
)

type inferenceResponse struct {
	Choices []inferenceChoice `json:"choices"`
	Model   string            `json:"model"`
}

type inferenceChoice struct {
	Text string `json:"text"`
}

func TestEndpointPickerBasics(t *testing.T) {
	inferenceURL := fmt.Sprintf("%s/v1/completions", gatewayURL)
	jsonData := []byte(`{"model":"food-review","prompt":"hi","max_tokens":10,"temperature":0}`)

	t.Logf("sending POST to %s: %s", inferenceURL, jsonData)
	resp, err := http.Post(inferenceURL, "application/json", bytes.NewBuffer(jsonData))
	require.NoError(t, err)
	defer resp.Body.Close()

	// t.Logf("checking HTTP response from %s", inferenceURL)
	// require.Equal(t, http.StatusOK, resp.StatusCode)
	// respBytes, err := io.ReadAll(resp.Body)
	// require.NoError(t, err)

	// t.Log("checking HTTP response headers to verify endpoint-picker was called")
	// assert.Equal(t, "true", resp.Header.Get("x-went-into-resp-headers"))
	// assert.NotNil(t, resp.Header.Get("x-envoy-upstream-service-time"))

	// t.Logf("checking HTTP response body: %s", respBytes)
	// infResp := inferenceResponse{}
	// require.NoError(t, json.Unmarshal(respBytes, &infResp))
	// assert.Equal(t, "food-review", infResp.Model)
	// require.True(t, len(infResp.Choices) > 0)
	// assert.NotEmpty(t, infResp.Choices[0].Text)
}
