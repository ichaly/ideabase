package gql

import (
	"context"
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

const sharedBuildTimeout = 30 * time.Second

var sharedBuildSlots = make(chan struct{}, 64)

// sharedBuild lets each waiter honor its own cancellation while a bounded,
// detached build continues for healthy waiters sharing the same immutable key.
func sharedBuild[T any](ctx context.Context, group *singleflight.Group, key string, build func(context.Context) (T, error)) (T, error) {
	return sharedBuildWithin(ctx, group, key, sharedBuildTimeout, build)
}

func sharedBuildWithin[T any](ctx context.Context, group *singleflight.Group, key string, timeout time.Duration, build func(context.Context) (T, error)) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := group.DoChan(key, func() (any, error) {
		select {
		case sharedBuildSlots <- struct{}{}:
			defer func() { <-sharedBuildSlots }()
		default:
			return nil, fmt.Errorf("effective schema build capacity exceeded")
		}
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		return build(shared)
	})
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case result := <-result:
		if result.Err != nil {
			var zero T
			return zero, result.Err
		}
		return result.Val.(T), nil
	}
}
