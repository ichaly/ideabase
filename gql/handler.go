package gql

import (
	"context"
	"fmt"

	"github.com/vektah/gqlparser/v2/ast"
)

const defaultBatchLimit = 1000

func (my *Engine) executionContextFor(ctx context.Context, selection ast.SelectionSet, coordinate string) context.Context {
	if my.validator != nil {
		ctx = context.WithValue(ctx, inputValidatorKey{}, my.validator)
	}
	fingerprint := ""
	if my.snapshot != nil {
		fingerprint = my.snapshot.Fingerprint()
		if capability, ok := my.snapshot.Capability(coordinate); ok {
			return withExecutionContext(ctx, selection, fingerprint, my.database, capability)
		}
	}
	return withExecutionContext(ctx, selection, fingerprint, my.database)
}

func handlerContext(ctx context.Context, handler Resolver) (context.Context, context.CancelFunc) {
	if timeout := handler.Define().Timeout; timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return ctx, func() {}
}

func handlerResult(ctx context.Context, err error) error {
	if err != nil {
		return err
	}
	return ctx.Err()
}

func handlerBatchLimit(handler Resolver) int {
	if limit := handler.Define().MaxBatch; limit > 0 {
		return limit
	}
	return defaultBatchLimit
}

func validateHandler(handler Resolver) error {
	d := handler.Define()
	if d.Class == "" || d.Name == "" || d.Result == "" {
		return fmt.Errorf("handler声明不完整: %s", handler.Name())
	}
	if d.Timeout < 0 || d.MaxBatch < 0 {
		return fmt.Errorf("handler %s的timeout/maxBatch不能为负数", handler.Name())
	}
	if d.RetrySafe && d.IdempotencyKey == "" {
		return fmt.Errorf("handler %s声明RetrySafe必须提供幂等键", handler.Name())
	}
	if !d.RetrySafe && d.IdempotencyKey != "" {
		return fmt.Errorf("handler %s未声明RetrySafe却提供了幂等键", handler.Name())
	}
	if (d.Class == "Query" || d.Class == "Mutation") && len(d.Requires) > 0 {
		return fmt.Errorf("根handler %s不能声明source依赖", handler.Name())
	}
	return nil
}
