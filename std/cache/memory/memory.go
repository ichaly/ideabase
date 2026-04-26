package memory

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/dgraph-io/ristretto/v2"
	"github.com/ichaly/ideabase/std/cache"
)

// 使用: import _ "github.com/ichaly/ideabase/std/cache/memory"
func init() {
	cache.Register("memory", func(conn any) (cache.Cache, error) {
		c, err := ristretto.NewCache(&ristretto.Config[string, []byte]{
			NumCounters: 1e5, MaxCost: 1 << 30, BufferItems: 64,
			Cost: func(val []byte) int64 { return int64(len(val)) }, IgnoreInternalCost: true,
		})
		if err != nil {
			return nil, err
		}
		return &memoryCache{
			cache: c,
			tags:  &tagIndex{data: make(map[string]map[string]time.Time)},
			locks: &lockTable{data: make(map[string]lockEntry)},
		}, nil
	})
}

type memoryCache struct {
	cache *ristretto.Cache[string, []byte]
	tags  *tagIndex
	locks *lockTable
}

func (my *memoryCache) Get(_ context.Context, key string) ([]byte, error) {
	val, ok := my.cache.Get(key)
	if !ok {
		return nil, cache.ErrNotFound
	}
	return val, nil
}

func (my *memoryCache) Set(_ context.Context, key string, val []byte, ttl time.Duration, tags ...string) error {
	my.cache.SetWithTTL(key, val, 0, ttl)
	my.tags.add(tags, key, ttl)
	return nil
}

func (my *memoryCache) Del(_ context.Context, keys ...string) error {
	for _, key := range keys {
		my.cache.Del(key)
		my.tags.removeKey(key)
	}
	return nil
}

func (my *memoryCache) Flush(_ context.Context, tags ...string) error {
	for _, key := range my.tags.pop(tags) {
		my.cache.Del(key)
	}
	return nil
}

func (my *memoryCache) TryLock(_ context.Context, key, owner string, ttl time.Duration) (bool, error) {
	if owner == "" || ttl <= 0 {
		return false, errors.New("cache: TryLock requires non-empty owner and positive ttl")
	}
	return my.locks.tryAcquire(key, owner, ttl), nil
}

func (my *memoryCache) Unlock(_ context.Context, key, owner string) error {
	my.locks.release(key, owner)
	return nil
}

// lockEntry 一条锁记录；expires 用于 TTL 兜底，超过即视为过期可被抢占。
type lockEntry struct {
	owner   string
	expires time.Time
}

// lockTable 所有锁的集中表；单 mutex 保护，锁数量在系统级 mutex 量级（数十～数百），无需分片。
type lockTable struct {
	mu   sync.Mutex
	data map[string]lockEntry
}

func (my *lockTable) tryAcquire(key, owner string, ttl time.Duration) bool {
	my.mu.Lock()
	defer my.mu.Unlock()
	now := time.Now()
	if e, ok := my.data[key]; ok && e.expires.After(now) {
		return false
	}
	my.data[key] = lockEntry{owner: owner, expires: now.Add(ttl)}
	return true
}

func (my *lockTable) release(key, owner string) {
	my.mu.Lock()
	defer my.mu.Unlock()
	if e, ok := my.data[key]; ok && e.owner == owner {
		delete(my.data, key)
	}
}

// tagIndex — tag 到 key 的索引，记录过期时间用于清理
type tagIndex struct {
	mu   sync.Mutex
	data map[string]map[string]time.Time
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
	for tag, items := range my.data {
		delete(items, key)
		if len(items) == 0 {
			delete(my.data, tag)
		}
	}
}
