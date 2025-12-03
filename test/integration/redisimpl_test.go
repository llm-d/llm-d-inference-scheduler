package integration_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch"
	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch/redis"
)

const (
	redisURL = "localhost:6379"
)

func TestRedisImpl(t *testing.T) {
	ctx := context.Background()
	flow := redis.NewRedisMQFlow(redisURL)
	flow.Start(ctx)

	flow.RetryChannel() <- batch.RetryMessage{
		RequestMessage: batch.RequestMessage{
			Id:              "test-id",
			DeadlineUnixSec: strconv.FormatInt(time.Now().Add(time.Minute).Unix(), 10),
			Payload:         map[string]any{"model": "food-review", "prompt": "hi", "max_tokens": 10, "temperature": 0},
		},
		BackoffDurationSeconds: 2,
	}
	totalReqCount := 0
	for _, value := range flow.RequestChannels() {
		totalReqCount += len(value.Channel)
	}

	if totalReqCount > 0 {
		t.Errorf("Expected no messages in request channels yet")
		return
	}
	if len(flow.ResultChannel()) > 0 {
		t.Errorf("Expected no messages in result channel yet")
		return
	}
	time.Sleep(3 * time.Second)

	mergedChannel := batch.NewRandomRobinPolicy().MergeRequestChannels(flow.RequestChannels())

	select {
	case req := <-mergedChannel.Channel:
		if req.Id != "test-id" {
			t.Errorf("Expected message id to be test-id, got %s", req.Id)
		}
	case <-time.After(2 * time.Second):
		t.Errorf("Expected message in request channel after backoff")
	}

}
