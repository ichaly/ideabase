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

type Handler = driver.Handler

// Transport 是底层传输契约，仅由 provider 实现、由 ioc/测试用于构造 *Bus。
// 业务代码不应直接持有或调用此类型，发布订阅一律走 Publish[T]/Subscribe[T]。
type Transport = driver.Driver

// Bus 业务层入口，发布/订阅走 Publish[T]/Subscribe[T]。
type Bus struct {
	d driver.Driver
}

func (my *Bus) Close() error { return my.d.Close() }

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

// New 由 ioc 调用实例化 Transport，业务代码不直接使用；*Bus 通过 NewBus 包装。
func New(rdb redis.UniversalClient, nc *nats.Conn, db *gorm.DB) (Transport, error) {
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
	return current.factory(conn)
}

// NewBus 把 ioc 提供的 Transport 包装为业务层 *Bus。
func NewBus(t Transport) *Bus {
	return &Bus{d: t}
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

// SubscribeRaw 绕过类型化解码，回调直接拿到字节载荷。
// 仅用于通配符主题或载荷形态不固定的场景；业务事件一律走 Subscribe[T]。
func SubscribeRaw(ctx context.Context, bus *Bus, topic string, handler Handler) error {
	return bus.d.Subscribe(ctx, topic, handler)
}

// Marshal 序列化 payload，供各 provider 复用。
func Marshal(payload any) ([]byte, error) {
	switch v := payload.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return utl.Marshal(payload)
	}
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
