package memory

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMemoryBus(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()
	topic := "test-topic"

	var wg sync.WaitGroup
	wg.Add(1)

	var received string
	handler := func(ctx context.Context, payload []byte) error {
		received = string(payload)
		wg.Done()
		return nil
	}

	err := bus.Subscribe(ctx, topic, handler)
	assert.NoError(t, err)

	err = bus.Publish(ctx, topic, []byte("hello world"))
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		assert.Equal(t, "hello world", received)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}
