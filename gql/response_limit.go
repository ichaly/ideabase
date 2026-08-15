package gql

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"errors"
	"io"
	"sync/atomic"
)

type responseLimitKey struct{}

type responseBudget struct {
	limit     int64
	remaining atomic.Int64
	cancel    context.CancelFunc
}

// WithResponseLimit bounds the encoded GraphQL response at the execution
// boundary, before a transport receives or copies it.
func WithResponseLimit(ctx context.Context, bytes int64) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if bytes <= 0 {
		return ctx
	}
	ctx, cancel := context.WithCancel(ctx)
	budget := &responseBudget{limit: bytes, cancel: cancel}
	budget.remaining.Store(bytes)
	return context.WithValue(ctx, responseLimitKey{}, budget)
}

// ResponseLimit returns the active execution response budget, or zero when
// the caller did not request one.
func ResponseLimit(ctx context.Context) int64 {
	if ctx == nil {
		return 0
	}
	if budget, ok := ctx.Value(responseLimitKey{}).(*responseBudget); ok {
		return budget.limit
	}
	limit, _ := ctx.Value(responseLimitKey{}).(int64)
	return limit
}

func consumeResponseBudget(ctx context.Context, size int) bool {
	if size < 0 {
		return false
	}
	budget, ok := ctx.Value(responseLimitKey{}).(*responseBudget)
	if !ok {
		return true
	}
	for {
		remaining := budget.remaining.Load()
		if int64(size) > remaining {
			budget.cancel()
			return false
		}
		if budget.remaining.CompareAndSwap(remaining, remaining-int64(size)) {
			return true
		}
	}
}

var errResponseLimit error = &publicFailure{"GraphQL response size limit exceeded", "LIMIT_EXCEEDED"}

func marshalReply(ctx context.Context, reply gqlReply) ([]byte, error) {
	if len(reply.Errors) > 0 {
		reply.Errors = codedGraphQLErrors(reply.Errors, "GRAPHQL_ERROR")
	}
	limit := ResponseLimit(ctx)
	if limit == 0 {
		return reply.MarshalJSON()
	}
	if reply.raw != nil && len(reply.Errors) == 0 && !reply.nullData && len(reply.Extensions) == 0 {
		dataSize := len(reply.raw)
		if dataSize == 0 {
			dataSize = 2
		}
		if int64(dataSize+9) > limit {
			return nil, errResponseLimit
		}
		return reply.MarshalJSON()
	}
	value := replyWireValue(reply)
	var buffer bytes.Buffer
	writer := &limitWriter{Writer: &buffer, remaining: limit + 1}
	if err := jsonReply.NewEncoder(writer).Encode(value); err != nil {
		if errors.Is(err, errResponseLimit) {
			return nil, errResponseLimit
		}
		return nil, err
	}
	body := bytes.TrimSuffix(buffer.Bytes(), []byte{'\n'})
	if int64(len(body)) > limit {
		return nil, errResponseLimit
	}
	return body, nil
}

func replyWireValue(reply gqlReply) any {
	if reply.nullData {
		return struct {
			Data       any                    `json:"data"`
			Errors     any                    `json:"errors,omitempty"`
			Extensions map[string]interface{} `json:"extensions,omitempty"`
		}{nil, reply.Errors, reply.Extensions}
	}
	if reply.raw != nil && len(reply.Errors) == 0 {
		data := reply.raw
		if len(data) == 0 {
			data = []byte(`{}`)
		}
		return struct {
			Data       stdjson.RawMessage     `json:"data"`
			Extensions map[string]interface{} `json:"extensions,omitempty"`
		}{data, reply.Extensions}
	}
	type wireReply gqlReply
	return wireReply(reply)
}

type limitWriter struct {
	io.Writer
	remaining int64
}

func (writer *limitWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > writer.remaining {
		return 0, errResponseLimit
	}
	n, err := writer.Writer.Write(data)
	writer.remaining -= int64(n)
	return n, err
}
