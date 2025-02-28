package gql

import "github.com/vektah/gqlparser/v2/ast"

func (my *compilerContext) renderMutation(set ast.SelectionSet) {
	my.Write(`WITH `)
	for i, s := range set {
		switch f := s.(type) {
		case *ast.Field:
			id := my.fieldId(f)
			insert := f.Arguments.ForName(INSERT)
			update := f.Arguments.ForName(UPDATE)
			upsert := f.Arguments.ForName(UPSERT)
			remove := f.Arguments.ForName(REMOVE)
			if i != 0 && (insert != nil || update != nil || upsert != nil || remove != nil) {
				my.Write(`,`)
			}
			if insert != nil {
				my.renderInsert(id, 0, f)
			} else if update != nil {
				my.renderUpdate(id, 0, f)
			} else if upsert != nil {
				my.renderUpsert(id, 0, f)
			} else if remove != nil {
				my.renderRemove(id, 0, f)
			}
		}
	}
	my.renderQuery(set)
}

func (my *compilerContext) renderRemove(id, pid int, f *ast.Field) {
	table, _ := my.meta.TableName(f.Definition.Type.Name(), false)
	my.Quoted(table)
	my.Space(`AS (DELETE FROM`)
	my.Quoted(table)
	my.renderWhereField(f)
	my.Space(`RETURNING`)
	my.Quoted(table)
	my.Write(`.* ) `)
}
