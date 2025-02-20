package gql

import (
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/samber/lo"
	"github.com/vektah/gqlparser/v2/ast"
)

type upsertItem struct {
	index int
	field *internal.Field
	child *ast.ChildValue
}

func (my *compilerContext) renderUpsert(id, pid int, f *ast.Field) {
	upsert := f.Arguments.ForName(UPSERT)
	where := f.Arguments.ForName(WHERE)
	if upsert == nil || where == nil {
		return
	}
	class := f.Definition.Type.Name()
	table, _ := my.meta.TableName(f.Definition.Type.Name(), false)
	children := lo.Map(upsert.Value.Children, func(item *ast.ChildValue, index int) upsertItem {
		field, _ := my.meta.FindField(class, item.Name, false)
		return upsertItem{index: index, field: field, child: item}
	})

	my.Quoted(table)
	my.Space(`AS (INSERT INTO`)
	my.Quoted(table)
	my.Space(`(`)
	for i, v := range children {
		if i != 0 {
			my.Write(`,`)
		}
		my.Quoted(v.child.Name)
	}
	my.Write(`) SELECT `)
	for i, v := range children {
		if i != 0 {
			my.Write(`,`)
		}
		raw, _ := v.child.Value.Value(my.variables)
		my.Wrap(`'`, raw)
		my.Write(`::`)
		my.Write(v.field.DataType)
	}
	my.Space(`ON CONFLICT (id) DO UPDATE SET`)
	for i, v := range upsert.Value.Children {
		if i != 0 {
			my.Write(`,`)
		}
		my.Write(v.Name)
		my.Space(`=`)
		my.Write(`EXCLUDED.`)
		my.Write(v.Name)
	}
	my.renderWhereField(f)
	my.Space(`RETURNING`)
	my.Quoted(table)
	my.Write(`.* ) `)
}
