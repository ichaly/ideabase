package std

import (
	"context"
	"errors"
	"fmt"
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

func NewStorage(c *Config) (*Storage, error) {
	if c == nil || c.Cache == nil {
		return newRistrettoStorage()
	}

	switch strings.ToLower(c.Cache.Dialect) {
	case "redis":
		args := []interface{}{c.Cache.Host, c.Cache.Port}
		client := redis.NewClient(&redis.Options{
			Addr:     fmt.Sprintf("%s:%d", args...),
			Username: c.Cache.Username,
			Password: c.Cache.Password,
		})
		return &Storage{backend: &redisBackend{client: client, prefix: c.Cache.Name}}, nil
	default:
		return newRistrettoStorage()
	}
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

func (my *Storage) Delete(ctx context.Context, key string) error {
	return my.backend.delete(ctx, key)
}

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
	for _, key := range my.tags.pop(tags) {
		my.cache.Del(key)
	}
	return nil
}

type redisBackend struct {
	client *redis.Client
	prefix string
}

func (my *redisBackend) get(ctx context.Context, key string) ([]byte, bool, error) {
	data, err := my.client.Get(ctx, my.key(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return data, true, nil
}

func (my *redisBackend) delete(ctx context.Context, key string) error {
	deleted, err := my.client.Del(ctx, my.key(key)).Result()
	if err != nil {
		return err
	}
	if deleted == 0 {
		return ErrCacheMiss
	}
	return nil
}

func (my *redisBackend) set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	p := my.client.Pipeline()
	p.Set(ctx, my.key(key), value, ttl)
	for _, tag := range tags {
		tKey := my.tagKey(tag)
		p.SAdd(ctx, tKey, my.key(key))
		if ttl > 0 {
			p.Expire(ctx, tKey, ttl)
		}
	}
	_, err := p.Exec(ctx)
	return err
}

func (my *redisBackend) invalidate(ctx context.Context, tags []string) error {
	for _, tag := range tags {
		tagKey := my.tagKey(tag)
		keys, err := my.client.SMembers(ctx, tagKey).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		if len(keys) > 0 {
			if err := my.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		if err := my.client.Del(ctx, tagKey).Err(); err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
	}
	return nil
}

func (my *redisBackend) key(key string) string {
	if my.prefix == "" {
		return key
	}
	return fmt.Sprintf("%s:%s", my.prefix, key)
}

func (my *redisBackend) tagKey(tag string) string {
	return my.key(fmt.Sprintf("tag:%s", tag))
}

type tagIndex struct {
	mu   sync.Mutex
	data map[string]map[string]time.Time
}

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
	var result []string
	for _, tag := range tags {
		items := my.data[tag]
		for key, exp := range items {
			if exp.IsZero() || now.Before(exp) {
				result = append(result, key)
			}
		}
		delete(my.data, tag)
	}
	return result
}

func (my *tagIndex) removeKey(key string) {
	my.mu.Lock()
	defer my.mu.Unlock()

	for tag, items := range my.data {
		delete(items, key)
		if len(items) == 0 {
			delete(my.data, tag)
		}
	}
}
