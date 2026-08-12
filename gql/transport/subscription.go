package transport

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	core "github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type subscriptionEndpoint interface {
	Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error)
}

// NewSubscriptionHandler exposes one GraphQL SSE single-operation stream.
func NewSubscriptionHandler(endpoint subscriptionEndpoint, opts Options) http.Handler {
	opts = defaultOptions(opts)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint == nil {
			http.Error(w, "subscription unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			http.Error(w, "Accept must include text/event-stream", http.StatusNotAcceptable)
			return
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			http.Error(w, "Content-Type must be application/json", http.StatusUnsupportedMediaType)
			return
		}
		var params request
		if err := decodeLimited(r, opts.MaxBodyBytes, &params); err != nil {
			http.Error(w, "invalid subscription request", http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		if opts.Delegation != nil {
			token, ok := delegationToken(r)
			if !ok {
				http.Error(w, "delegation required", http.StatusUnauthorized)
				return
			}
			var err error
			ctx, err = opts.Delegation.Verify(ctx, token, operationDigest(params, r.Header.Get("GQL-Snapshot")))
			if err != nil {
				http.Error(w, "invalid delegation", http.StatusUnauthorized)
				return
			}
			ctx = core.WithDelegatedExecution(ctx)
		}
		execution := endpoint
		revision := r.Header.Get("GQL-Snapshot")
		if revision != "" {
			if opts.Delegation == nil {
				source, ok := endpoint.(manifestEndpoint)
				if !ok {
					http.Error(w, "snapshot pinning unavailable", http.StatusConflict)
					return
				}
				current, snapshotErr := source.Snapshot()
				if snapshotErr != nil || current == nil || current.Fingerprint() != revision {
					http.Error(w, "snapshot revision changed", http.StatusConflict)
					return
				}
			}
			if pinner, ok := endpoint.(snapshotPinner); ok {
				pinned, pinErr := pinner.ForSnapshot(revision)
				var subscribable bool
				execution, subscribable = pinned.(subscriptionEndpoint)
				if pinErr != nil || !subscribable {
					http.Error(w, "snapshot revision changed", http.StatusConflict)
					return
				}
			} else {
				source, ok := endpoint.(manifestEndpoint)
				if !ok {
					http.Error(w, "snapshot pinning unavailable", http.StatusConflict)
					return
				}
				snapshot, err := source.Snapshot()
				if err != nil || snapshot == nil || snapshot.Fingerprint() != revision {
					http.Error(w, "snapshot revision changed", http.StatusConflict)
					return
				}
			}
		}
		if revision == "" {
			if source, ok := endpoint.(manifestEndpoint); ok {
				if pinner, pinnable := endpoint.(snapshotPinner); pinnable {
					snapshot, snapshotErr := source.Snapshot()
					if snapshotErr != nil || snapshot == nil {
						http.Error(w, "snapshot unavailable", http.StatusServiceUnavailable)
						return
					}
					revision = snapshot.Fingerprint()
					pinned, pinErr := pinner.ForSnapshot(revision)
					var subscribable bool
					execution, subscribable = pinned.(subscriptionEndpoint)
					if pinErr != nil || !subscribable {
						http.Error(w, "snapshot revision changed", http.StatusConflict)
						return
					}
				}
			}
		}
		events, err := execution.Subscribe(ctx, params.Query, params.Variables, params.OperationName)
		if err != nil || events == nil {
			writeSubscriptionError(w, err)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
		if revision != "" {
			w.Header().Set("GQL-Snapshot", revision)
		} else if source, ok := execution.(manifestEndpoint); ok {
			if snapshot, snapshotErr := source.Snapshot(); snapshotErr == nil && snapshot != nil {
				w.Header().Set("GQL-Snapshot", snapshot.Fingerprint())
			}
		}
		w.WriteHeader(http.StatusOK)
		for {
			select {
			case <-ctx.Done():
				return
			case frame, open := <-events:
				if !open {
					_, _ = fmt.Fprint(w, "event: complete\n\n")
					flusher.Flush()
					return
				}
				eventBytes := int64(len("event: next\n\n"))
				for _, line := range strings.Split(string(frame), "\n") {
					eventBytes += int64(len("data: ")) + int64(len(line)) + 1
				}
				if eventBytes > opts.MaxResponseBytes {
					_, _ = fmt.Fprint(w, "event: next\ndata: {\"errors\":[{\"message\":\"subscription event exceeds response limit\",\"extensions\":{\"code\":\"LIMIT_EXCEEDED\"}}]}\n\nevent: complete\n\n")
					flusher.Flush()
					return
				}
				_, _ = fmt.Fprint(w, "event: next\n")
				for _, line := range strings.Split(string(frame), "\n") {
					_, _ = fmt.Fprintf(w, "data: %s\n", line)
				}
				_, _ = fmt.Fprint(w, "\n")
				flusher.Flush()
			}
		}
	})
}

func writeSubscriptionError(w http.ResponseWriter, cause error) {
	errors := gqlerror.List{&gqlerror.Error{Message: "subscription rejected", Extensions: map[string]any{"code": "SUBSCRIPTION_REJECTED"}}}
	if cause != nil {
		errors = core.GraphQLErrors(cause)
	}
	body, _ := json.Marshal(map[string]any{"errors": errors})
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = fmt.Fprintf(w, "event: next\ndata: %s\n\nevent: complete\n\n", body)
}
