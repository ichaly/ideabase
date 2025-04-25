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

		// 判断是否为列表查询
		isListQuery := !strings.HasSuffix(field.Definition.Type.Name(), "Page") && field.Arguments.ForName("id") == nil

		if isListQuery {
			cpl.Write(`SELECT COALESCE(JSONB_AGG(TO_JSONB(__sr_`, i, `.*)), '[]') -> 0 AS json FROM (`)
		} else {
			cpl.Write(`SELECT JSONB_BUILD_OBJECT('items', COALESCE(JSONB_AGG(TO_JSONB(__sr_`, i, `.*)), '[]')`)
			// 检查是否有total字段
			hasTotal := false
			for _, sel := range field.SelectionSet {
				if f, ok := sel.(*ast.Field); ok && f.Name == "total" {
					hasTotal = true
					break
				}
			}
			if hasTotal {
				cpl.Write(`, 'total', COUNT(*) OVER()`)
			}
			cpl.Write(`) AS json FROM (`)
		}

		my.buildSelect(cpl, field, i, "0")
		if len(field.SelectionSet) > 0 {
			my.buildJson(cpl, field.SelectionSet)
		}

		cpl.SpaceAfter(`) AS`).Quote(`__sr_`, i)
		cpl.SpaceAfter(`) AS`).Quote(`__sj_`, i).Write(` ON TRUE`)
	}
}

func (my *Dialect) buildSelect(cpl *gql.Compiler, field *ast.Field, index int, parent string) {
	table, ok := cpl.TableName(field.Definition.Type.Name())
	if !ok {
		return
	}

	// 判断是否为分页查询
	isPage := strings.HasSuffix(field.Definition.Type.Name(), "Page")

	alias := strings.Join([]string{table, parent, strconv.Itoa(index)}, "_")
	cpl.SpaceAfter(`SELECT`)

	// 处理字段选择
	my.buildFields(cpl, field, alias)

	// 处理FROM子句
	cpl.Space(`FROM (SELECT`)
	my.buildSourceFields(cpl, field, table)
	cpl.Space("FROM").Write(table).Space(`) AS`).Quote(alias)

	// 处理WHERE条件
	if arg := field.Arguments.ForName("id"); arg != nil {
		cpl.Space(`WHERE`).Quote(alias).Write(`.id = `).Write(arg.Value.Raw)
	}

	// 处理分页查询
	if isPage {
		_ = my.buildPagination(cpl, field.Arguments) // 使用dialect.go中的实现
	}
}

// 构建选择字段
func (my *Dialect) buildFields(cpl *gql.Compiler, field *ast.Field, alias string) {
	hasItems := false
	for i, s := range field.SelectionSet {
		if i != 0 && !hasItems {
			cpl.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			if f.Name == "items" {
				hasItems = true
				my.buildItemsField(cpl, f, alias)
				continue
			}
			if hasItems {
				continue
			}
			field, ok := cpl.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			cpl.Quote(alias).Write(".").Quote(field.Column)
			cpl.Space(`AS`).Quote(f.Alias)
		}
	}
}

// 构建items字段
func (my *Dialect) buildItemsField(cpl *gql.Compiler, field *ast.Field, alias string) {
	cpl.Write(`JSONB_BUILD_ARRAY(`)
	for i, s := range field.SelectionSet {
		if i != 0 {
			cpl.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			field, ok := cpl.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			cpl.Quote(alias).Write(".").Quote(field.Column)
		}
	}
	cpl.Write(`) AS items`)
}

// 构建源字段
func (my *Dialect) buildSourceFields(cpl *gql.Compiler, field *ast.Field, table string) {
	hasItems := false
	for i, s := range field.SelectionSet {
		if i != 0 && !hasItems {
			cpl.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			if f.Name == "items" {
				hasItems = true
				my.buildSourceItemsFields(cpl, f, table)
				continue
			}
			if hasItems {
				continue
			}
			field, ok := cpl.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			cpl.Quote(table).Write(".").Quote(field.Column)
			cpl.Space(`AS`).Quote(f.Alias)
		}
	}
}

// buildSourceItemsFields 构建源items字段
func (my *Dialect) buildSourceItemsFields(cpl *gql.Compiler, field *ast.Field, table string) {
	for i, s := range field.SelectionSet {
		if i != 0 {
			cpl.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			field, ok := cpl.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			cpl.Quote(table).Write(".").Quote(field.Column)
			cpl.Space(`AS`).Quote(f.Alias)
		}
	}
}
