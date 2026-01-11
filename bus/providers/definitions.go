package providers

import "context"

// Handler 消息处理函数
type Handler func(ctx context.Context, payload []byte) error

// Bus 通用通知总线接口
// 设计原则：简单、通用，支持多种底层实现（Redis, Postgres, NATS, Memory）
type Bus interface {
	// Publish 发布消息
	// topic: 主题/频道
	// payload: 消息内容（支持 struct，并通过 json 序列化，或直接 []byte）
	Publish(ctx context.Context, topic string, payload any) error

	// Subscribe 订阅消息
	// topic: 主题/频道
	// handler: 处理函数
	Subscribe(ctx context.Context, topic string, handler Handler) error
}
