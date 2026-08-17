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
	limiter := newBuildLimiter(2, 1)
	identity, err := NewIdentity(IdentityInput{Tenant: "blocked", Subject: "user"})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	started := time.Now()
	_, err = sharedBuildWithin(ctx, &group, limiter, "blocked", 20*time.Millisecond, func(context.Context) (string, error) {
		select {}
	})
	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Less(t, time.Since(started), time.Second)

	healthy, err := NewIdentity(IdentityInput{Tenant: "healthy", Subject: "user"})
	require.NoError(t, err)
	value, err := sharedBuildWithin(WithIdentity(context.Background(), healthy), &group, limiter, "healthy", time.Second, func(context.Context) (string, error) {
		return "ok", nil
	})
	require.NoError(t, err)
	require.Equal(t, "ok", value, "one stuck call must not consume all bounded capacity")
}

func TestSharedBuildCallerCancellationIsIndependent(t *testing.T) {
	var group singleflight.Group
	limiter := newBuildLimiter(2, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := sharedBuildWithin(ctx, &group, limiter, "cancelled", time.Second, func(context.Context) (string, error) {
		return "unused", nil
	})
	require.ErrorIs(t, err, context.Canceled)
}

func TestSharedBuildFailsFastWhenCapacityIsExhausted(t *testing.T) {
	limiter := newBuildLimiter(1, 1)
	limiter.global <- struct{}{}
	var group singleflight.Group
	_, err := sharedBuild(context.Background(), &group, limiter, "saturated", func(context.Context) (string, error) { return "", nil })
	require.ErrorContains(t, err, "capacity exceeded")
}

func TestBuildCapacityIsIsolatedAndTenantFair(t *testing.T) {
	first, second := newBuildLimiter(2, 1), newBuildLimiter(1, 1)
	identity := func(tenant string) context.Context {
		id, err := NewIdentity(IdentityInput{Tenant: tenant, Subject: "user"})
		require.NoError(t, err)
		return WithIdentity(context.Background(), id)
	}
	started, release := make(chan struct{}), make(chan struct{})
	var group singleflight.Group
	done := make(chan error, 1)
	go func() {
		_, err := sharedBuild(identity("a"), &group, first, "a-1", func(context.Context) (string, error) {
			close(started)
			<-release
			return "ok", nil
		})
		done <- err
	}()
	<-started
	_, err := sharedBuild(identity("a"), &group, first, "a-2", func(context.Context) (string, error) { return "", nil })
	require.ErrorContains(t, err, "tenant capacity exceeded")
	value, err := sharedBuild(identity("b"), &group, first, "b", func(context.Context) (string, error) { return "fair", nil })
	require.NoError(t, err)
	require.Equal(t, "fair", value)
	value, err = sharedBuild(identity("a"), &singleflight.Group{}, second, "isolated", func(context.Context) (string, error) { return "isolated", nil })
	require.NoError(t, err)
	require.Equal(t, "isolated", value)
	close(release)
	require.NoError(t, <-done)
}
