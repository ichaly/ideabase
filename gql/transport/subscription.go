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
	admission := make(chan struct{}, opts.MaxConcurrent+opts.MaxQueued)
	executionSlots := make(chan struct{}, opts.MaxConcurrent)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if endpoint == nil {
			writeProblem(w, http.StatusServiceUnavailable, "SUBSCRIPTION_UNAVAILABLE", "subscription unavailable", true)
			return
		}
		select {
		case admission <- struct{}{}:
			defer func() { <-admission }()
		default:
			w.Header().Set("Retry-After", "1")
			writeProblem(w, http.StatusTooManyRequests, "REQUEST_OVERLOADED", "subscription capacity exhausted", true)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			writeProblem(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "method not allowed", false)
			return
		}
		if !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
			writeProblem(w, http.StatusNotAcceptable, "NOT_ACCEPTABLE", "Accept must include text/event-stream", false)
			return
		}
		if !isJSONContentType(r.Header.Get("Content-Type")) {
			writeProblem(w, http.StatusUnsupportedMediaType, "UNSUPPORTED_MEDIA_TYPE", "Content-Type must be application/json", false)
			return
		}
		var params request
		if err := decodeLimited(r, opts.MaxBodyBytes, &params); err != nil {
			writeProblem(w, http.StatusBadRequest, "INVALID_REQUEST", "invalid subscription request", false)
			return
		}
		ctx := core.WithResponseLimit(r.Context(), opts.MaxResponseBytes)
		execution := endpoint
		ctx, execution, revision, failure := prepareRequest(ctx, r, endpoint, execution, opts.Delegation,
			operationDigest(params, r.Header.Get("GQL-Snapshot"), r.Header.Get(executionPinHeader)),
			func(value any) (subscriptionEndpoint, bool) {
				endpoint, ok := value.(subscriptionEndpoint)
				return endpoint, ok
			})
		if failure != nil {
			writeRequestFailure(w, failure)
			return
		}
		select {
		case executionSlots <- struct{}{}:
			defer func() { <-executionSlots }()
		case <-ctx.Done():
			writeProblem(w, http.StatusServiceUnavailable, "REQUEST_TIMEOUT", "request expired while awaiting subscription capacity", true)
			return
		}
		events, err := execution.Subscribe(ctx, params.Query, params.Variables, params.OperationName)
		if err != nil || events == nil {
			writeSubscriptionError(w, err)
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			writeProblem(w, http.StatusInternalServerError, "STREAMING_UNAVAILABLE", "streaming unavailable", false)
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
		if heartbeatInterval <= 0 {
			heartbeatInterval = time.Millisecond
		}
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
	deadlineErr := controller.SetWriteDeadline(time.Now().Add(timeout))
	if deadlineErr != nil {
		// Never issue an unbounded Write when the underlying writer cannot
		// enforce a deadline. Returning before Write is fail-closed and does
		// not leave an uncancellable goroutine behind.
		return fmt.Errorf("SSE write deadline unavailable: %w", deadlineErr)
	} else if _, err := w.Write(frame); err != nil {
		return err
	}
	if flushErr, ok := w.(interface{ FlushError() error }); ok {
		if err := flushErr.FlushError(); err != nil {
			return err
		}
	} else {
		flusher.Flush()
	}
	if deadlineErr == nil {
		_ = controller.SetWriteDeadline(time.Time{})
	}
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
	frame := []byte(fmt.Sprintf("event: next\ndata: %s\n\nevent: complete\n\n", body))
	flusher, ok := w.(http.Flusher)
	if !ok {
		return
	}
	_ = writeSSEFrame(w, flusher, defaultTimeout, frame)
}
