// Package driver 定义 event bus 的底层传输契约，仅供 event 包及其
// provider 子包（memory/redis/nats/postgres）实现与使用。
// 业务代码不要直接引用本包类型，应通过 event.Publish[T] / event.Subscribe[T]
// 以及 *event.Bus 访问。
package driver

import "context"

// Handler 订阅回调，收到的是 provider 发来的原始字节载荷。
type Handler func(ctx context.Context, payload []byte) error

// Driver 是 bus 的底层实现契约，四个 provider 各自满足此接口。
type Driver interface {
	Publish(ctx context.Context, topic string, payload any) error
	Subscribe(ctx context.Context, topic string, handler Handler) error
	Close() error
}

// Factory 由 provider init 阶段注册，event.New 按激活 provider 调用。
type Factory func(conn any) (Driver, error)
