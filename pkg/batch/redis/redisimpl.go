package redis

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"

	"strconv"
	"time"

	"github.com/llm-d/llm-d-inference-scheduler/pkg/batch"
	"github.com/redis/go-redis/v9"

	"sigs.k8s.io/controller-runtime/pkg/log"
	logutil "sigs.k8s.io/gateway-api-inference-extension/pkg/epp/util/logging"
)

var (
	redisAddr = flag.String("redis.addr", "localhost:6379", "address of the Redis server")

	// TODO: support multiple request queues with metadata (for policy)
	requestQueueName = flag.String("redis.request-queue-name", "batch-queue", "name of the Redis queue for request messages")
	retryQueueName   = flag.String("redis.retry-queue-name", "batch-sortedset-retry", "name of the Redis sorted set for retry messages")
	resultQueueName  = flag.String("redis.result-queue-name", "batch-queue-result", "name of the Redis queue for result messages")
)

// TODO: think about what to do if Redis is down
type RedisMQFlow struct {
	rdb            *redis.Client
	requestChannel chan batch.RequestMessage
	retryChannel   chan batch.RetryMessage
	resultChannel  chan batch.ResultMessage
}

func NewRedisMQFlow() *RedisMQFlow {
	rdb := redis.NewClient(&redis.Options{
		Addr: *redisAddr,

		// TODO: check specific version of go-redis. might require higher version.
		// Explicitly disable maintenance notifications
		// This prevents the client from sending CLIENT MAINT_NOTIFICATIONS ON
		// import "github.com/redis/go-redis/v9/maintnotifications"
		// MaintNotificationsConfig: &maintnotifications.Config{
		// 	Mode: maintnotifications.ModeDisabled,
		// },
	})
	return &RedisMQFlow{
		rdb:            rdb,
		requestChannel: make(chan batch.RequestMessage),
		retryChannel:   make(chan batch.RetryMessage),
		resultChannel:  make(chan batch.ResultMessage),
	}
}

func (r *RedisMQFlow) Start(ctx context.Context) {
	go requestWorker(ctx, r.rdb, r.requestChannel, *requestQueueName)

	go addMsgToRetryWorker(ctx, r.rdb, r.retryChannel, *retryQueueName)

	go retryWorker(ctx, r.rdb, r.requestChannel)

	go resultWorker(ctx, r.rdb, r.resultChannel, *resultQueueName)
}
func (r *RedisMQFlow) RequestChannels() []batch.RequestChannel {
	return []batch.RequestChannel{{Channel: r.requestChannel, Metadata: map[string]any{}}}
}

func (r *RedisMQFlow) RetryChannel() chan batch.RetryMessage {
	return r.retryChannel
}

func (r *RedisMQFlow) ResultChannel() chan batch.ResultMessage {
	return r.resultChannel
}

// Listening on the results channel and responsible for writing results into Redis.
func resultWorker(ctx context.Context, rdb *redis.Client, resultChannel chan batch.ResultMessage, resultsQueueName string) {
	logger := log.FromContext(ctx)
	for {
		select {
		case <-ctx.Done():
			return

		case msg := <-resultChannel:
			bytes, err := json.Marshal(msg)
			var msgStr string
			if err != nil {
				msgStr = fmt.Sprintf(`{"id" : "%s", "error": "%s"}`, msg.Id, "Failed to marshal result to string")
			} else {
				msgStr = string(bytes)
			}
			err = publishRedis(ctx, rdb, resultsQueueName, msgStr)
			if err != nil {
				// Not going to retry here. Just log the error.
				logger.V(logutil.DEFAULT).Error(err, "Failed to publish result message to Redis")
			}
		}
	}
}

// pulls from Redis Queue and put in the request channel
func requestWorker(ctx context.Context, rdb *redis.Client, msgChannel chan batch.RequestMessage, queueName string) {
	sub := rdb.Subscribe(ctx, queueName)
	defer sub.Close()

	// redis.WithChannelSize(100) -- TODO: consider exposing to config
	ch := sub.Channel()
	for {
		select {
		case <-ctx.Done():
			return

		case rmsg := <-ch:
			var msg batch.RequestMessage

			err := json.Unmarshal([]byte(rmsg.Payload), &msg)
			if err != nil {
				// TODO: log failed to unmarshal message.
				fmt.Println(err)
				continue // skip this message

			}
			msgChannel <- msg
		}
	}

}

// Puts msgs from the retry channel into a Redis sorted-set with a duration Score.
func addMsgToRetryWorker(ctx context.Context, rdb *redis.Client, retryChannel chan batch.RetryMessage, sortedSetName string) error {
	for {
		select {
		case <-ctx.Done():
			return nil

		case msg := <-retryChannel:
			score := float64(time.Now().Unix()) + msg.BackoffDurationSeconds
			bytes, err := json.Marshal(msg.RequestMessage)
			if err != nil {
				fmt.Printf("Failed to marshal message for retry in Redis: %s", err.Error())
				continue // skip this message. TODO: log
			}
			err = rdb.ZAdd(ctx, sortedSetName, redis.Z{
				Score:  score,
				Member: string(bytes),
			}).Err()

			if err != nil {
				fmt.Printf("Failed to add message for retry in Redis: %s", err.Error())
				// TODO:
			}
		}
	}

}

// TODO
// Every second polls the sorted set and publishes the messages that need to be retried into the request queue
func retryWorker(ctx context.Context, rdb *redis.Client, msgChannel chan batch.RequestMessage) {
	for {
		select {
		case <-ctx.Done():
			return

		default:
			currentTimeSec := float64(time.Now().Unix())
			results, err := rdb.ZRangeByScore(ctx, *retryQueueName, &redis.ZRangeBy{
				Min: "0",
				Max: strconv.FormatFloat(currentTimeSec, 'f', -1, 64),
			}).Result()
			if err != nil {
				panic(err)
			}
			for _, msg := range results {
				var message batch.RequestMessage
				err := json.Unmarshal([]byte(msg), &message)
				if err != nil {
					fmt.Println(err)

				}
				err = rdb.ZRem(ctx, *retryQueueName, msg).Err()
				if err != nil {
					fmt.Println(err)

				}
				// TODO: Publish to request channel or directly to request queue in Redis?
				msgChannel <- message
			}
			time.Sleep(time.Second)
		}
	}

}

func publishRedis(ctx context.Context, rdb *redis.Client, channelId, msg string) error {
	logger := log.FromContext(ctx)
	err := rdb.Publish(ctx, channelId, msg).Err()
	if err != nil {
		logger.V(logutil.DEFAULT).Error(err, "Error publishing message:%s\n", err.Error())
		return err
	}
	return nil
}
