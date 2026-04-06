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
