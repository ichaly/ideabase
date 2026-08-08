package gql

import (
	"context"
	"errors"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"gorm.io/gorm"
)

// ErrDatabaseUnavailable reports that a resolver was executed without a
// database provider. Resolvers should return this error instead of silently
// opening a second connection or bypassing the request transaction.
var ErrDatabaseUnavailable = errors.New("gql: request database is unavailable")

type executionContextKey struct{}

// ExecutionContext is the read-only request view exposed to custom resolvers.
// Identity is copied only from the host-authenticated context; it is never an
// input to WithExecutionContext and therefore cannot be supplied by GraphQL
// variables. DB selects the transaction attached to the request when one is
// active, falling back to the request's base database.
type ExecutionContext struct {
	ctx                 context.Context
	identity            Identity
	hasIdentity         bool
	selection           ast.SelectionSet
	snapshotFingerprint string
	database            *gorm.DB
}

// WithExecutionContext attaches the engine-owned execution view to ctx. The
// identity is inherited from WithIdentity (or an existing execution view),
// while selection, snapshot and database are supplied by the engine.
func WithExecutionContext(ctx context.Context, selection ast.SelectionSet, snapshotFingerprint string, database *gorm.DB) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		if previous, exists := ExecutionContextFromContext(ctx); exists {
			identity, ok = previous.identity, previous.hasIdentity
		}
	}
	view := ExecutionContext{
		ctx: ctx, identity: identity, hasIdentity: ok,
		selection:           append(ast.SelectionSet(nil), selection...),
		snapshotFingerprint: snapshotFingerprint,
		database:            database,
	}
	return context.WithValue(ctx, executionContextKey{}, view)
}

// ExecutionContextFromContext returns the request execution view, if the
// current resolver call is running under an engine execution.
func ExecutionContextFromContext(ctx context.Context) (ExecutionContext, bool) {
	if ctx == nil {
		return ExecutionContext{}, false
	}
	view, ok := ctx.Value(executionContextKey{}).(ExecutionContext)
	if ok {
		// Keep the lookup context, not only the context used when the view was
		// attached: executors may add the transaction key in a child context.
		view.ctx = ctx
	}
	return view, ok
}

// Identity returns the host-authenticated identity. The zero value means the
// request has no authenticated identity; use IdentityValue when presence must
// be distinguished explicitly.
func (my ExecutionContext) Identity() Identity { return my.identity }

// IdentityValue returns the host-authenticated identity and whether it exists.
func (my ExecutionContext) IdentityValue() (Identity, bool) { return my.identity, my.hasIdentity }

// Selection returns a shallow copy of the GraphQL selection set. AST fields
// are owned by the parsed request and must be treated as read-only.
func (my ExecutionContext) Selection() ast.SelectionSet {
	return append(ast.SelectionSet(nil), my.selection...)
}

// Deadline returns the current resolver context deadline, if one was set.
func (my ExecutionContext) Deadline() (deadline time.Time, ok bool) {
	if my.ctx == nil {
		return time.Time{}, false
	}
	return my.ctx.Deadline()
}

// SnapshotFingerprint identifies the immutable schema/handler contract used
// to compile this request.
func (my ExecutionContext) SnapshotFingerprint() string { return my.snapshotFingerprint }

// DB returns the request transaction when present, otherwise its base DB.
func (my ExecutionContext) DB() *gorm.DB {
	if tx := Tx(my.ctx); tx != nil {
		return tx
	}
	if my.database != nil && my.ctx != nil {
		return my.database.WithContext(my.ctx)
	}
	return my.database
}

// DB is the resolver-facing database helper. It preserves the historical Tx
// behavior while also supporting query resolvers with a request DB provider.
func DB(ctx context.Context) *gorm.DB {
	if tx := Tx(ctx); tx != nil {
		return tx
	}
	if view, ok := ExecutionContextFromContext(ctx); ok {
		return view.DB()
	}
	return nil
}

// RequireDB is the explicit form of DB for resolvers that cannot proceed
// without persistence.
func RequireDB(ctx context.Context) (*gorm.DB, error) {
	if database := DB(ctx); database != nil {
		return database, nil
	}
	return nil, ErrDatabaseUnavailable
}
