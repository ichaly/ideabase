package memory

import (
	"context"
	"runtime/debug"
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
		my.invoke(ctx, topic, body, h)
	}
	return nil
}

// invoke 单 handler 调用 + recover 兜底:
// handler panic 转 log.Error 含 stack,不传播到 publisher;
// handler 返 error 走 log.Warn tolerant;
// 一个 handler 失败不影响后续 handler 收到同一事件。
func (my *memoryEvent) invoke(ctx context.Context, topic string, body []byte, h driver.Handler) {
	defer func() {
		if r := recover(); r != nil {
			log.Error().
				Interface("panic", r).
				Str("topic", topic).
				Bytes("stack", debug.Stack()).
				Msg("memory event handler panic recovered")
		}
	}()
	if err := h(ctx, body); err != nil {
		log.Warn().Err(err).Str("topic", topic).Msg("memory event handler error")
	}
}

func (my *memoryEvent) Subscribe(_ context.Context, topic string, handler driver.Handler) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.handlers[topic] = append(my.handlers[topic], handler)
	return nil
}

func (my *memoryEvent) Close() error { return nil }
