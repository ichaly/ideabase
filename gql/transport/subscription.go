package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	core "github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// subscribe writes a stream after the shared HTTP boundary has decoded,
// authenticated and pinned the request. Its lifetime is bounded by the client.
func (h *handler) subscribe(w http.ResponseWriter, r *http.Request, ctx context.Context, execution core.SubscriptionEndpoint, params request, revision string) {
	opts := h.options
	events, err := subscribeWithTimeout(ctx, execution, params, opts.Timeout)
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
}

type subscriptionResult struct {
	events <-chan []byte
	err    error
}

func subscribeWithTimeout(ctx context.Context, endpoint core.SubscriptionEndpoint, params request, timeout time.Duration) (<-chan []byte, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	defer func() {
		if ctx.Err() != nil {
			cancel()
		}
	}()
	result := make(chan subscriptionResult, 1)
	go func() {
		events, err := endpoint.Subscribe(attemptCtx, params.Query, params.Variables, params.OperationName)
		result <- subscriptionResult{events: events, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case reply := <-result:
		return reply.events, reply.err
	case <-timer.C:
		cancel()
		return nil, context.DeadlineExceeded
	case <-ctx.Done():
		cancel()
		return nil, ctx.Err()
	}
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
