package redis

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ichaly/ideabase/std/cache"
	goredis "github.com/redis/go-redis/v9"
)

// 使用: import _ "github.com/ichaly/ideabase/std/cache/redis"
func init() {
	cache.Register("redis", func(conn any) (cache.Cache, error) {
		rdb, ok := conn.(goredis.UniversalClient)
		if !ok {
			return nil, fmt.Errorf("cache/redis: requires redis.UniversalClient, got %T", conn)
		}
		return &redisCache{rdb: rdb}, nil
	})
}

type redisCache struct {
	rdb goredis.UniversalClient
}

func (my *redisCache) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := my.rdb.Get(ctx, key).Bytes()
	if errors.Is(err, goredis.Nil) {
		return nil, cache.ErrNotFound
	}
	return data, err
}

func (my *redisCache) Set(ctx context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	p := my.rdb.Pipeline()
	p.Set(ctx, key, val, ttl)
	for _, tag := range tags {
		tKey := "tag:" + tag
		p.SAdd(ctx, tKey, key)
		if ttl > 0 {
			p.Expire(ctx, tKey, ttl)
		}
	}
	_, err := p.Exec(ctx)
	return err
}

func (my *redisCache) Del(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	return my.rdb.Unlink(ctx, keys...).Err()
}

func (my *redisCache) TryLock(ctx context.Context, key, owner string, ttl time.Duration) (bool, error) {
	if owner == "" || ttl <= 0 {
		return false, errors.New("cache: TryLock requires non-empty owner and positive ttl")
	}
	return my.rdb.SetNX(ctx, key, owner, ttl).Result()
}

// unlockScript 原子 check-and-del：仅当当前 value 仍是 owner 时才删，避免误删别人续过的锁。
const unlockScript = `if redis.call("get", KEYS[1]) == ARGV[1] then return redis.call("del", KEYS[1]) else return 0 end`

func (my *redisCache) Unlock(ctx context.Context, key, owner string) error {
	return my.rdb.Eval(ctx, unlockScript, []string{key}, owner).Err()
}

func (my *redisCache) Flush(ctx context.Context, tags ...string) error {
	if len(tags) == 0 {
		return nil
	}
	// pipeline 批量获取所有 tag 的成员，避免逐标签 round-trip
	p := my.rdb.Pipeline()
	tagKeys := make([]string, len(tags))
	cmds := make([]*goredis.StringSliceCmd, len(tags))
	for i, tag := range tags {
		tagKeys[i] = "tag:" + tag
		cmds[i] = p.SMembers(ctx, tagKeys[i])
	}
	if _, err := p.Exec(ctx); err != nil && !errors.Is(err, goredis.Nil) {
		return err
	}
	keys := append([]string{}, tagKeys...)
	for _, cmd := range cmds {
		keys = append(keys, cmd.Val()...)
	}
	if len(keys) == 0 {
		return nil
	}
	return my.rdb.Unlink(ctx, keys...).Err()
}
