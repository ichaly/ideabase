package redis

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/ichaly/ideabase/bus/providers"
	"github.com/ichaly/ideabase/log"
	"github.com/redis/go-redis/v9"
)

// RedisBus 基于 Redis Pub/Sub 的通知总线
// 优化版：单链接多路复用
type RedisBus struct {
	rdb      *redis.Client
	pubsub   *redis.PubSub
	handlers map[string][]providers.Handler
	mu       sync.RWMutex
	once     sync.Once
}

func NewRedisBus(rdb *redis.Client) *RedisBus {
	return &RedisBus{
		rdb:      rdb,
		handlers: make(map[string][]providers.Handler),
	}
}

func (my *RedisBus) Publish(ctx context.Context, topic string, payload any) error {
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
	return my.rdb.Publish(ctx, topic, body).Err()
}

func (my *RedisBus) Subscribe(ctx context.Context, topic string, handler providers.Handler) error {
	my.mu.Lock()
	defer my.mu.Unlock()

	// 懒加载初始化 PubSub 连接
	my.once.Do(func() {
		my.pubsub = my.rdb.Subscribe(ctx)
		// 启动分发循环
		go my.dispatchLoop()
	})

	// 注册 Handler
	my.handlers[topic] = append(my.handlers[topic], handler)

	// 含通配符时使用 PSubscribe，否则 Subscribe
	if strings.Contains(topic, "*") {
		return my.pubsub.PSubscribe(ctx, topic)
	}
	return my.pubsub.Subscribe(ctx, topic)
}

func (my *RedisBus) dispatchLoop() {
	// 获取只读 Channel
	ch := my.pubsub.Channel()

	for msg := range ch {
		my.mu.RLock()
		// 精确匹配
		var activeHandlers []providers.Handler
		activeHandlers = append(activeHandlers, my.handlers[msg.Channel]...)
		// PSubscribe 的消息通过 Pattern 字段匹配
		if msg.Pattern != "" && msg.Pattern != msg.Channel {
			activeHandlers = append(activeHandlers, my.handlers[msg.Pattern]...)
		}
		my.mu.RUnlock()

		if len(activeHandlers) == 0 {
			continue
		}

		// 异步分发
		for _, h := range activeHandlers {
			go func(handler providers.Handler, payload string) {
				if err := handler(context.Background(), []byte(payload)); err != nil {
					log.Warn().Err(err).Str("topic", msg.Channel).Msg("RedisBus handler error")
				}
			}(h, msg.Payload)
		}
	}

	// 如果循环退出（连接关闭），记录日志
	log.Warn().Msg("RedisBus dispatch loop exited")
}

// Close 关闭连接
func (my *RedisBus) Close() error {
	if my.pubsub != nil {
		return my.pubsub.Close()
	}
	return nil
}
