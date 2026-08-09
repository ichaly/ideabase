package transport

import (
	"context"
	"fmt"
	"mime"
	"net/http"
	"strings"
)

type subscriptionEndpoint interface {
	Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error)
}

// NewSubscriptionHandler exposes one GraphQL SSE single-operation stream.
func NewSubscriptionHandler(endpoint subscriptionEndpoint, options ...Options) http.Handler {
	var opts Options
	if len(options) > 0 {
		opts = options[0]
	}
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = defaultMaxBodyBytes
	}
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
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
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
			const scheme = "GQL-Delegation "
			authorization := r.Header.Get("Authorization")
			if !strings.HasPrefix(authorization, scheme) {
				http.Error(w, "delegation required", http.StatusUnauthorized)
				return
			}
			var err error
			ctx, err = opts.Delegation.Verify(ctx, strings.TrimPrefix(authorization, scheme), operationDigest(params))
			if err != nil {
				http.Error(w, "invalid delegation", http.StatusUnauthorized)
				return
			}
		}
		if revision := r.Header.Get("GQL-Snapshot"); revision != "" {
			source, ok := endpoint.(manifestEndpoint)
			if !ok {
				http.Error(w, "snapshot pinning unavailable", http.StatusConflict)
				return
			}
			snapshot, err := source.Snapshot()
			if err != nil || snapshot.Fingerprint() != revision {
				http.Error(w, "snapshot revision changed", http.StatusConflict)
				return
			}
		}
		events, err := endpoint.Subscribe(ctx, params.Query, params.Variables, params.OperationName)
		if err != nil || events == nil {
			http.Error(w, "subscription rejected", http.StatusUnprocessableEntity)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unavailable", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-store")
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
