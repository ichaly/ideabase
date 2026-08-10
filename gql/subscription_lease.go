package gql

import (
	"context"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
)

const defaultSubscriptionLeaseInterval = time.Minute

type subscriptionLease struct {
	Barrier         SecurityStateProvider
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
		if err := l.Barrier.Check(ctx, l.Identity, l.Route); err != nil {
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

func configuredSubscriptionLease(engine *Engine) time.Duration {
	if engine != nil && engine.subscriptionLease > 0 {
		return engine.subscriptionLease
	}
	return defaultSubscriptionLeaseInterval
}
