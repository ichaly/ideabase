package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/ichaly/ideabase/bus/types"
	"github.com/ichaly/ideabase/log"
	"github.com/jackc/pgx/v5"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// PostgresBus 基于 PostgreSQL LISTEN/NOTIFY 的通知总线
type PostgresBus struct {
	db  *gorm.DB
	dsn string

	listenConn *pgx.Conn
	handlers   map[string][]types.Handler
	lock       sync.Mutex

	wakeUpCancel context.CancelFunc
}

func NewPostgresBus(db *gorm.DB) *PostgresBus {
	var dsn string
	if dialector, ok := db.Dialector.(*postgres.Dialector); ok {
		if dialector.Config != nil {
			dsn = dialector.Config.DSN
		}
	}
	if dsn == "" {
		log.Warn().Msg("PostgresBus: 无法获取 DSN，订阅功能将不可用")
	}

	bus := &PostgresBus{
		db:       db,
		dsn:      dsn,
		handlers: make(map[string][]types.Handler),
	}

	if dsn != "" {
		go bus.listenLoop()
	}

	return bus
}

func (my *PostgresBus) Publish(ctx context.Context, topic string, payload any) error {
	var body []byte
	var err error
	if v, ok := payload.(string); ok {
		body = []byte(v)
	} else if v, ok := payload.([]byte); ok {
		body = v
	} else {
		body, err = json.Marshal(payload)
		if err != nil {
			return err
		}
	}
	return my.db.Exec("SELECT pg_notify(?, ?)", topic, string(body)).Error
}

func (my *PostgresBus) Subscribe(ctx context.Context, topic string, handler types.Handler) error {
	if my.dsn == "" {
		return errors.New("PostgresBus DSN invalid")
	}

	my.lock.Lock()
	defer my.lock.Unlock()

	my.handlers[topic] = append(my.handlers[topic], handler)

	if my.wakeUpCancel != nil {
		my.wakeUpCancel()
	}

	return nil
}

func (my *PostgresBus) listenLoop() {
	for {
		err := my.runListen()
		if err != nil {
			log.Error().Err(err).Msg("PostgresBus listener disconnected, retrying in 5s...")
			time.Sleep(5 * time.Second)
		}
	}
}

func (my *PostgresBus) runListen() error {
	conn, err := pgx.Connect(context.Background(), my.dsn)
	if err != nil {
		return err
	}
	defer conn.Close(context.Background())

	my.lock.Lock()
	my.listenConn = conn
	my.lock.Unlock()

	listening := make(map[string]bool)

	for {
		my.lock.Lock()
		currentTopics := make([]string, 0, len(my.handlers))
		for t := range my.handlers {
			currentTopics = append(currentTopics, t)
		}
		cancel := my.wakeUpCancel
		ctx, newCancel := context.WithCancel(context.Background())
		my.wakeUpCancel = newCancel
		my.lock.Unlock()

		if cancel != nil {
			cancel()
		}

		for _, t := range currentTopics {
			if !listening[t] {
				_, err := conn.Exec(context.Background(), fmt.Sprintf("LISTEN %q", t))
				if err != nil {
					newCancel()
					return err
				}
				listening[t] = true
				log.Info().Str("topic", t).Msg("PostgresBus: LISTEN started")
			}
		}

		notification, err := conn.WaitForNotification(ctx)
		if err != nil {
			if errors.Is(err, context.Canceled) {
				continue
			}
			newCancel()
			return err
		}

		my.lock.Lock()
		handlers := my.handlers[notification.Channel]
		activeHandlers := make([]types.Handler, len(handlers))
		copy(activeHandlers, handlers)
		my.lock.Unlock()

		for _, h := range activeHandlers {
			go func(handler types.Handler, payload string) {
				if err := handler(context.Background(), []byte(payload)); err != nil {
					log.Warn().Err(err).Str("topic", notification.Channel).Msg("PostgresBus handler error")
				}
			}(h, notification.Payload)
		}
	}
}
