package gql

import (
	"strconv"
	"strings"

	"github.com/duke-git/lancet/v2/convertor"
	"github.com/ichaly/ideabase/utl"
	"github.com/vektah/gqlparser/v2/ast"
)

func (my *compilerContext) renderQuery(set ast.SelectionSet) {
	my.Write(`SELECT jsonb_build_object(`)
	my.eachField(set, func(index int, field *ast.Field) {
		if index != 0 {
			my.Write(`,`)
		}
		id := my.fieldId(field)
		my.Write(`'`, field.Name, `', __sj_`, id, `.json`)
	})
	my.Write(`) AS __root FROM (SELECT true) AS __root_x`)
	my.renderField(0, set)
}

func (my *compilerContext) eachField(set ast.SelectionSet, callback func(index int, field *ast.Field)) {
	for i, s := range set {
		switch t := s.(type) {
		case *ast.Field:
			_, ok := my.meta.FindClass(t.Definition.Type.Name(), false)
			if ok && callback != nil {
				callback(i, t)
			}
		}
	}
}

func (my *compilerContext) renderField(pid int, set ast.SelectionSet) {
	my.eachField(set, func(index int, field *ast.Field) {
		id := my.fieldId(field)

		my.renderJoin(id)
		my.renderList(id, field)
		my.renderJson(id, field)

		my.renderSelect(id, pid, field)
		if len(field.SelectionSet) > 0 {
			my.renderField(id, field.SelectionSet)
		}

		my.renderJsonClose(id)
		my.renderListClose(id)
		my.renderJoinClose(id)
	})
}

func (my *compilerContext) renderJoin(id int) {
	my.Write(` LEFT OUTER JOIN LATERAL (`)
}

func (my *compilerContext) renderJoinClose(id int) {
	my.Write(` ) AS `).Quoted(`__sj_`, id).Write(` ON true `)
}

func (my *compilerContext) renderList(id int, field *ast.Field) {
	my.Write(` SELECT COALESCE(jsonb_agg(__sj_`, id, `.json), '[]') AS json FROM ( `)
}

func (my *compilerContext) renderListClose(id int) {
	my.Write(` ) AS `).Quoted(`__sj_`, id)
}

func (my *compilerContext) renderJson(id int, field *ast.Field) {
	my.Write(` SELECT to_jsonb(__sr_`, id, `.*) AS json FROM ( `)
}

func (my *compilerContext) renderJsonClose(id int) {
	my.Write(` ) AS `).Quoted(`__sr_`, id)
}

func (my *compilerContext) renderSelect(id, pid int, f *ast.Field) {
	table, ok := my.meta.TableName(f.Definition.Type.Name(), false)
	if !ok {
		return
	}

	alias := utl.JoinString(table, "_", convertor.ToString(id))
	my.Write(` SELECT `)
	my.renderDistinct(id, pid, f)
	for i, s := range f.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)
			if !ok {
				continue
			}
			if i != 0 {
				my.Write(",")
			}
			if len(field.Kind) > 0 {
				my.Quoted("__sj_", my.fieldId(f))
				my.Write(".").Quoted("json")
			} else {
				my.Quoted(alias)
				my.Write(".")
				my.Quoted(field.Column)
			}
			my.Write(` AS `)
			my.Quoted(f.Alias)
		}
	}
	my.Write(` FROM (`)
	field, ok := my.meta.FindField(f.Definition.Type.Name(), f.Name, false)
	if ok && field.Kind == RECURSIVE {
		my.renderRecursiveSelect(id, pid, f)
	} else {
		my.renderUniversalSelect(id, pid, f)
	}
	my.renderSort(f)
	my.renderLimit(f)
	my.renderOffset(f)
	my.Write(` ) AS `)
	my.Quoted(alias)
}

func (my *compilerContext) renderDistinct(id, pid int, f *ast.Field) {
	distinct := f.Arguments.ForName(DISTINCT)
	if distinct == nil {
		return
	}
	data, err := distinct.Value.Value(nil)
	if err != nil {
		return
	}
	list, ok := data.([]interface{})
	if !ok || len(list) == 0 {
		return
	}
	my.Write(`DISTINCT ON (`)
	for i, v := range list {
		if i != 0 {
			my.Write(`, `)
		}
		field, ok := my.meta.FindField(f.Definition.Type.Name(), convertor.ToString(v), false)
		if !ok {
			continue
		}
		my.Quoted(utl.JoinString(field.Table, "_", convertor.ToString(id)))
		my.Write(".")
		my.Quoted(field.Column)
	}
	my.Write(`) `)
}

func (my *compilerContext) renderInner(id, pid int, f *ast.Field) {
	field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)
	if !ok || field.Join == nil {
		return
	}
	join := field.Join
	my.Write(` INNER JOIN `)
	my.Write(join.TableName)
	my.Write(` ON ((`)
	my.Quoted(join.TableName)
	my.Write(` . `)
	my.Quoted(join.ColumnName)
	my.Write(` = `)
	my.Quoted(utl.JoinString(join.TableRelation, "_", convertor.ToString(pid)))
	my.Write(` . `)
	my.Quoted(join.ColumnRelation)
	my.Write(`))`)
}

func (my *compilerContext) renderWhere(id, pid int, f *ast.Field) {
	field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)

	//TODO:处理关联关系的查询条件，有优化空间？
	if ok && field.Link != nil {
		//组装关联条件
		link := field.Link
		var relation string
		if field.Kind == MANY_TO_MANY {
			relation = link.TableRelation
		} else {
			relation = utl.JoinString(link.TableRelation, "_", convertor.ToString(pid))
		}
		value := &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{
			Name: utl.JoinString(`"`, link.TableName, `"."`, link.ColumnName, `"`),
			Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{
				Name: EQ, Value: &ast.Value{
					Kind: ast.BlockValue,
					Raw:  utl.JoinString(`"`, relation, `"."`, link.ColumnRelation, `"`),
				},
			}}},
		}}}
		//拼接原始条件
		my.appendWhereValue(f, value)
	}

	//编译WHERE
	my.renderWhereField(f)
}

func (my *compilerContext) renderUniversalSelect(id, pid int, f *ast.Field) {
	table, _ := my.meta.TableName(f.Definition.Type.Name(), false)

	my.Write(` SELECT `)
	for i, s := range f.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			_field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)
			if !ok || len(_field.Table) == 0 || len(_field.Column) == 0 {
				continue
			}
			if i != 0 {
				my.Write(",")
			}
			my.Quoted(_field.Table)
			my.Write(".")
			my.Quoted(_field.Column)
		}
	}
	my.Write(` FROM `)
	my.Quoted(table)

	my.renderInner(id, pid, f)
	my.renderWhere(id, pid, f)
}

func (my *compilerContext) renderRecursiveSelect(id, pid int, f *ast.Field) {
	field, _ := my.meta.FindField(f.Definition.Type.Name(), f.Name, false)
	table, column := field.Link.TableName, field.Link.ColumnName
	alias := utl.JoinString("__rcte_", table)

	my.Write(` WITH RECURSIVE `)
	my.Quoted(alias)
	my.Write(` AS ((SELECT `)
	for _, s := range f.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			_field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)
			if !ok || len(_field.Table) == 0 || len(_field.Column) == 0 {
				continue
			}
			my.Quoted(_field.Table)
			my.Write(".")
			my.Quoted(_field.Column)
			my.Write(",")
		}
	}

	if "children" == f.Name {
		my.Quoted(field.Link.TableName).Write(".").Quoted(field.Link.ColumnName)
	} else {
		my.Quoted(field.Link.TableRelation).Write(".").Quoted(field.Link.ColumnRelation)
	}

	my.Write(" FROM ").Quoted(table).Write(` WHERE `)

	if "children" == f.Name {
		my.Quoted(table).Write(".").Write(field.Link.ColumnRelation)
		my.Write(" = ")
		my.Quoted(table, "_", pid).Write(".").Write(field.Link.ColumnRelation)
	} else {
		my.Quoted(table).Write(".").Write(field.Link.ColumnName)
		my.Write(" = ")
		my.Quoted(table, "_", pid).Write(".").Write(field.Link.ColumnName)
	}

	my.Write(` LIMIT 1 ) UNION ALL `)

	my.Write(` SELECT `)
	for i, s := range f.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			_field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)
			if !ok || len(_field.Table) == 0 || len(_field.Column) == 0 {
				continue
			}
			if i != 0 {
				my.Write(",")
			}
			my.Quoted(_field.Table)
			my.Write(".")
			my.Quoted(_field.Column)
		}
	}

	if "children" == f.Name {
		my.Write(",").Quoted(table).Write(".").Quoted(field.Link.ColumnName).Write(" FROM ").Quoted(table)
	} else {
		my.Write(",").Quoted(table).Write(".").Quoted(field.Link.ColumnRelation).Write(" FROM ").Quoted(table)
	}

	my.Write(" , ")
	my.Quoted(alias)

	//拼接并编译条件
	var children []*ast.ChildValue
	if "children" == f.Name {
		key := utl.JoinString(`"`, table, `"."`, column, `"`)
		//条件1
		children = append(children, my.buildChildValue(key, IS, "NOT_NULL", ast.EnumValue))
		//条件2
		children = append(children, my.buildChildValue(key, NE, utl.JoinString(`"`, field.Link.TableRelation, `"."`, field.Link.ColumnRelation, `"`), ast.BlockValue))
		//条件3
		children = append(children, my.buildChildValue(key, EQ, utl.JoinString(`"`, alias, `"."`, field.Link.ColumnRelation, `"`), ast.BlockValue))
	} else {
		key := utl.JoinString(`"`, alias, `"."`, field.Link.ColumnRelation, `"`)
		//条件1
		children = append(children, my.buildChildValue(key, IS, "NOT_NULL", ast.EnumValue))
		//条件2
		children = append(children, my.buildChildValue(key, NE, utl.JoinString(`"`, alias, `"."`, field.Link.ColumnName, `"`), ast.BlockValue))
		//条件3
		children = append(children, my.buildChildValue(key, EQ, utl.JoinString(`"`, field.Link.TableName, `"."`, field.Link.ColumnName, `"`), ast.BlockValue))
	}
	my.appendWhereValue(f, &ast.Value{
		Kind: ast.ObjectValue,
		Children: []*ast.ChildValue{{
			Name: AND,
			Value: &ast.Value{
				Kind:     ast.ListValue,
				Children: children,
			},
		}},
	})
	my.renderWhereField(f)

	my.Write(") SELECT ")

	for i, s := range f.SelectionSet {
		switch f := s.(type) {
		case *ast.Field:
			_field, ok := my.meta.FindField(f.ObjectDefinition.Name, f.Name, false)
			if !ok || len(_field.Table) == 0 || len(_field.Column) == 0 {
				continue
			}
			if i != 0 {
				my.Write(",")
			}
			my.Quoted(_field.Table)
			my.Write(".")
			my.Quoted(_field.Column)
			my.Write(" AS ")
			my.Quoted(_field.Column)
		}
	}
	my.Write(` FROM (SELECT * FROM `)
	my.Quoted(alias)
	my.Write(` OFFSET 1) AS  `)
	my.Quoted(table)
}

func (my *compilerContext) renderSort(field *ast.Field) {
	sort := field.Arguments.ForName(SORT)
	if sort == nil || len(sort.Value.Children) == 0 {
		return
	}
	my.Write(` ORDER BY  `)
	for i, v := range sort.Value.Children {
		if i != 0 {
			my.Write(`, `)
		}
		f, _ := my.meta.FindField(field.Definition.Type.Name(), v.Name, false)
		my.Quoted(f.Table)
		my.Write(".")
		my.Quoted(f.Column)
		my.Write(` `)
		my.Write(strings.ReplaceAll(convertor.ToString(v.Value.Raw), "_", " "))
	}
}

func (my *compilerContext) renderLimit(field *ast.Field) {
	my.Write(` LIMIT `)
	var value *ast.Value
	limit := field.Arguments.ForName(LIMIT)
	if limit != nil {
		value = limit.Value
	} else {
		value = &ast.Value{
			Kind: ast.IntValue,
			Raw:  strconv.Itoa(my.meta.cfg.DefaultLimit),
		}
	}
	my.renderParam(value)
}

func (my *compilerContext) renderOffset(field *ast.Field) {
	offset := field.Arguments.ForName(OFFSET)
	if offset == nil {
		return
	}
	my.Write(` OFFSET `)
	my.renderParam(offset.Value)
}
