package transport

import (
	"context"
	"net/http"

	core "github.com/ichaly/ideabase/gql"
)

type requestFailure struct {
	status    int
	code      string
	message   string
	retryable bool
}

func prepareRequest(ctx context.Context, r *http.Request, endpoint core.Endpoint, verifier DelegationVerifier, digest string) (context.Context, core.Endpoint, string, *requestFailure) {
	execution := endpoint
	if verifier != nil {
		token, ok := delegationToken(r)
		if !ok {
			return ctx, execution, "", &requestFailure{http.StatusUnauthorized, "AUTHENTICATION_REQUIRED", "delegation required", false}
		}
		var err error
		ctx, err = verifier.Verify(ctx, token, digest)
		if err != nil {
			return ctx, execution, "", &requestFailure{http.StatusUnauthorized, "INVALID_DELEGATION", "invalid delegation", false}
		}
	}

	revision := r.Header.Get("GQL-Snapshot")
	source, hasSnapshot := endpoint.(manifestEndpoint)
	if revision != "" && verifier == nil {
		if !hasSnapshot {
			return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_UNAVAILABLE", "snapshot pinning unavailable", true}
		}
		current, err := source.Snapshot()
		if err != nil || current == nil || current.Fingerprint() != revision {
			return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true}
		}
	}
	if revision == "" && hasSnapshot {
		if _, ok := endpoint.(snapshotPinner); ok {
			current, err := source.Snapshot()
			if err != nil || current == nil {
				return ctx, execution, "", &requestFailure{http.StatusServiceUnavailable, "SNAPSHOT_UNAVAILABLE", "snapshot unavailable", true}
			}
			revision = current.Fingerprint()
		}
	}
	if revision != "" {
		if pinner, ok := endpoint.(snapshotPinner); ok {
			pinned, err := pinner.ForSnapshot(revision)
			if err != nil || pinned == nil {
				return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true}
			}
			execution = pinned
		} else if hasSnapshot {
			current, err := source.Snapshot()
			if err != nil || current == nil || current.Fingerprint() != revision {
				return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_CHANGED", "snapshot revision changed", true}
			}
		} else {
			return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_UNAVAILABLE", "snapshot pinning unavailable", true}
		}
	}
	if pin := r.Header.Get(executionPinHeader); pin != "" {
		effective, ok := any(execution).(core.EffectiveSchemaEndpoint)
		if !ok {
			return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_CONFLICT", "effective schema execution pin unavailable", true}
		}
		artifact, err := effective.EffectiveSchema(ctx)
		if err != nil || artifact.ExecutionPin == "" || artifact.ExecutionPin != pin {
			return ctx, execution, revision, &requestFailure{http.StatusConflict, "SNAPSHOT_CONFLICT", "effective schema execution pin changed", true}
		}
		ctx = core.WithExecutionPin(ctx, pin)
	}
	return ctx, execution, revision, nil
}

func writeRequestFailure(w http.ResponseWriter, failure *requestFailure) {
	writeProblem(w, failure.status, failure.code, failure.message, failure.retryable)
}
