package sharedflight

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sync/singleflight"
)

func TestCanceledLeaderDoesNotFailFollower(t *testing.T) {
	var group singleflight.Group
	var calls atomic.Int32
	started, release := make(chan struct{}), make(chan struct{})
	call := func(context.Context) (string, error) {
		calls.Add(1)
		close(started)
		<-release
		return "ok", nil
	}
	leaderCtx, cancel := context.WithCancel(context.Background())
	leader := make(chan error, 1)
	go func() { _, err := Do(leaderCtx, &group, "key", time.Second, call); leader <- err }()
	<-started
	follower := make(chan string, 1)
	go func() { value, _ := Do(context.Background(), &group, "key", time.Second, call); follower <- value }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if err := <-leader; err != context.Canceled {
		t.Fatalf("leader error = %v", err)
	}
	close(release)
	if value := <-follower; value != "ok" || calls.Load() != 1 {
		t.Fatalf("follower = %q, calls = %d", value, calls.Load())
	}
}

func TestDoPropagatesTimeoutAndCallErrors(t *testing.T) {
	var group singleflight.Group
	if _, err := Do(context.Background(), &group, "timeout", time.Millisecond, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	}); err == nil {
		t.Fatal("timed out shared call unexpectedly succeeded")
	}
	if _, err := Do(context.Background(), &group, "error", time.Second, func(context.Context) (string, error) {
		return "", context.DeadlineExceeded
	}); err != context.DeadlineExceeded {
		t.Fatalf("call error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Do(ctx, &group, "canceled", time.Second, func(context.Context) (string, error) { return "ok", nil }); err != context.Canceled {
		t.Fatalf("canceled waiter error = %v", err)
	}
}

func BenchmarkDoWarm(b *testing.B) {
	var group singleflight.Group
	call := func(context.Context) (int, error) { return 1, nil }
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Do(context.Background(), &group, "warm", time.Second, call); err != nil {
			b.Fatal(err)
		}
	}
}
