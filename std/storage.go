package std

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto/v2"
	"github.com/redis/go-redis/v9"
)

var (
	ErrCacheMiss   = errors.New("cache: key not found")
	errSetRejected = errors.New("cache: entry rejected")
)

type (
	// Storage 对外暴露的缓存入口，内部委托给不同的后端实现
	Storage struct {
		backend cacheBackend
	}
	cacheBackend interface {
		get(ctx context.Context, key string) ([]byte, bool, error)
		delete(ctx context.Context, key string) error
		set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error
		invalidate(ctx context.Context, tags []string) error
	}
	cacheOptions struct {
		expiration     time.Duration
		tags           []string
		invalidateTags []string
	}
	Option func(*cacheOptions)
)

// NewStorage 按配置选择内存或 Redis 缓存，并初始化后端
func NewStorage(c *Config) (*Storage, error) {
	if c == nil || c.Cache == nil || strings.ToLower(c.Cache.Dialect) != "redis" {
		return newRistrettoStorage()
	}

	db := 0
	if c.Cache.Name != "" {
		var err error
		if db, err = strconv.Atoi(c.Cache.Name); err != nil {
			return nil, err
		}
	}

	client := redis.NewClient(&redis.Options{
		Addr:     fmt.Sprintf("%s:%d", c.Cache.Host, c.Cache.Port),
		Username: c.Cache.Username, Password: c.Cache.Password, DB: db,
	})
	return &Storage{backend: &redisBackend{client: client}}, nil
}

func (my *Storage) Get(ctx context.Context, key string) ([]byte, error) {
	val, ok, err := my.backend.get(ctx, key)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrCacheMiss
	}
	return val, nil
}

func (my *Storage) Set(ctx context.Context, key string, value []byte, opts ...Option) error {
	option := applyOptions(opts...)
	return my.backend.set(ctx, key, value, option.expiration, option.tags)
}

// Delete 直接移除指定 key，对应标签关系也会被同步清理（仅单点删除）
func (my *Storage) Delete(ctx context.Context, key string) error {
	return my.backend.delete(ctx, key)
}

// Invalidate 根据标签批量失效（常用于数据更新后整体缓存刷新）
func (my *Storage) Invalidate(ctx context.Context, opts ...Option) error {
	option := applyOptions(opts...)
	if len(option.invalidateTags) == 0 {
		return nil
	}
	return my.backend.invalidate(ctx, option.invalidateTags)
}

func WithExpiration(exp time.Duration) Option {
	return func(opt *cacheOptions) {
		opt.expiration = exp
	}
}

func WithTags(tags []string) Option {
	return func(opt *cacheOptions) {
		opt.tags = append(opt.tags, tags...)
	}
}

func WithInvalidateTags(tags []string) Option {
	return func(opt *cacheOptions) {
		opt.invalidateTags = append(opt.invalidateTags, tags...)
	}
}

func applyOptions(opts ...Option) cacheOptions {
	opt := cacheOptions{}
	for _, fn := range opts {
		if fn != nil {
			fn(&opt)
		}
	}
	return opt
}

type ristrettoBackend struct {
	cache *ristretto.Cache[string, []byte]
	tags  *tagIndex
}

// newRistrettoStorage 创建基于 ristretto 的本地缓存（带标签索引）
func newRistrettoStorage() (*Storage, error) {
	cache, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
		NumCounters:        1e5,     // ~100k keys
		MaxCost:            1 << 30, // ~1GB
		BufferItems:        64,
		Cost:               func(value []byte) int64 { return int64(len(value)) },
		IgnoreInternalCost: true,
	})
	if err != nil {
		return nil, err
	}
	return &Storage{
		backend: &ristrettoBackend{
			cache: cache,
			tags:  newTagIndex(),
		},
	}, nil
}

func (my *ristrettoBackend) get(_ context.Context, key string) ([]byte, bool, error) {
	val, ok := my.cache.Get(key)
	if !ok {
		return nil, false, nil
	}
	return val, true, nil
}

func (my *ristrettoBackend) set(_ context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	if ok := my.cache.SetWithTTL(key, value, 0, ttl); !ok {
		return errSetRejected
	}
	// 等待写入完成，避免紧接着的读取出现未命中的情况
	my.cache.Wait()
	my.tags.add(tags, key, ttl)
	return nil
}

func (my *ristrettoBackend) delete(_ context.Context, key string) error {
	if _, ok := my.cache.Get(key); !ok {
		return ErrCacheMiss
	}
	my.cache.Del(key)
	my.tags.removeKey(key)
	return nil
}

func (my *ristrettoBackend) invalidate(_ context.Context, tags []string) error {
	// 按标签批量取出相关 key，再逐个删除
	for _, key := range my.tags.pop(tags) {
		my.cache.Del(key)
	}
	return nil
}

type redisBackend struct{ client *redis.Client }

// get 读取单键，区分未命中和错误
func (my *redisBackend) get(ctx context.Context, key string) ([]byte, bool, error) {
	data, err := my.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (my *redisBackend) delete(ctx context.Context, key string) error {
	deleted, err := my.client.Unlink(ctx, key).Result()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrCacheMiss
	}
	return nil
}

func (my *redisBackend) set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	// 管道写入主键并维护标签集合
	p := my.client.Pipeline()
	p.Set(ctx, key, value, ttl)
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

func (my *redisBackend) invalidate(ctx context.Context, tags []string) error {
	// 收集标签集合及其成员，一次 UNLINK
	if len(tags) == 0 {
		return nil
	}
	var keys []string
	for _, tag := range tags {
		tagKey := "tag:" + tag
		keys = append(keys, tagKey)
		tagged, err := my.client.SMembers(ctx, tagKey).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		keys = append(keys, tagged...)
	}
	if len(keys) == 0 {
		return nil
	}
	return my.client.Unlink(ctx, keys...).Err()
}

type tagIndex struct {
	mu   sync.Mutex
	data map[string]map[string]time.Time
}

// newTagIndex 构建标签到 key 的索引（记录过期时间用于清理）
func newTagIndex() *tagIndex {
	return &tagIndex{data: make(map[string]map[string]time.Time)}
}

func (my *tagIndex) add(tags []string, key string, ttl time.Duration) {
	if len(tags) == 0 {
		return
	}
	my.mu.Lock()
	defer my.mu.Unlock()

	expire := time.Time{}
	if ttl > 0 {
		expire = time.Now().Add(ttl)
	}
	for _, tag := range tags {
		items := my.data[tag]
		if items == nil {
			items = make(map[string]time.Time)
			my.data[tag] = items
		}
		items[key] = expire
	}
}

func (my *tagIndex) pop(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	my.mu.Lock()
	defer my.mu.Unlock()

	now := time.Now()
	seen := make(map[string]struct{})
	var result []string
	for _, tag := range tags {
		items, ok := my.data[tag]
		if !ok {
			continue
		}
		for key, exp := range items {
			if !exp.IsZero() && now.After(exp) {
				continue
			}
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			result = append(result, key)
		}
		delete(my.data, tag)
	}
	return result
}

func (my *tagIndex) removeKey(key string) {
	my.mu.Lock()
	defer my.mu.Unlock()

	// 移除单 key 与所有标签的绑定，清空空标签
	for tag, items := range my.data {
		delete(items, key)
		if len(items) == 0 {
			delete(my.data, tag)
		}
	}
}
