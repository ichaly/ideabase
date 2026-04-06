package std

import (
	"time"

	"github.com/nats-io/nats.go"
)

// NewNats 根据 Config 中的 NATS URL 创建连接
func NewNats(c *Config) (*nats.Conn, error) {
	if c.Nats == "" {
		return nil, nil
	}
	return nats.Connect(c.Nats,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
}
