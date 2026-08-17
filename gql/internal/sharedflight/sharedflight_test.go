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
