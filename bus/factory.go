package bus

import (
	"errors"

	"github.com/ichaly/ideabase/bus/providers"
	"github.com/ichaly/ideabase/bus/providers/memory"
	"github.com/ichaly/ideabase/bus/providers/nats"
	"github.com/ichaly/ideabase/bus/providers/postgres"
	"github.com/ichaly/ideabase/bus/providers/redis"
	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std"
	rpk "github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// NewBus 根据配置创建 Bus 实例
// 优先级：配置 > 自动探测
// rdb 和 db 是可选的，如果配置了 redis 但 rdb 为 nil，则降级为 memory，同理 postgres
func NewBus(k *std.Konfig, rdb *rpk.Client, db *gorm.DB) (providers.Bus, error) {
	var cfg struct {
		Driver string `mapstructure:"driver"`
		URL    string `mapstructure:"url"`
	}
	if err := k.UnmarshalKey("bus", &cfg); err != nil {
		log.Warn().Err(err).Msg("Failed to unmarshal bus config, using default")
	}

	driver := cfg.Driver
	log.Info().Str("driver", driver).Msg("Initializing Notification Bus")

	switch driver {
	case "redis":
		if rdb == nil {
			return nil, errors.New("redis client is nil")
		}
		return redis.NewRedisBus(rdb), nil
	case "postgres":
		if db == nil {
			return nil, errors.New("database client is nil")
		}
		return postgres.NewPostgresBus(db), nil
	case "nats":
		return nats.NewNatsBus(cfg.URL)
	}

	// 默认使用内存实现
	return memory.NewMemoryBus(), nil
}
