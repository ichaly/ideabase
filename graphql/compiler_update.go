package graphql

import (
	"github.com/ichaly/ideabase/graphql/internal"
	"github.com/samber/lo"
	"github.com/vektah/gqlparser/v2/ast"
	"strings"
)

type updateItem struct {
	index int
	field *internal.Field
	child *ast.ChildValue
}

func (my *compilerContext) renderUpdate(id, pid int, f *ast.Field) {
	update := f.Arguments.ForName(UPDATE)
	class := strings.TrimSuffix(update.Value.Definition.Name, SUFFIX_UPDATE_INPUT)
	table, _ := my.meta.TableName(class, false)

	children := lo.Map(lo.Filter(update.Value.Children, func(item *ast.ChildValue, index int) bool {
		return item.Value.Definition.Kind == ast.Scalar
	}), func(item *ast.ChildValue, index int) updateItem {
		field, _ := my.meta.FindField(class, item.Name, false)
		return updateItem{index: index, field: field, child: item}
	})

	my.Quoted(table)
	my.Space(`AS (UPDATE`)
	my.Quoted(table)
	my.Space(`SET (`)
	for i, v := range children {
		if i != 0 {
			my.Write(`,`)
		}
		my.Quoted(v.child.Name)
	}
	my.Space(`) = (SELECT`)
	for i, v := range children {
		if i != 0 {
			my.Write(`,`)
		}
		raw, _ := v.child.Value.Value(my.variables)
		my.Wrap(`'`, raw)
		my.Write(`::`)
		my.Write(v.field.DataType)
	}
	my.Space(`)`)

	my.renderWhereField(f)
	my.Space(`RETURNING`)
	my.Quoted(table)
	my.Write(`.* ) `)
}
