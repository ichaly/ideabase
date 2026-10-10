package gql

import (
	"context"
	"time"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

type Phase string

const (
	PhaseExecute   Phase = "execute"
	PhasePlan      Phase = "plan"
	PhaseAuthorize Phase = "authorize"
	PhaseDatabase  Phase = "database"
	PhaseHandler   Phase = "handler"
)

// Observation intentionally excludes query text, variables and identity.
// Code is SUCCESS or a stable public error code; arbitrary error text is never emitted.
type Observation struct {
	Phase    Phase
	Duration time.Duration
	Code     string
}

type Observer interface {
	Observe(context.Context, Observation)
}

func (my *Engine) startPhase(ctx context.Context, phase Phase) func(string) {
	if my == nil || my.observer == nil {
		return nil
	}
	started, observer := time.Now(), my.observer
	return func(code string) {
		if code == "" {
			code = "SUCCESS"
		}
		func() {
			defer func() { _ = recover() }()
			observer.Observe(ctx, Observation{Phase: phase, Duration: time.Since(started), Code: code})
		}()
	}
}

func observationErrorCode(err error) string {
	if err == nil {
		return "SUCCESS"
	}
	if _, ok := err.(*introQuery); ok {
		return "SUCCESS"
	}
	if _, ok := err.(*rootResolverQuery); ok {
		return "SUCCESS"
	}
	if list, ok := err.(gqlerror.List); ok && len(list) > 0 {
		if code, _ := list[0].Extensions["code"].(string); code != "" {
			return code
		}
		return "REQUEST_ERROR"
	}
	if coded, ok := err.(errorCoder); ok {
		return coded.ErrorCode()
	}
	if safe := safeFailure(err); safe != nil {
		return safe.(errorCoder).ErrorCode()
	}
	return "ERROR"
}

func observationReplyCode(reply gqlReply, err error) string {
	if err != nil {
		return observationErrorCode(err)
	}
	if len(reply.Errors) == 0 {
		return "SUCCESS"
	}
	if code, _ := reply.Errors[0].Extensions["code"].(string); code != "" {
		return code
	}
	return "GRAPHQL_ERROR"
}
