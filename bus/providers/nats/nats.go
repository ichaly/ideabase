package nats

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/ichaly/ideabase/bus/providers"
	"github.com/ichaly/ideabase/log"
	"github.com/nats-io/nats.go"
)

// NatsBus 基于 NATS 的通知总线
type NatsBus struct {
	nc *nats.Conn
}

func NewNatsBus(url string) (*NatsBus, error) {
	if url == "" {
		url = nats.DefaultURL
	}
	nc, err := nats.Connect(url,
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, err
	}
	return &NatsBus{nc: nc}, nil
}

// natsTopic 将冒号分隔的 topic 转为 NATS 的点分隔格式
func natsTopic(topic string) string {
	return strings.ReplaceAll(topic, ":", ".")
}

func (my *NatsBus) Publish(ctx context.Context, topic string, payload any) error {
	var body []byte
	var err error
	if v, ok := payload.([]byte); ok {
		body = v
	} else {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	return my.nc.Publish(natsTopic(topic), body)
}

func (my *NatsBus) Subscribe(ctx context.Context, topic string, handler providers.Handler) error {
	_, err := my.nc.Subscribe(natsTopic(topic), func(msg *nats.Msg) {
		go func(data []byte) {
			if err := handler(context.Background(), data); err != nil {
				log.Warn().Err(err).Str("topic", topic).Msg("NatsBus handler error")
			}
		}(msg.Data)
	})
	return err
}

// Close 关闭连接
func (my *NatsBus) Close() {
	my.nc.Close()
}
