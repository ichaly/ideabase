package graphql

import (
	"github.com/duke-git/lancet/v2/convertor"
	"github.com/duke-git/lancet/v2/maputil"
	"github.com/ichaly/ideabase/graphql/internal"
	"github.com/ichaly/ideabase/utility"
	"github.com/samber/lo"
	"github.com/vektah/gqlparser/v2/ast"
	"strings"
)

type insertItem struct {
	index   int
	field   *internal.Field
	value   *ast.Value
	parents []*internal.Entry
}

func (my *compilerContext) renderInsert(id, pid int, f *ast.Field) {
	insert := f.Arguments.ForName(INSERT)
	result := my.parseValue(insert.Value, nil)
	union := make(map[string][]string)

	//定义CTE进行数据插入
	for index, value := range result {
		class := strings.TrimSuffix(value.value.Definition.Name, SUFFIX_INSERT_INPUT)
		table, _ := my.meta.TableName(class, false)
		alias := utility.JoinString(table, `_`, convertor.ToString(index))
		union[table] = append(maputil.GetOrSet(union, table, []string{}), alias)

		children := lo.Filter(lo.Map(value.value.Children, func(item *ast.ChildValue, index int) insertItem {
			field, _ := my.meta.FindField(class, item.Name, false)
			return insertItem{index: index, field: field, value: item.Value}
		}), func(item insertItem, index int) bool {
			return item.field != nil && item.field.Kind == NONE
		})

		my.Quoted(alias)
		my.Space(`AS (INSERT INTO`)
		my.Quoted(table)

		my.Write(` (`)
		for i, v := range children {
			if i != 0 {
				my.Write(`,`)
			}
			my.Quoted(v.field.Column)
		}
		if len(value.parents) > 0 {
			for _, v := range value.parents {
				my.Write(`,`)
				my.Quoted(v.ColumnName)
			}
		}
		my.Write(`) SELECT `)
		for i, v := range children {
			if i != 0 {
				my.Write(`,`)
			}
			raw, _ := v.value.Value(my.variables)
			my.Wrap(`'`, raw)
			my.Write(`::`)
			my.Write(v.field.DataType)
		}
		if len(value.parents) > 0 {
			for _, v := range value.parents {
				my.Write(`,`)
				my.Quoted(utility.JoinString(v.TableRelation, `_0`))
				my.Write(`.`)
				my.Quoted(v.ColumnRelation)
			}
			my.Space(`FROM`)
			for i, v := range value.parents {
				if i != 0 {
					my.Write(`,`)
				}
				my.Quoted(utility.JoinString(v.TableRelation, `_0`))
			}
		}
		my.Space(`RETURNING`)
		my.Quoted(table)
		my.Write(`.* ),`)
	}

	//将所有关联的表最后拼接成一个和原表同名的临时表
	keys := maputil.Keys(union)
	for i, k := range keys {
		if i != 0 {
			my.Write(`,`)
		}
		my.Quoted(k)
		my.Space(`AS (`)
		for j, v := range union[k] {
			if j != 0 {
				my.Space(`UNION ALL`)
			}
			my.Space(`SELECT * FROM`)
			my.Quoted(v)
		}
		my.Write(`)`)
	}
}

func (my *compilerContext) parseValue(value *ast.Value, parents ...*internal.Entry) (result []*insertItem) {
	parents = lo.Filter(parents, func(item *internal.Entry, index int) bool {
		return item != nil
	})
	result = append(result, &insertItem{value: value, parents: parents})
	for _, v := range value.Children {
		if v.Value.Definition.Kind == ast.InputObject {
			class := strings.TrimSuffix(value.Definition.Name, SUFFIX_INSERT_INPUT)
			field, _ := my.meta.FindField(class, v.Name, false)
			//TODO:MANY_TO_MANY
			if v.Value.Kind == ast.ListValue {
				for _, c := range v.Value.Children {
					result = append(result, my.parseValue(c.Value, field.Link, field.Join)...)
				}
			} else {
				result = append(result, my.parseValue(v.Value, field.Link, field.Join)...)
			}
		}
	}
	return
}
