package graphql

import (
	"github.com/samber/lo"
	"github.com/vektah/gqlparser/v2/ast"
)

func (my *compilerContext) renderUpdate(id, pid int, f *ast.Field) {
	update := f.Arguments.ForName(UPDATE)
	table, _ := my.meta.TableName(f.Definition.Type.Name(), false)

	children := lo.Filter(update.Value.Children, func(item *ast.ChildValue, index int) bool {
		return item.Value.Definition.Kind == ast.Scalar
	})

	my.Quoted(table)
	my.Space(`AS (UPDATE`)
	my.Quoted(table)
	my.Space(`SET (`)
	for i, v := range children {
		if i != 0 {
			my.Write(`,`)
		}
		my.Quoted(v.Name)
	}
	my.Space(`) = (SELECT`)
	for i, v := range children {
		if i != 0 {
			my.Write(`,`)
		}
		raw, _ := v.Value.Value(my.variables)
		my.Wrap(`'`, raw)
		my.Write(`::`)
		my.Write("text") //TODO:需要转化为数据库对应的具体类型
	}
	my.Space(`)`)

	my.renderWhereField(f)
	my.Space(`RETURNING`)
	my.Quoted(table)
	my.Write(`.* ) `)
}
