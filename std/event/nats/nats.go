package nats

import (
	"context"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std/event"
	"github.com/ichaly/ideabase/std/event/internal/driver"
	gonats "github.com/nats-io/nats.go"
)

// 使用: import _ "github.com/ichaly/ideabase/std/event/nats"
// URL: nats://user:pass@host:4222
func init() {
	event.Register("nats", func(conn any) (driver.Driver, error) {
		nc, ok := conn.(*gonats.Conn)
		if !ok {
			return nil, fmt.Errorf("event/nats: requires *nats.Conn, got %T", conn)
		}
		return &natsEvent{nc: nc}, nil
	})
}

type natsEvent struct {
	nc *gonats.Conn
}

func (my *natsEvent) Publish(_ context.Context, topic string, payload any) error {
	body, err := event.Marshal(payload)
	if err != nil {
		return err
	}
	return my.nc.Publish(natsTopic(topic), body)
}

func (my *natsEvent) Subscribe(_ context.Context, topic string, handler event.Handler) error {
	_, err := my.nc.Subscribe(natsTopic(topic), func(msg *gonats.Msg) {
		go func(data []byte) {
			if err := handler(context.Background(), data); err != nil {
				log.Warn().Err(err).Str("topic", topic).Msg("nats event handler error")
			}
		}(msg.Data)
	})
	return err
}

func (my *natsEvent) Close() error {
	my.nc.Close()
	return nil
}

func natsTopic(topic string) string {
	return strings.ReplaceAll(topic, ":", ".")
}
