package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

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
		heartbeatInterval := min(opts.Timeout/2, 15*time.Second)
		heartbeat := time.NewTicker(heartbeatInterval)
		defer heartbeat.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-heartbeat.C:
				if err := writeSSEFrame(w, flusher, opts.Timeout, []byte(": keepalive\n\n")); err != nil {
					return
				}
			case frame, open := <-events:
				if !open {
					_ = writeSSEFrame(w, flusher, opts.Timeout, []byte("event: complete\n\n"))
					return
				}
				newlines := int64(bytes.Count(frame, []byte{'\n'}))
				lines := newlines + 1
				eventBytes := int64(len("event: next\n\n")) + int64(len(frame)) + lines*int64(len("data: ")+1)
				if eventBytes > opts.MaxResponseBytes {
					_ = writeSSEFrame(w, flusher, opts.Timeout, []byte("event: next\ndata: {\"errors\":[{\"message\":\"subscription event exceeds response limit\",\"extensions\":{\"code\":\"LIMIT_EXCEEDED\"}}]}\n\nevent: complete\n\n"))
					return
				}
				payload := bytes.NewBuffer(make([]byte, 0, int(eventBytes)))
				payload.WriteString("event: next\n")
				for line := range bytes.SplitSeq(frame, []byte{'\n'}) {
					payload.WriteString("data: ")
					payload.Write(line)
					payload.WriteByte('\n')
				}
				payload.WriteByte('\n')
				if err := writeSSEFrame(w, flusher, opts.Timeout, payload.Bytes()); err != nil {
					return
				}
			}
		}
	})
}

func writeSSEFrame(w http.ResponseWriter, flusher http.Flusher, timeout time.Duration, frame []byte) error {
	controller := http.NewResponseController(w)
	_ = controller.SetWriteDeadline(time.Now().Add(timeout))
	if _, err := w.Write(frame); err != nil {
		return err
	}
	if flushErr, ok := w.(interface{ FlushError() error }); ok {
		if err := flushErr.FlushError(); err != nil {
			return err
		}
	} else {
		flusher.Flush()
	}
	_ = controller.SetWriteDeadline(time.Time{})
	return nil
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
