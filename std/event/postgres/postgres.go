package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ichaly/ideabase/log"
	"github.com/ichaly/ideabase/std/event"
	"github.com/jackc/pgx/v5"
	pgdriver "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// 使用: import _ "github.com/ichaly/ideabase/std/event/postgres"
// 复用 *gorm.DB 连接的 LISTEN/NOTIFY
func init() {
	event.Register("postgres", func(conn any) (event.Transport, error) {
		db, ok := conn.(*gorm.DB)
		if !ok {
			return nil, fmt.Errorf("event/postgres: requires *gorm.DB, got %T", conn)
		}
		return newPostgresEvent(db), nil
	})
}

type postgresEvent struct {
	db       *gorm.DB
	dsn      string
	handlers map[string][]event.Handler
	lock     sync.Mutex
	cancel   context.CancelFunc
	stop     context.Context
	stopFunc context.CancelFunc
}

func newPostgresEvent(db *gorm.DB) *postgresEvent {
	var dsn string
	if d, ok := db.Dialector.(*pgdriver.Dialector); ok && d.Config != nil {
		dsn = d.Config.DSN
	}
	ctx, cancel := context.WithCancel(context.Background())
	e := &postgresEvent{db: db, dsn: dsn, handlers: make(map[string][]event.Handler), stop: ctx, stopFunc: cancel}
	if dsn != "" {
		go e.listenLoop()
	}
	return e
}

func (my *postgresEvent) Publish(ctx context.Context, topic string, payload any) error {
	body, err := event.Marshal(payload)
	if err != nil {
		return err
	}
	return my.db.Exec("SELECT pg_notify(?, ?)", topic, string(body)).Error
}

func (my *postgresEvent) Subscribe(_ context.Context, topic string, handler event.Handler) error {
	if my.dsn == "" {
		return errors.New("event/postgres: DSN not available")
	}
	my.lock.Lock()
	defer my.lock.Unlock()
	my.handlers[topic] = append(my.handlers[topic], handler)
	if my.cancel != nil {
		my.cancel()
	}
	return nil
}

func (my *postgresEvent) Close() error {
	my.stopFunc()
	return nil
}

func (my *postgresEvent) listenLoop() {
	for {
		select {
		case <-my.stop.Done():
			return
		default:
		}
		if err := my.runListen(); err != nil {
			log.Error().Err(err).Msg("postgres event listener disconnected, retrying in 5s")
			select {
			case <-time.After(5 * time.Second):
			case <-my.stop.Done():
				return
			}
		}
	}
}

func (my *postgresEvent) runListen() error {
	conn, err := pgx.Connect(context.Background(), my.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	listening := make(map[string]bool)
	for {
		my.lock.Lock()
		topics := make([]string, 0, len(my.handlers))
		for t := range my.handlers {
			topics = append(topics, t)
		}
		if my.cancel != nil {
			my.cancel()
		}
		ctx, cancel := context.WithCancel(context.Background())
		my.cancel = cancel
		my.lock.Unlock()

		for _, t := range topics {
			if !listening[t] {
				if _, err := conn.Exec(context.Background(), fmt.Sprintf("LISTEN %q", t)); err != nil {
					cancel()
					return err
				}
				listening[t] = true
			}
		}

		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				continue
			}
			cancel()
			return err
		}

		my.lock.Lock()
		var active []event.Handler
		for pattern, handlers := range my.handlers {
			if event.MatchTopic(pattern, notification.Channel) {
				active = append(active, handlers...)
			}
		}
		my.lock.Unlock()

		for _, h := range active {
			go func(handler event.Handler, data string) {
				if err := handler(context.Background(), []byte(data)); err != nil {
					log.Warn().Err(err).Str("topic", notification.Channel).Msg("postgres event handler error")
				}
			}(h, notification.Payload)
		}
	}
}
