package redis

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std/event"
	"github.com/ichaly/ideabase/std/event/internal/driver"
	"github.com/ichaly/ideabase/utl"
	goredis "github.com/redis/go-redis/v9"
)

// 使用: import _ "github.com/ichaly/ideabase/std/event/redis"
func init() {
	event.Register("redis", func(conn any) (driver.Driver, error) {
		rdb, ok := conn.(goredis.UniversalClient)
		if !ok {
			return nil, fmt.Errorf("event/redis: requires redis.UniversalClient, got %T", conn)
		}
		return &redisEvent{rdb: rdb, handlers: make(map[string][]driver.Handler)}, nil
	})
}

type redisEvent struct {
	rdb      goredis.UniversalClient
	pubsub   *goredis.PubSub
	handlers map[string][]driver.Handler
	mu       sync.RWMutex
	once     sync.Once
}

func (my *redisEvent) Publish(ctx context.Context, topic string, payload any) error {
	body, err := utl.Marshal(payload)
	if err != nil {
		return err
	}
	return my.rdb.Publish(ctx, topic, body).Err()
}

func (my *redisEvent) Subscribe(ctx context.Context, topic string, handler driver.Handler) error {
	my.mu.Lock()
	defer my.mu.Unlock()
	my.once.Do(func() {
		my.pubsub = my.rdb.Subscribe(ctx)
		go my.dispatch()
	})
	my.handlers[topic] = append(my.handlers[topic], handler)
	if strings.Contains(topic, "*") {
		return my.pubsub.PSubscribe(ctx, topic)
	}
	return my.pubsub.Subscribe(ctx, topic)
}

func (my *redisEvent) Close() error {
	if my.pubsub != nil {
		return my.pubsub.Close()
	}
	return nil
}

func (my *redisEvent) dispatch() {
	ch := my.pubsub.Channel()
	for msg := range ch {
		my.mu.RLock()
		var active []driver.Handler
		active = append(active, my.handlers[msg.Channel]...)
		if msg.Pattern != "" && msg.Pattern != msg.Channel {
			active = append(active, my.handlers[msg.Pattern]...)
		}
		my.mu.RUnlock()
		for _, h := range active {
			go func(handler driver.Handler, data string) {
				if err := handler(context.Background(), []byte(data)); err != nil {
					log.Warn().Err(err).Str("topic", msg.Channel).Msg("redis event handler error")
				}
			}(h, msg.Payload)
		}
	}
}
