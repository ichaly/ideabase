package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
)

func TestSharedBuildBoundsUncooperativeWork(t *testing.T) {
	var group singleflight.Group
	started := time.Now()
	_, err := sharedBuildWithin(context.Background(), &group, "blocked", 20*time.Millisecond, func(ctx context.Context) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)
}

func TestSharedBuildCallerCancellationIsIndependent(t *testing.T) {
	var group singleflight.Group
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sharedBuildWithin(ctx, &group, "cancelled", time.Second, func(context.Context) (string, error) {
		return "unused", nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSharedBuildFailsFastWhenCapacityIsExhausted(t *testing.T) {
	for range cap(sharedBuildSlots) {
		sharedBuildSlots <- struct{}{}
	}
	defer func() {
		for range cap(sharedBuildSlots) {
			<-sharedBuildSlots
		}
	}()
	var group singleflight.Group
	_, err := sharedBuild(context.Background(), &group, "saturated", func(context.Context) (string, error) { return "", nil })
	require.ErrorContains(t, err, "capacity exceeded")
}
