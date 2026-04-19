package event

import (
	"context"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std/event/internal/driver"
	"github.com/ichaly/ideabase/utl"
	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Bus 业务层入口，发布/订阅走 Publish[T]/Subscribe[T]。
type Bus struct {
	d driver.Driver
}

var current struct {
	name    string
	factory driver.Factory
}

// Register 注册 provider 工厂，重复注册 panic。
func Register(name string, f driver.Factory) {
	if current.factory != nil {
		panic(fmt.Sprintf("event: multiple providers registered: %s and %s", current.name, name))
	}
	current.name = name
	current.factory = f
}

// New 根据当前激活的 provider 实例化底层 driver 并包装为 *Bus。
// 测试可 blank import std/event/memory 后传 nil 连接以获得 in-process *Bus。
func New(rdb redis.UniversalClient, nc *nats.Conn, db *gorm.DB) (*Bus, error) {
	if current.factory == nil {
		return nil, fmt.Errorf("event: no provider registered, import a provider package")
	}
	var conn any
	switch current.name {
	case "nats":
		conn = nc
	case "redis":
		conn = rdb
	case "postgres":
		conn = db
	}
	d, err := current.factory(conn)
	if err != nil {
		return nil, err
	}
	return &Bus{d: d}, nil
}

// Publish 类型化发布。
func Publish[T any](ctx context.Context, bus *Bus, topic string, payload T) error {
	return bus.d.Publish(ctx, topic, payload)
}

// Subscribe 类型化订阅，字节载荷自动按 T 反序列化。
// 反序列化失败打日志丢弃，不回传 driver，避免坏载荷阻塞业务总线（R12 语义）。
func Subscribe[T any](ctx context.Context, bus *Bus, topic string, handler func(context.Context, T) error) error {
	return bus.d.Subscribe(ctx, topic, func(c context.Context, data []byte) error {
		var payload T
		if err := utl.Unmarshal(data, &payload); err != nil {
			log.Error().Err(err).Str("topic", topic).Msg("event: drop malformed payload")
			return nil
		}
		return handler(c, payload)
	})
}

// Marshal 序列化 payload，供各 provider 复用；
// 与 Subscribe[T] 的 utl.Unmarshal 对称，统一走 JSON。
func Marshal(payload any) ([]byte, error) {
	return utl.Marshal(payload)
}

// MatchTopic 检查 topic 是否匹配 pattern（`*` 匹配一个冒号分隔段）。
func MatchTopic(pattern, topic string) bool {
	if pattern == topic {
		return true
	}
	pp := strings.Split(pattern, ":")
	tp := strings.Split(topic, ":")
	if len(pp) != len(tp) {
		return false
	}
	for i := range pp {
		if pp[i] != "*" && pp[i] != tp[i] {
			return false
		}
	}
	return true
}
