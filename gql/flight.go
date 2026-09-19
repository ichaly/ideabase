package gql

import (
	"context"
	"fmt"
	"time"

	"github.com/ichaly/ideabase/gql/internal/sharedflight"
	"golang.org/x/sync/singleflight"
)

const sharedBuildTimeout = 30 * time.Second

type buildLimiter struct {
	global  chan struct{}
	tenants [64]chan struct{}
}

func newBuildLimiter(global, tenant int) *buildLimiter {
	limiter := &buildLimiter{global: make(chan struct{}, global)}
	for index := range limiter.tenants {
		limiter.tenants[index] = make(chan struct{}, tenant)
	}
	return limiter
}

func (limiter *buildLimiter) acquire(ctx context.Context) (func(), error) {
	tenant := ""
	if identity, ok := IdentityFrom(ctx); ok {
		tenant = identity.Tenant()
	}
	local := limiter.tenants[tenantRefreshSlot(tenant)]
	select {
	case local <- struct{}{}:
	default:
		return nil, fmt.Errorf("effective schema tenant capacity exceeded")
	}
	select {
	case limiter.global <- struct{}{}:
		return func() { <-limiter.global; <-local }, nil
	default:
		<-local
		return nil, fmt.Errorf("effective schema build capacity exceeded")
	}
}

func sharedBuild[T any](ctx context.Context, group *singleflight.Group, limiter *buildLimiter, key string, timeout time.Duration, build func(context.Context) (T, error)) (T, error) {
	return sharedflight.Do(ctx, group, key, timeout, func(shared context.Context) (T, error) {
		release, err := limiter.acquire(shared)
		if err != nil {
			var zero T
			return zero, err
		}
		defer release()
		return build(shared)
	})
}
