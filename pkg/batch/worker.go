package batch

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net/http"
	"strconv"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

var baseDelaySeconds = 2

func Worker(ctx context.Context, endpoint string, inferenceObjective string, httpClient *http.Client, requestChannel chan RequestMessage,
	retryChannel chan RetryMessage, resultChannel chan ResultMessage) {

	logger := log.FromContext(ctx)
	for {
		select {
		case <-ctx.Done():
			logger.V(logutil.DEFAULT).Info("Worker finishing.")
			return
		case msg := <-requestChannel:
			if msg.RetryCount == 0 {
				// Only count first attempt as a new request.
				metrics.BatchReqs.Inc()
			}
			payloadBytes := parseAndValidateRequest(resultChannel, msg)
			if payloadBytes == nil {
				continue
			}

			sendInferenceRequest := func() {
				logger.V(logutil.DEBUG).Info("Sending inference request.")
				request, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewBuffer(payloadBytes))
				if err != nil {
					metrics.FailedReqs.Inc()
					resultChannel <- CreateErrorResultMessage(msg.Id, fmt.Sprintf("Failed to create request to inference: %s", err.Error()))
					return
				}
				request.Header.Set("Content-Type", "application/json")
				if inferenceObjective != "" {
					request.Header.Set("x-gateway-inference-objective", inferenceObjective)
				}

				result, err := httpClient.Do(request)
				if err != nil {
					metrics.FailedReqs.Inc()
					resultChannel <- CreateErrorResultMessage(msg.Id, fmt.Sprintf("Failed to send request to inference: %s", err.Error()))
					return
				}
				defer result.Body.Close()
				// Retrying on too many requests or any server-side error.
				if result.StatusCode == 429 || result.StatusCode >= 500 && result.StatusCode < 600 {
					if result.StatusCode == 429 {
						metrics.SheddedRequests.Inc()
					}
					retryMessage(msg, retryChannel, resultChannel)
				} else {
					payloadBytes, err := io.ReadAll(result.Body)
					if err != nil {
						// Retrying on IO-read error as well.
						retryMessage(msg, retryChannel, resultChannel)
					} else {
						var resultPayload map[string]any
						err := json.Unmarshal(payloadBytes, &resultPayload)
						if err != nil {
							// Not retrying on unmarshalling error.
							metrics.FailedReqs.Inc()
							resultChannel <- CreateErrorResultMessage(msg.Id, fmt.Sprintf("Failed to unmarshal inference result payload: %v", err))
							return
						}
						metrics.SuccessfulReqs.Inc()
						resultChannel <- ResultMessage{
							Id:      msg.Id,
							Payload: resultPayload,
						}
					}
				}
			}
			sendInferenceRequest()
		}
	}
}
func parseAndValidateRequest(resultChannel chan ResultMessage, msg RequestMessage) []byte {
	deadline, err := strconv.ParseInt(msg.DeadlineUnixSec, 10, 64)
	if err != nil {
		metrics.FailedReqs.Inc()
		resultChannel <- CreateErrorResultMessage(msg.Id, "Failed to parse deadline, should be in Unix seconds.")
		return nil
	}

	if deadline < time.Now().Unix() {
		metrics.ExceededDeadlineReqs.Inc()
		resultChannel <- CreateDeadlineExceededResultMessage(msg.Id)
		return nil
	}

	payloadBytes, err := json.Marshal(msg.Payload)
	if err != nil {
		metrics.FailedReqs.Inc()
		resultChannel <- CreateErrorResultMessage(msg.Id, fmt.Sprintf("Failed to marshal message's payload: %s", err.Error()))
		return nil
	}
	return payloadBytes
}

// If it is not after deadline, just publish again.
func retryMessage(msg RequestMessage, retryChannel chan RetryMessage, resultChannel chan ResultMessage) {
	deadline, err := strconv.ParseInt(msg.DeadlineUnixSec, 10, 64)
	if err != nil { // Can't really happen because this was already parsed in the past. But we don't care to have this branch.
		resultChannel <- CreateErrorResultMessage(msg.Id, "Failed to parse deadline. Should be in Unix time")
		return
	}
	secondsToDeadline := deadline - time.Now().Unix()
	if secondsToDeadline < 0 {
		metrics.ExceededDeadlineReqs.Inc()
		resultChannel <- CreateDeadlineExceededResultMessage(msg.Id)
	} else {
		msg.RetryCount++
		backoffDurationSeconds := math.Min(
			float64(baseDelaySeconds)*(math.Pow(2, float64(msg.RetryCount))),
			float64(secondsToDeadline))

		jitter := rand.Float64() - 0.5
		finalDuration := backoffDurationSeconds + jitter
		if finalDuration < 0 {
			finalDuration = 0
		}
		metrics.Retries.Inc()
		retryChannel <- RetryMessage{
			RequestMessage:         msg,
			BackoffDurationSeconds: finalDuration,
		}

	}

}
func CreateErrorResultMessage(id string, errMsg string) ResultMessage {
	return ResultMessage{
		Id:      id,
		Payload: map[string]any{"error": errMsg},
	}
}

func CreateDeadlineExceededResultMessage(id string) ResultMessage {
	return CreateErrorResultMessage(id, "deadline exceeded")
}
