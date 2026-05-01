package memory

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/ichaly/ideabase/std/event/internal/driver"
)

// newBus 直接构造 memoryEvent,绕开 event.Register 全局注册。
func newBus() *memoryEvent {
	return &memoryEvent{handlers: make(map[string][]driver.Handler)}
}

// 单 handler 正常执行 -> Publish 返回 nil。
func TestMemoryBus_HappyPath(t *testing.T) {
	bus := newBus()
	var called int32
	_ = bus.Subscribe(context.Background(), "t.ok", func(ctx context.Context, body []byte) error {
		atomic.AddInt32(&called, 1)
		return nil
	})
	if err := bus.Publish(context.Background(), "t.ok", "payload"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if atomic.LoadInt32(&called) != 1 {
		t.Fatalf("handler not called: %d", called)
	}
}

// 一个 handler panic 不应阻断其他 handler 接收同一事件。
func TestMemoryBus_HandlerPanicDoesNotPropagate(t *testing.T) {
	bus := newBus()
	var first, third int32
	_ = bus.Subscribe(context.Background(), "t.boom", func(ctx context.Context, body []byte) error {
		atomic.AddInt32(&first, 1)
		return nil
	})
	_ = bus.Subscribe(context.Background(), "t.boom", func(ctx context.Context, body []byte) error {
		panic("boom")
	})
	_ = bus.Subscribe(context.Background(), "t.boom", func(ctx context.Context, body []byte) error {
		atomic.AddInt32(&third, 1)
		return nil
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic propagated to publisher: %v", r)
		}
	}()
	if err := bus.Publish(context.Background(), "t.boom", "payload"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if atomic.LoadInt32(&first) != 1 || atomic.LoadInt32(&third) != 1 {
		t.Fatalf("siblings skipped: first=%d third=%d", first, third)
	}
}

// handler 返 error -> 走 log.Warn 但循环继续。
func TestMemoryBus_HandlerErrorTolerant(t *testing.T) {
	bus := newBus()
	var second int32
	_ = bus.Subscribe(context.Background(), "t.err", func(ctx context.Context, body []byte) error {
		return errors.New("first failed")
	})
	_ = bus.Subscribe(context.Background(), "t.err", func(ctx context.Context, body []byte) error {
		atomic.AddInt32(&second, 1)
		return nil
	})
	if err := bus.Publish(context.Background(), "t.err", "payload"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if atomic.LoadInt32(&second) != 1 {
		t.Fatalf("second handler skipped after first error")
	}
}

// nil pointer panic 也应被兜底,验证常见 panic 类型。
func TestMemoryBus_NilPointerPanicRecovered(t *testing.T) {
	bus := newBus()
	var sibling int32
	_ = bus.Subscribe(context.Background(), "t.nil", func(ctx context.Context, body []byte) error {
		var p *struct{ Field int }
		_ = p.Field // nil deref
		return nil
	})
	_ = bus.Subscribe(context.Background(), "t.nil", func(ctx context.Context, body []byte) error {
		atomic.AddInt32(&sibling, 1)
		return nil
	})

	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil-deref panic propagated: %v", r)
		}
	}()
	if err := bus.Publish(context.Background(), "t.nil", "payload"); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if atomic.LoadInt32(&sibling) != 1 {
		t.Fatalf("sibling skipped after nil-deref")
	}
}

// 0 个 handler 时 Publish 不应 panic。
func TestMemoryBus_NoHandlers(t *testing.T) {
	bus := newBus()
	if err := bus.Publish(context.Background(), "t.empty", "payload"); err != nil {
		t.Fatalf("publish without handlers: %v", err)
	}
}
