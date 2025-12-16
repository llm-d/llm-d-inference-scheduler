package batch

import "context"

type Flow interface {
	// starts processing requests.
	Start(ctx context.Context)

	// returns the channel for requests. Implementation is responsible for populating this channel.
	RequestChannels() []RequestChannel
	// returns the channel that accepts messages to be retries with their backoff delay.
	RetryChannel() chan RetryMessage
	// returns the channel for storing the results.
	ResultChannel() chan ResultMessage
}

// TODO: how to handle retries here?
type RequestMergePolicy interface {
	MergeRequestChannels(channels []RequestChannel) RequestChannel
}

type RequestMessage struct {
	Id              string         `json:"id"`
	RetryCount      int            `json:"retry_count,omitempty"`
	DeadlineUnixSec string         `json:"deadline"`
	Payload         map[string]any `json:"payload"`
}

// TODO: decide about metadata
type RequestChannel struct {
	Channel  chan RequestMessage
	Metadata map[string]any
}

type RetryMessage struct {
	RequestMessage
	BackoffDurationSeconds float64
}

type ResultMessage struct {
	Id      string         `json:"id"`
	Payload map[string]any `json:"payload"`
}
