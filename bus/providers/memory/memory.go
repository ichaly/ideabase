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

	// 1. 获取所有匹配的订阅者快照
	my.mu.RLock()
	var snapshot []providers.Handler
	for pattern, handlers := range my.handlers {
		if providers.MatchTopic(pattern, topic) {
			snapshot = append(snapshot, handlers...)
		}
	}
	my.mu.RUnlock()

	if len(snapshot) == 0 {
		return nil
	}

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
