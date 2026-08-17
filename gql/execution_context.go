package gql

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"gorm.io/gorm"
)

// ErrDatabaseUnavailable reports that a resolver was executed without a
// database provider. Resolvers should return this error instead of silently
// opening a second connection or bypassing the request transaction.
var ErrDatabaseUnavailable = errors.New("gql: request database is unavailable")

type executionContextKey struct{}
type invalidationStateKey struct{}
type engineMutationTransactionKey struct{}

type invalidationState struct {
	mu     sync.Mutex
	tables map[string]struct{}
}

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
	invalidation        *invalidationState
	coordinate          string
	capability          Capability
	hasCapability       bool
}

func withInvalidationState(ctx context.Context) context.Context {
	if _, ok := ctx.Value(invalidationStateKey{}).(*invalidationState); ok {
		return ctx
	}
	return context.WithValue(ctx, invalidationStateKey{}, &invalidationState{tables: make(map[string]struct{})})
}

// Invalidate requests a post-commit refresh for one or more database tables.
// It is only valid from an engine-owned mutation transaction.
func (my ExecutionContext) Invalidate(tables ...string) error {
	if my.invalidation == nil || my.ctx.Value(engineMutationTransactionKey{}) != true {
		return fmt.Errorf("table invalidation requires an engine-owned mutation transaction")
	}
	my.invalidation.mu.Lock()
	defer my.invalidation.mu.Unlock()
	for _, table := range tables {
		if strings.TrimSpace(table) == "" {
			return fmt.Errorf("invalidated table name is required")
		}
	}
	for _, table := range tables {
		my.invalidation.tables[table] = struct{}{}
	}
	return nil
}

func invalidationTables(ctx context.Context) []string {
	state, _ := ctx.Value(invalidationStateKey{}).(*invalidationState)
	if state == nil {
		return nil
	}
	state.mu.Lock()
	defer state.mu.Unlock()
	tables := make([]string, 0, len(state.tables))
	for table := range state.tables {
		tables = append(tables, table)
	}
	sort.Strings(tables)
	return tables
}

// withExecutionContext attaches the engine-owned execution view to ctx. The
// identity is inherited from WithIdentity (or an existing execution view),
// while selection, snapshot and database are supplied by the engine.
func withExecutionContext(ctx context.Context, selection ast.SelectionSet, snapshotFingerprint string, database *gorm.DB, selected ...Capability) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		if previous, exists := ExecutionContextFromContext(ctx); exists {
			identity, ok = previous.identity, previous.hasIdentity
		}
	}
	ctx = withInvalidationState(ctx)
	view := ExecutionContext{
		ctx: ctx, identity: identity, hasIdentity: ok,
		selection:           cloneResolverSelection(selection),
		snapshotFingerprint: snapshotFingerprint,
		database:            database,
		invalidation:        ctx.Value(invalidationStateKey{}).(*invalidationState),
	}
	if len(selected) != 0 {
		capability := selected[0]
		view.capability, view.coordinate, view.hasCapability = cloneCapability(capability), capability.Coordinate, true
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

// Selection returns a detached copy of the GraphQL selection set.
func (my ExecutionContext) Selection() ast.SelectionSet {
	return cloneResolverSelection(my.selection)
}

func cloneResolverSelection(selection ast.SelectionSet) ast.SelectionSet {
	out := make(ast.SelectionSet, len(selection))
	for index, item := range selection {
		switch item := item.(type) {
		case *ast.Field:
			field := *item
			field.Definition, field.ObjectDefinition = nil, nil
			field.Arguments = cloneResolverArguments(item.Arguments)
			field.Directives = cloneResolverDirectives(item.Directives)
			field.SelectionSet = cloneResolverSelection(item.SelectionSet)
			out[index] = &field
		case *ast.InlineFragment:
			fragment := *item
			fragment.ObjectDefinition = nil
			fragment.Directives = cloneResolverDirectives(item.Directives)
			fragment.SelectionSet = cloneResolverSelection(item.SelectionSet)
			out[index] = &fragment
		case *ast.FragmentSpread:
			spread := *item
			spread.ObjectDefinition, spread.Definition = nil, nil
			spread.Directives = cloneResolverDirectives(item.Directives)
			out[index] = &spread
		}
	}
	return out
}

func cloneResolverArguments(arguments ast.ArgumentList) ast.ArgumentList {
	out := make(ast.ArgumentList, len(arguments))
	for index, argument := range arguments {
		copy := *argument
		copy.Value = cloneResolverValue(argument.Value)
		out[index] = &copy
	}
	return out
}

func cloneResolverDirectives(directives ast.DirectiveList) ast.DirectiveList {
	out := make(ast.DirectiveList, len(directives))
	for index, directive := range directives {
		copy := *directive
		copy.Definition, copy.ParentDefinition = nil, nil
		copy.Arguments = cloneResolverArguments(directive.Arguments)
		out[index] = &copy
	}
	return out
}

func cloneResolverValue(value *ast.Value) *ast.Value {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Definition, copy.VariableDefinition, copy.ExpectedType = nil, nil, nil
	copy.Children = make(ast.ChildValueList, len(value.Children))
	for index, child := range value.Children {
		childCopy := *child
		childCopy.Value = cloneResolverValue(child.Value)
		copy.Children[index] = &childCopy
	}
	return &copy
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

// Scope returns a detached copy of trusted request scope values.
func (my ExecutionContext) Scope() map[string]any { return Scope(my.ctx) }

// Route returns the pinned trusted tenant route, when one is present.
func (my ExecutionContext) Route() (TenantRoute, bool) { return PinnedRoute(my.ctx) }

// Coordinate identifies the capability selected for this resolver call.
func (my ExecutionContext) Coordinate() (string, bool) { return my.coordinate, my.hasCapability }

// Capability returns a defensive copy of the selected capability.
func (my ExecutionContext) Capability() (Capability, bool) {
	if !my.hasCapability {
		return Capability{}, false
	}
	return cloneCapability(my.capability), true
}

// PolicyDecision returns the normalized decision for the selected capability.
func (my ExecutionContext) PolicyDecision() (PolicyDecision, bool) {
	if !my.hasCapability {
		return PolicyDecision{}, false
	}
	return PolicyDecisionFromContext(my.ctx, my.coordinate)
}

// DB returns the request transaction when present, otherwise its base DB.
func (my ExecutionContext) DB() *gorm.DB {
	if tx := transactionFromContext(my.ctx); tx != nil {
		return tx
	}
	if my.database != nil && my.ctx != nil {
		return my.database.WithContext(my.ctx)
	}
	return my.database
}

// RequireDB is the explicit form for resolvers that cannot proceed without
// the engine-owned request database or transaction.
func (my ExecutionContext) RequireDB() (*gorm.DB, error) {
	if database := my.DB(); database != nil {
		return database, nil
	}
	return nil, ErrDatabaseUnavailable
}
