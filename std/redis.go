package std

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/redis/go-redis/v9"
)

// NewRedis 根据 Config 中的 Redis URL 创建连接
// 支持 redis:// (单机), redis-cluster:// (集群), redis-sentinel:// (哨兵)
func NewRedis(c *Config) (redis.UniversalClient, error) {
	if c.Redis == "" {
		return nil, nil
	}
	u, err := url.Parse(c.Redis)
	if err != nil {
		return nil, fmt.Errorf("redis: invalid url: %w", err)
	}
	pass, _ := u.User.Password()
	switch u.Scheme {
	case "redis":
		opt, err := redis.ParseURL(c.Redis)
		if err != nil {
			return nil, err
		}
		return redis.NewClient(opt), nil
	case "redis-cluster":
		return redis.NewClusterClient(&redis.ClusterOptions{
			Addrs:    strings.Split(u.Host, ","),
			Password: pass,
		}), nil
	case "redis-sentinel":
		master := strings.TrimPrefix(u.Path, "/")
		return redis.NewFailoverClient(&redis.FailoverOptions{
			MasterName:    master,
			SentinelAddrs: strings.Split(u.Host, ","),
			Password:      pass,
		}), nil
	default:
		return nil, fmt.Errorf("redis: unsupported scheme %q", u.Scheme)
	}
}
