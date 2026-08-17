package sharedflight

import (
	"context"
	"time"

	"golang.org/x/sync/singleflight"
)

// Do shares one bounded call without allowing the first waiter's cancellation
// to fail healthy followers. Each waiter still observes its own context.
func Do[T any](ctx context.Context, group *singleflight.Group, key string, timeout time.Duration, call func(context.Context) (T, error)) (T, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	result := group.DoChan(key, func() (any, error) {
		shared, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		return call(shared)
	})
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case <-timer.C:
		var zero T
		return zero, context.DeadlineExceeded
	case result := <-result:
		if result.Err != nil {
			var zero T
			return zero, result.Err
		}
		return result.Val.(T), nil
	}
}
