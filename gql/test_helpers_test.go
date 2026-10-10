package gql

import (
	"context"

	"github.com/vektah/gqlparser/v2/gqlerror"
)

func (my *Compiler) freeze(metadata *Metadata) (*Compiler, error) {
	frozen := *my
	frozen.meta = metadata
	fingerprint, err := metadata.CatalogFingerprint()
	if err != nil {
		return nil, err
	}
	frozen.cursorFingerprint = fingerprint
	return &frozen, nil
}

func (my *Engine) runOperation(ctx context.Context, operationName string, variables map[string]interface{}) gqlReply {
	query, ok := my.documents[operationName]
	if !ok {
		return gqlReply{Errors: gqlerror.List{gqlerror.Errorf("未找到名为'%s'的持久化操作", operationName)}}
	}
	return my.queryData(ctx, query.query, variables, operationName)
}

func (my *Engine) plan(query, operationName string, variables map[string]interface{}) (*Plan, error) {
	return my.planWithView(&effectiveView{schema: my.schema}, query, operationName, variables)
}
