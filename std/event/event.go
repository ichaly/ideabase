package event

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nats-io/nats.go"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

type Handler func(ctx context.Context, payload []byte) error

type Event interface {
	Publish(ctx context.Context, topic string, payload any) error
	Subscribe(ctx context.Context, topic string, handler Handler) error
	Close() error
}

type factory func(conn any) (Event, error)

var current struct {
	name    string
	factory factory
}

func Register(name string, f factory) {
	if current.factory != nil {
		panic(fmt.Sprintf("event: multiple providers registered: %s and %s", current.name, name))
	}
	current.name = name
	current.factory = f
}

// New 创建 Event 实例，根据已注册 Provider 选择对应连接
func New(rdb redis.UniversalClient, nc *nats.Conn, db *gorm.DB) (Event, error) {
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

// Marshal 将 payload 序列化为 []byte，供所有 event provider 共用
func Marshal(payload any) ([]byte, error) {
	switch v := payload.(type) {
	case []byte:
		return v, nil
	case string:
		return []byte(v), nil
	default:
		return json.Marshal(payload)
	}
}

// MatchTopic 检查 topic 是否匹配 pattern（`*` 匹配一个冒号分隔段）
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
