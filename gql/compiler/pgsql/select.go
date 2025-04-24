// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/vektah/gqlparser/v2/ast"
)

// BuildQuery 构建查询语句
func (my *Dialect) BuildQuery(cpl *gql.Compiler, set ast.SelectionSet) error {
	if len(set) == 0 {
		return fmt.Errorf("empty selection set")
	}

	if err := my.buildRoot(cpl, set); err != nil {
		return err
	}

	my.buildJson(cpl, set)
	return nil
}

func (my *Dialect) buildRoot(cpl *gql.Compiler, set ast.SelectionSet) error {
	cpl.Write(`SELECT JSONB_BUILD_OBJECT(`)
	for i, s := range set {
		field, ok := s.(*ast.Field)
		if !ok {
			return fmt.Errorf("selection must be a field")
		}
		if i != 0 {
			cpl.SpaceAfter(`,`)
		}
		cpl.Write(`'`, field.Name, `', __sj_`, i, `.json`)
	}
	cpl.Write(`) AS "__root" FROM (SELECT TRUE) AS "__root_x"`)
	return nil
}

func (my *Dialect) buildJson(cpl *gql.Compiler, set ast.SelectionSet) {
	for i, s := range set {
		field := s.(*ast.Field)
		cpl.SpaceBefore(`LEFT OUTER JOIN LATERAL (`)
		cpl.Write(`SELECT COALESCE(JSONB_AGG(__sj_`, i, `.json), '[]') AS "json" FROM (`)
		cpl.Write(`SELECT TO_JSONB(__sr_`, i, `.*) AS "json" FROM (`)

		my.buildSelect(cpl, field, i, "0")
		if len(field.SelectionSet) > 0 {
			my.buildJson(cpl, field.SelectionSet)
		}

		cpl.SpaceAfter(`) AS`).Quote(`__sr_`, i)
		cpl.SpaceAfter(`) AS`).Quote(`__sj_`, i)
		cpl.SpaceAfter(`) AS`).Quote(`__sj_`, i).Write(` ON TRUE`)
	}
}

func (my *Dialect) buildSelect(cpl *gql.Compiler, field *ast.Field, index int, parent string) {
	table, ok := cpl.TableName(field.Definition.Type.Name())
	if !ok {
		return
	}
	alias := strings.Join([]string{table, parent, strconv.Itoa(index)}, "_")
	// 外层别名查询
	cpl.SpaceAfter(`SELECT`)
	for i, s := range field.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			field, ok := cpl.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			if i != 0 {
				cpl.SpaceAfter(`,`)
			}
			cpl.Quote(alias).Write(".").Quote(field.Column)
			cpl.Space(`AS`).Quote(f.Alias)
		}
	}
	cpl.Space(`FROM (SELECT`)
	// 内层原生查询
	for i, s := range field.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			field, ok := cpl.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			if i != 0 {
				cpl.SpaceAfter(`,`)
			}
			cpl.Quote(table).Write(".").Quote(field.Column)
			cpl.Space(`AS`).Quote(f.Alias)
		}
	}
	cpl.Space("FROM").Write(table).Space(`) AS`).Quote(alias)
}
