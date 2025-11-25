package batch

import (
	"testing"
)

func TestProcessAllChannels(t *testing.T) {
	msgsPerChannel := 5
	channels := []RequestChannel{
		{Channel: make(chan RequestMessage, msgsPerChannel), Metadata: map[string]any{}},
		{Channel: make(chan RequestMessage, msgsPerChannel), Metadata: map[string]any{}},
		{Channel: make(chan RequestMessage, msgsPerChannel), Metadata: map[string]any{}},
	}
	policy := NewRandomRobinPolicy()

	// Send messages to each channel
	for i, ch := range channels {
		for range msgsPerChannel {
			ch.Channel <- RequestMessage{Id: string(rune('A' + i))}
		}
	}
	mergedChannel := policy.MergeRequestChannels(channels).Channel
	close(channels[0].Channel)
	close(channels[1].Channel)
	close(channels[2].Channel)

	counts := map[string]int{}
	totalMessages := msgsPerChannel * 3
	for range totalMessages {
		msg := <-mergedChannel
		counts[msg.Id]++

	}

	for i := range 3 {
		id := string(rune('A' + i))
		if counts[id] != msgsPerChannel {
			t.Errorf("Expected %d messages from channel %s, got %d", msgsPerChannel, id, counts[id])
		}
	}
}
