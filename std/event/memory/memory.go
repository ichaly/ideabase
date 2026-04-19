package memory

import (
	"context"
	"sync"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std/event"
	"github.com/ichaly/ideabase/std/event/internal/driver"
	"github.com/ichaly/ideabase/utl"
)

// Package memory 提供 in-process 同步分发的事件 bus。
// 需要异步请用 nats/redis provider，或在 handler 内自行 `go func`。
// 使用: import _ "github.com/ichaly/ideabase/std/event/memory"
func init() {
	event.Register("memory", func(conn any) (driver.Driver, error) {
		return &memoryEvent{handlers: make(map[string][]driver.Handler)}, nil
	})
}

type memoryEvent struct {
	handlers map[string][]driver.Handler
	mu       sync.RWMutex
}

func (my *memoryEvent) Publish(ctx context.Context, topic string, payload any) error {
	body, err := utl.Marshal(payload)
	if err != nil {
		return err
	}
	my.mu.RLock()
	var snapshot []driver.Handler
	for pattern, handlers := range my.handlers {
		if driver.MatchTopic(pattern, topic) {
			snapshot = append(snapshot, handlers...)
		}
	}
	my.mu.RUnlock()
	for _, h := range snapshot {
		if err := h(ctx, body); err != nil {
			log.Warn().Err(err).Str("topic", topic).Msg("memory event handler error")
		}
	}
	return nil
}

func (my *memoryEvent) Subscribe(_ context.Context, topic string, handler driver.Handler) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.handlers[topic] = append(my.handlers[topic], handler)
	return nil
}

func (my *memoryEvent) Close() error { return nil }
