/*
Copyright 2025 The Kubernetes Authors.

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

package runner

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRunnableGroup_AllSucceed(t *testing.T) {
	g := newRunnableGroup()
	ran := make([]string, 0, 2)
	ch := make(chan string, 2)

	g.Add("a", func(ctx context.Context) error { ch <- "a"; return nil })
	g.Add("b", func(ctx context.Context) error { ch <- "b"; return nil })

	err := g.Run(context.Background())
	require.NoError(t, err)
	close(ch)
	for s := range ch {
		ran = append(ran, s)
	}
	assert.Len(t, ran, 2)
}

func TestRunnableGroup_OneFailsWrapsName(t *testing.T) {
	g := newRunnableGroup()
	g.Add("failing", func(ctx context.Context) error {
		return errors.New("boom")
	})
	g.Add("ok", func(ctx context.Context) error { return nil })

	err := g.Run(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "failing")
	assert.Contains(t, err.Error(), "boom")
}

func TestRunnableGroup_OneFailsCancelsOthers(t *testing.T) {
	g := newRunnableGroup()
	cancelled := make(chan struct{})

	g.Add("failing", func(ctx context.Context) error {
		return errors.New("trigger cancel")
	})
	g.Add("waiter", func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		return ctx.Err()
	})

	_ = g.Run(context.Background())
	_, open := <-cancelled
	assert.False(t, open, "waiter goroutine should have been cancelled")
}
