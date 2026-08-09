package gql

import (
	"context"
	"fmt"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
)

const defaultSubscriptionLeaseInterval = time.Minute

type subscriptionLease struct {
	Barrier         *RevocationBarrier
	Identity        Identity
	Route           TenantRoute
	Interval        time.Duration
	PolicyProvider  PolicyProvider
	Operation       *ast.OperationDefinition
	Variables       map[string]interface{}
	PolicySignature string
}

func (l subscriptionLease) Check(ctx context.Context) error {
	if l.Barrier != nil {
		if err := l.Barrier.Check(l.Identity, l.Route); err != nil {
			return err
		}
	}
	return nil
}

type subscriptionLeaseKey struct{}

func withSubscriptionLease(ctx context.Context, lease subscriptionLease) context.Context {
	if lease.Interval <= 0 {
		lease.Interval = defaultSubscriptionLeaseInterval
	}
	return context.WithValue(ctx, subscriptionLeaseKey{}, lease)
}

func subscriptionLeaseFromContext(ctx context.Context) (subscriptionLease, bool) {
	lease, ok := ctx.Value(subscriptionLeaseKey{}).(subscriptionLease)
	return lease, ok
}

// ConfigureSubscriptionLease sets a periodic reauthorization interval for an Engine.
func ConfigureSubscriptionLease(engine *Engine, interval time.Duration) error {
	if engine == nil {
		return fmt.Errorf("engine is nil")
	}
	if interval <= 0 {
		return fmt.Errorf("subscription lease interval must be positive")
	}
	engine.subscriptionLease = interval
	return nil
}

func configuredSubscriptionLease(engine *Engine) time.Duration {
	if engine != nil && engine.subscriptionLease > 0 {
		return engine.subscriptionLease
	}
	return defaultSubscriptionLeaseInterval
}
