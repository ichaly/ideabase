package gql

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/duke-git/lancet/v2/maputil"
	"github.com/vektah/gqlparser/v2/ast"
)

type Compiler struct {
	meta *Metadata
}

func NewCompiler(m *Metadata) *Compiler {
	return &Compiler{meta: m}
}
func (my *Compiler) Compile(operation *ast.OperationDefinition, variables json.RawMessage) (string, []any) {
	c := newContext(my.meta)
	c.Render(operation, variables)
	return c.String(), c.params
}

type compilerContext struct {
	buf        *bytes.Buffer
	meta       *Metadata
	params     []any
	variables  map[string]interface{}
	dictionary map[int]int
}

func newContext(m *Metadata) *compilerContext {
	return &compilerContext{meta: m, buf: bytes.NewBuffer([]byte{}), dictionary: make(map[int]int), variables: make(map[string]interface{})}
}

func (my *compilerContext) Wrap(with string, list ...any) *compilerContext {
	my.Write(with)
	my.Write(list...)
	my.Write(with)
	return my
}

func (my *compilerContext) Write(list ...any) *compilerContext {
	for _, e := range list {
		my.buf.WriteString(fmt.Sprint(e))
	}
	return my
}

func (my *compilerContext) Space(list ...any) *compilerContext {
	my.Wrap(` `, list...)
	return my
}

func (my *compilerContext) Quoted(list ...any) *compilerContext {
	my.Wrap(`"`, list...)
	return my
}

func (my *compilerContext) String() string {
	return strings.TrimSpace(my.buf.String())
}

func (my *compilerContext) Render(operation *ast.OperationDefinition, variables json.RawMessage) {
	_ = json.Unmarshal(variables, &my.variables)
	switch operation.Operation {
	case ast.Query, ast.Subscription:
		my.renderQuery(operation.SelectionSet)
	case ast.Mutation:
		my.renderMutation(operation.SelectionSet)
	}
}

func (my *compilerContext) fieldId(field *ast.Field) int {
	p := field.GetPosition()
	id := p.Line<<32 | p.Column
	return maputil.GetOrSet(my.dictionary, id, len(my.dictionary))
}

func (my *compilerContext) renderParam(value *ast.Value) {
	val, err := value.Value(my.variables)
	if err != nil {
		my.params = append(my.params, nil)
	} else {
		my.params = append(my.params, val)
	}
	my.Write(`?`)
}
