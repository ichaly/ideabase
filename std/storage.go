package std

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/allegro/bigcache/v3"
	"github.com/eko/gocache/lib/v4/cache"
	"github.com/eko/gocache/lib/v4/store"
	sb "github.com/eko/gocache/store/bigcache/v4"
	sg "github.com/eko/gocache/store/go_cache/v4"
	sr "github.com/eko/gocache/store/redis/v4"
	gocache "github.com/patrickmn/go-cache"
	"github.com/redis/go-redis/v9"
)

func NewStorage(c *Config) (*cache.Cache[string], error) {
	var s store.StoreInterface
	switch strings.ToLower(c.Cache.Dialect) {
	case "redis":
		args := []interface{}{c.Cache.Host, c.Cache.Port}
		s = sr.NewRedis(redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%d", args...),
			Username: c.Cache.Username,
			Password: c.Cache.Password,
		}))
	case "bigcache":
		client, err := bigcache.New(context.Background(), bigcache.DefaultConfig(5*time.Minute))
		if err != nil {
			return nil, err
		}
		s = sb.NewBigcache(client)
	default:
		client := gocache.New(5*time.Minute, 10*time.Minute)
		s = sg.NewGoCache(client)
	}
	return cache.New[string](s), nil
}
