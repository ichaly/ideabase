package memory

import (
	"context"
	"sync"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std/event"
)

// 使用: import _ "github.com/ichaly/ideabase/std/event/memory"
func init() {
	event.Register("memory", func(conn any) (event.Event, error) {
		return &memoryEvent{handlers: make(map[string][]event.Handler)}, nil
	})
}

type memoryEvent struct {
	handlers map[string][]event.Handler
	mu       sync.RWMutex
}

func (my *memoryEvent) Publish(_ context.Context, topic string, payload any) error {
	body, err := event.Marshal(payload)
	if err != nil {
		return err
	}
	my.mu.RLock()
	var snapshot []event.Handler
	for pattern, handlers := range my.handlers {
		if event.MatchTopic(pattern, topic) {
			snapshot = append(snapshot, handlers...)
		}
	}
	my.mu.RUnlock()
	for _, h := range snapshot {
		go func(handler event.Handler) {
			if err := handler(context.Background(), body); err != nil {
				log.Warn().Err(err).Str("topic", topic).Msg("memory event handler error")
			}
		}(h)
	}
	return nil
}

func (my *memoryEvent) Subscribe(_ context.Context, topic string, handler event.Handler) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.handlers[topic] = append(my.handlers[topic], handler)
	return nil
}

func (my *memoryEvent) Close() error { return nil }
