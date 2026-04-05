package memory

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestMemoryBus(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(1)

	var received string
	err := bus.Subscribe(ctx, "test-topic", func(ctx context.Context, payload []byte) error {
		received = string(payload)
		wg.Done()
		return nil
	})
	assert.NoError(t, err)

	err = bus.Publish(ctx, "test-topic", []byte("hello world"))
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		assert.Equal(t, "hello world", received)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for message")
	}
}

func TestMemoryBus_WildcardSuffix(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(1)

	var received string
	err := bus.Subscribe(ctx, "cms:content:*", func(ctx context.Context, payload []byte) error {
		received = string(payload)
		wg.Done()
		return nil
	})
	assert.NoError(t, err)

	// 匹配
	err = bus.Publish(ctx, "cms:content:like", []byte("matched"))
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		assert.Equal(t, "matched", received)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for wildcard message")
	}
}

func TestMemoryBus_WildcardNoMatch(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	var called atomic.Int32
	err := bus.Subscribe(ctx, "cms:content:*", func(ctx context.Context, payload []byte) error {
		called.Add(1)
		return nil
	})
	assert.NoError(t, err)

	// 不同前缀，不匹配
	err = bus.Publish(ctx, "cms:comment:create", []byte("nope"))
	assert.NoError(t, err)

	// 多层段，不匹配
	err = bus.Publish(ctx, "cms:content:sub:deep", []byte("nope"))
	assert.NoError(t, err)

	time.Sleep(100 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load())
}

func TestMemoryBus_WildcardMiddle(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)

	var mu sync.Mutex
	var topics []string
	handler := func(ctx context.Context, payload []byte) error {
		mu.Lock()
		topics = append(topics, string(payload))
		mu.Unlock()
		wg.Done()
		return nil
	}

	err := bus.Subscribe(ctx, "cms:*:like", handler)
	assert.NoError(t, err)

	err = bus.Publish(ctx, "cms:content:like", []byte("content"))
	assert.NoError(t, err)
	err = bus.Publish(ctx, "cms:comment:like", []byte("comment"))
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		mu.Lock()
		assert.ElementsMatch(t, []string{"content", "comment"}, topics)
		mu.Unlock()
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for wildcard middle messages")
	}
}

func TestMemoryBus_MultiplePatterns(t *testing.T) {
	bus := NewMemoryBus()
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(2)

	var exactReceived, wildcardReceived string
	err := bus.Subscribe(ctx, "cms:content:like", func(ctx context.Context, payload []byte) error {
		exactReceived = string(payload)
		wg.Done()
		return nil
	})
	assert.NoError(t, err)

	err = bus.Subscribe(ctx, "cms:content:*", func(ctx context.Context, payload []byte) error {
		wildcardReceived = string(payload)
		wg.Done()
		return nil
	})
	assert.NoError(t, err)

	err = bus.Publish(ctx, "cms:content:like", []byte("both"))
	assert.NoError(t, err)

	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
		assert.Equal(t, "both", exactReceived)
		assert.Equal(t, "both", wildcardReceived)
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for multiple pattern messages")
	}
}
