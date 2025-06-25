// Package pgsql 实现PostgreSQL的SQL方言
package pgsql

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// BuildQuery 构建查询语句
func (my *Dialect) BuildQuery(ctx *compiler.Context, set ast.SelectionSet) error {
	if len(set) == 0 {
		return fmt.Errorf("empty selection set")
	}

	if err := my.buildRoot(ctx, set); err != nil {
		return err
	}

	my.buildJson(ctx, set)
	return nil
}

func (my *Dialect) buildRoot(ctx *compiler.Context, set ast.SelectionSet) error {
	ctx.Write(`SELECT JSONB_BUILD_OBJECT(`)
	for i, s := range set {
		field, ok := s.(*ast.Field)
		if !ok {
			return fmt.Errorf("selection must be a field")
		}
		if i != 0 {
			ctx.SpaceAfter(`,`)
		}
		ctx.Write(`'`, field.Name, `', __sj_`, i, `."json"`)
	}
	ctx.Write(`) AS "__root" FROM (SELECT TRUE) AS "__root_x"`)
	return nil
}

func (my *Dialect) buildJson(ctx *compiler.Context, set ast.SelectionSet) {
	i := 0 // 关系字段的索引计数器

	for _, s := range set {
		field, ok := s.(*ast.Field)
		if !ok {
			continue
		}

		// 检查是否为关系字段：字段类型对应一个表
		if _, hasTable := ctx.TableName(field.Definition.Type.Name()); !hasTable {
			continue // 跳过标量字段
		}

		// 为关系字段生成JOIN子查询
		ctx.SpaceBefore(`LEFT OUTER JOIN LATERAL (`)

		isSingleQuery := field.Arguments.ForName("id") != nil

		if isSingleQuery {
			ctx.Write(`SELECT COALESCE(JSONB_AGG(TO_JSONB(__sr_`, i, `.*)), '[]') -> 0 AS "json" FROM (`)
		} else {
			ctx.Write(`SELECT JSONB_BUILD_OBJECT('`, gql.ITEMS, `', COALESCE(JSONB_AGG(TO_JSONB(__sr_`, i, `.*)), '[]')`)

			hasTotal := false
			for _, sel := range field.SelectionSet {
				if f, ok := sel.(*ast.Field); ok && f.Name == gql.TOTAL {
					hasTotal = true
					break
				}
			}
			if hasTotal {
				ctx.Write(`, gql.TOTAL, COUNT(*) OVER()`)
			}

			ctx.Write(`) AS "json" FROM (`)
		}

		my.buildSelect(ctx, field, i, "0")
		// 递归处理嵌套的关系字段
		if len(field.SelectionSet) > 0 {
			my.buildJson(ctx, field.SelectionSet)
		}

		ctx.SpaceAfter(`) AS`).Quote(`__sr_`, i)
		ctx.SpaceAfter(`) AS`).Quote(`__sj_`, i).Write(` ON TRUE`)

		i++ // 递增关系字段索引
	}
}

func (my *Dialect) buildSelect(ctx *compiler.Context, field *ast.Field, index int, parent string) {
	table, ok := ctx.TableName(field.Definition.Type.Name())
	if !ok {
		return
	}

	// 判断是否为分页查询
	isPage := strings.HasSuffix(field.Definition.Type.Name(), gql.SUFFIX_PAGE)

	alias := strings.Join([]string{table, parent, strconv.Itoa(index)}, "_")
	ctx.SpaceAfter(`SELECT`)

	// 处理字段选择
	my.buildFields(ctx, field, alias)

	// 处理FROM子句
	ctx.Space(`FROM (SELECT`)
	my.buildSourceFields(ctx, field, table)
	ctx.Space("FROM").Write(table).Space(`) AS`).Quote(alias)

	// 处理WHERE条件
	if arg := field.Arguments.ForName("id"); arg != nil {
		ctx.Space(`WHERE`).Quote(alias).Write(`.ID = `).Write(arg.Value.Raw)
	}

	// 处理分页查询
	if isPage {
		_ = my.buildPagination(ctx, field.Arguments) // 使用dialect.go中的实现
	}
}

// 构建选择字段
func (my *Dialect) buildFields(ctx *compiler.Context, field *ast.Field, alias string) {
	hasItems := false
	for i, s := range field.SelectionSet {
		if i != 0 && !hasItems {
			ctx.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			if f.Name == gql.ITEMS {
				hasItems = true
				my.buildItemsField(ctx, f, alias)
				continue
			}
			if hasItems {
				continue
			}
			field, ok := ctx.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			ctx.Quote(alias).Write(".").Quote(field.Column)
			ctx.Space(`AS`).Quote(f.Alias)
		}
	}
}

// 构建items字段
func (my *Dialect) buildItemsField(ctx *compiler.Context, field *ast.Field, alias string) {
	ctx.Write(`JSONB_BUILD_ARRAY(`)
	for i, s := range field.SelectionSet {
		if i != 0 {
			ctx.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			field, ok := ctx.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			ctx.Quote(alias).Write(".").Quote(field.Column)
		}
	}
	ctx.SpaceAfter(`) AS`).Write(gql.ITEMS)
}

// 构建源字段
func (my *Dialect) buildSourceFields(ctx *compiler.Context, field *ast.Field, table string) {
	hasItems := false
	for i, s := range field.SelectionSet {
		if i != 0 && !hasItems {
			ctx.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			if f.Name == gql.ITEMS {
				hasItems = true
				my.buildSourceItemsFields(ctx, f, table)
				continue
			}
			if hasItems {
				continue
			}
			field, ok := ctx.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			ctx.Quote(table).Write(".").Quote(field.Column)
			ctx.Space(`AS`).Quote(f.Alias)
		}
	}
}

// buildSourceItemsFields 构建源items字段
func (my *Dialect) buildSourceItemsFields(ctx *compiler.Context, field *ast.Field, table string) {
	for i, s := range field.SelectionSet {
		if i != 0 {
			ctx.SpaceAfter(`,`)
		}
		switch f := s.(type) {
		case *ast.Field:
			field, ok := ctx.FindField(f.ObjectDefinition.Name, f.Name)
			if !ok {
				continue
			}
			ctx.Quote(table).Write(".").Quote(field.Column)
			ctx.Space(`AS`).Quote(f.Alias)
		}
	}
}
