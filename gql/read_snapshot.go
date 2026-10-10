package gql

import "context"

type readSnapshotKey struct{}

// WithReadSnapshot requests one read-only repeatable-read transaction for the
// database query and every post-processing resolver in this operation.
func WithReadSnapshot(ctx context.Context) context.Context {
	return context.WithValue(ctx, readSnapshotKey{}, true)
}

func readSnapshotRequested(ctx context.Context) bool {
	enabled, _ := ctx.Value(readSnapshotKey{}).(bool)
	return enabled
}
