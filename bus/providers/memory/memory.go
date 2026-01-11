package memory

import (
	"context"
	"encoding/json"
	"sync"

	"github.com/ichaly/ideabase/bus/providers"
	"github.com/ichaly/ideabase/log"
)

// MemoryBus 本地内存实现的 Bus
// 生产级可用：使用 RWMutex 保证并发安全
type MemoryBus struct {
	handlers map[string][]providers.Handler
	mu       sync.RWMutex
}

func NewMemoryBus() *MemoryBus {
	return &MemoryBus{
		handlers: make(map[string][]providers.Handler),
	}
}

func (my *MemoryBus) Publish(ctx context.Context, topic string, payload any) error {
	var body []byte
	var err error
	if v, ok := payload.([]byte); ok {
		body = v
	} else if v, ok := payload.(string); ok {
		body = []byte(v)
	} else {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}

	// 1. 获取订阅者快照 (避免在锁内执行耗时操作)
	my.mu.RLock()
	handlers, ok := my.handlers[topic]
	if !ok || len(handlers) == 0 {
		my.mu.RUnlock()
		return nil
	}

	snapshot := make([]providers.Handler, len(handlers))
	copy(snapshot, handlers)
	my.mu.RUnlock()

	// 2. 异步执行
	go func() {
		for _, handler := range snapshot {
			go func(h providers.Handler) {
				if err := h(context.Background(), body); err != nil {
					log.Warn().Err(err).Str("topic", topic).Msg("MemoryBus handler error")
				}
			}(handler)
		}
	}()
	return nil
}

func (my *MemoryBus) Subscribe(ctx context.Context, topic string, handler providers.Handler) error {
	my.mu.Lock()
	defer my.mu.Unlock()

	my.handlers[topic] = append(my.handlers[topic], handler)
	return nil
}
