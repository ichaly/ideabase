package cache

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

var ErrNotFound = errors.New("cache: key not found")

type Cache interface {
	Get(ctx context.Context, key string) ([]byte, error)
	Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error
	Del(ctx context.Context, keys ...string) error
	Flush(ctx context.Context, tags ...string) error

	// TryLock 尝试获取一个 TTL 边界的互斥锁。
	//   - 成功返回 true，被他人持有返回 (false, nil)。
	//   - owner 是调用方提供的不透明 token，必须非空；Unlock 时需用同样的 token。
	//   - ttl 必须 > 0：进程崩溃 / 漏调 Unlock 时靠 TTL 兜底自动释放。
	//
	// 后端语义差异：
	//   - memory：进程内互斥（与 sync.Mutex 同量级）；多实例部署不构成跨进程锁。
	//   - redis：跨实例分布式锁（SETNX + 原子 check-and-del 释放）。
	TryLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error)

	// Unlock 释放 owner 持有的锁；owner 不匹配时静默忽略（防止误删别人的锁）。
	Unlock(ctx context.Context, key, owner string) error
}

type factory func(conn any) (Cache, error)

var current struct {
	name    string
	factory factory
}

func Register(name string, f factory) {
	if current.factory != nil {
		panic(fmt.Sprintf("cache: multiple providers registered: %s and %s", current.name, name))
	}
	current.name = name
	current.factory = f
}

// New 创建 Cache 实例，rdb 为 nil 时 memory provider 忽略它
func New(rdb redis.UniversalClient) (Cache, error) {
	if current.factory == nil {
		return nil, fmt.Errorf("cache: no provider registered, import a provider package")
	}
	return current.factory(rdb)
}
