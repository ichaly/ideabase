package gql

import (
	"context"
	"encoding/json"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"gorm.io/gorm"
)

type (
	gqlRequest struct {
		Query         string
		OperationName string
		Variables     map[string]interface{}
	}
	gqlResult struct {
		sql    string
		args   []any
		Data   map[string]interface{} `json:"data,omitempty"`
		Errors gqlerror.List          `json:"errors,omitempty"`
	}
	gqlValue struct {
		value interface{}
		name  string
		err   error
	}
)

type Executor struct {
	db       *gorm.DB
	intro    interface{}
	schema   *ast.Schema
	compiler *Compiler
}

func NewExecutor(d *gorm.DB, s *ast.Schema, c *Compiler) (*Executor, error) {
	return &Executor{db: d, intro: intro.New(s), schema: s, compiler: c}, nil
}

func (my *Executor) Execute(ctx context.Context, query string, variables json.RawMessage) (r gqlResult) {
	doc, err := gqlparser.LoadQuery(my.schema, query)
	if err != nil {
		r.Errors = err
		return
	}
	//resultChans := make([]<-chan gqlValue, 0, len(set))
	for _, operation := range doc.Operations {
		r.sql, r.args = my.compiler.Compile(operation, variables)
		e := my.db.Raw(r.sql, r.args...).Scan(&r.Data).Error
		if e != nil {
			r.Errors = append(r.Errors, gqlerror.Wrap(e))
		}
	}
	return
}
