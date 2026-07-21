package compiler

import "github.com/vektah/gqlparser/v2/ast"

// FieldsOf 选择集中的纯字段列表（fragment已由上游inline展开）
func FieldsOf(set ast.SelectionSet) []*ast.Field {
	fields := make([]*ast.Field, 0, len(set))
	for _, selection := range set {
		if field, ok := selection.(*ast.Field); ok {
			fields = append(fields, field)
		}
	}
	return fields
}
