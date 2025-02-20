package gql

import (
	"github.com/duke-git/lancet/v2/maputil"
	"github.com/duke-git/lancet/v2/slice"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
	"github.com/samber/lo"
	"github.com/vektah/gqlparser/v2/ast"
)

var inputs = func(name string, ops ...ast.Operation) []*internal.Input {
	data := map[ast.Operation][]*internal.Input{
		ast.Query: {
			{
				Name: DISTINCT,
				Type: ast.ListType(ast.NamedType(SCALAR_STRING, nil), nil),
			},
			{
				Name:    LIMIT,
				Type:    ast.NamedType(SCALAR_INT, nil),
				Default: `20`,
			},
			{
				Name: OFFSET,
				Type: ast.NamedType(SCALAR_INT, nil),
			},
			//{
			//	Name: FIRST,
			//	Type: ast.NamedType(SCALAR_INT, nil),
			//},
			//{
			//	Name: LAST,
			//	Type: ast.NamedType(SCALAR_INT, nil),
			//},
			//{
			//	Name: AFTER,
			//	Type: ast.NamedType(SCALAR_CURSOR, nil),
			//},
			//{
			//	Name: BEFORE,
			//	Type: ast.NamedType(SCALAR_CURSOR, nil),
			//},
			{
				Name: SORT,
				Type: ast.NamedType(utl.JoinString(name, SUFFIX_SORT_INPUT), nil),
			},
			{
				Name: WHERE,
				Type: ast.NamedType(utl.JoinString(name, SUFFIX_WHERE_INPUT), nil),
			},
		},
		ast.Mutation: {
			{
				Name: UPSERT,
				Type: ast.NamedType(utl.JoinString(name, SUFFIX_UPSERT_INPUT), nil),
			},
			{
				Name: INSERT,
				Type: ast.NamedType(utl.JoinString(name, SUFFIX_INSERT_INPUT), nil),
			},
			{
				Name: UPDATE,
				Type: ast.NamedType(utl.JoinString(name, SUFFIX_UPDATE_INPUT), nil),
			},
			{
				Name: REMOVE,
				Type: ast.NamedType(SCALAR_BOOLEAN, nil),
			},
		},
	}

	result := data[ast.Query]
	for _, k := range ops {
		result = append(result, data[k]...)
	}

	return result
}

func (my *Metadata) tableOption() error {
	// 查询表结构
	var list []*internal.Entry
	if err := my.db.Raw(pgsql).Scan(&list).Error; err != nil {
		return err
	}

	data := make(utl.AnyMap[utl.AnyMap[*internal.Entry]])
	//构建节点信息
	for _, r := range list {
		//判断是否包含黑名单关键字,执行忽略跳过
		if slice.ContainBy(my.cfg.BlockList, func(item string) bool {
			return item == r.ColumnName || item == r.TableName
		}) {
			continue
		}

		//规范命名
		table, column := my.Named(r.TableName, r.ColumnName)

		//类型转换
		name := lo.Ternary(r.IsPrimary, SCALAR_ID, my.cfg.Mapping[r.DataType])

		//构建字段
		field := &internal.Field{
			Type:        ast.NamedType(name, nil),
			Name:        column,
			Table:       r.TableName,
			Column:      r.ColumnName,
			DataType:    r.DataType,
			Description: r.ColumnDescription,
		}

		//索引节点
		class := maputil.GetOrSet(my.Nodes, table, &internal.Class{
			Name:        table,
			Kind:        ast.Object,
			Table:       r.TableName,
			Description: r.TableDescription,
			Fields:      make(map[string]*internal.Field),
		})
		class.Fields[column] = field

		//标记主键
		if r.IsPrimary {
			class.Primary = append(class.Primary, column)
		}

		//索引外键
		if r.IsForeign {
			maputil.GetOrSet(data, table, make(map[string]*internal.Entry))[column] = r
		}
	}

	//构建关联信息
	for _, v := range data {
		for k, e := range v {
			currentClass, currentField := my.Named(
				e.TableName, e.ColumnName,
				WithTrimSuffix(),
				NamedRecursion(e, true),
			)
			foreignClass, foreignField := my.Named(
				e.TableRelation,
				e.ColumnRelation,
				WithTrimSuffix(),
				SwapPrimaryKey(currentClass),
				JoinListSuffix(),
				NamedRecursion(e, false),
			)
			var args []*internal.Input
			if e.TableRelation == e.TableName {
				args = append(args, &internal.Input{
					Name:        LEVEL,
					Type:        ast.NamedType(SCALAR_INT, nil),
					Default:     `1`,
					Description: descLevel,
				})
			}
			//OneToMany
			my.Nodes[foreignClass].Fields[foreignField] = &internal.Field{
				Name: foreignField,
				Type: ast.ListType(ast.NamedType(currentClass, nil), nil),
				Kind: lo.Ternary(e.TableRelation == e.TableName, RECURSIVE, ONE_TO_MANY),
				//Link:      e,
				Arguments: append(args, inputs(currentClass)...),
			}
			//ManyToOne
			my.Nodes[currentClass].Fields[currentField] = &internal.Field{
				Name: currentField,
				Type: ast.NamedType(foreignClass, nil),
				Kind: lo.Ternary(e.TableRelation == e.TableName, RECURSIVE, MANY_TO_ONE),
				//Link: &internal.Entry{
				//	TableName:      e.TableRelation,
				//	ColumnName:     e.ColumnRelation,
				//	TableRelation:  e.TableName,
				//	ColumnRelation: e.ColumnName,
				//},
				Table:     e.TableName,
				Column:    e.ColumnName,
				Arguments: append(args, inputs(foreignClass)...),
			}
			//ManyToMany
			rest := maputil.OmitBy(v, func(key string, value *internal.Entry) bool {
				return k == key || value.TableRelation == e.TableName
			})
			for _, r := range rest {
				class, field := my.Named(
					r.TableRelation,
					r.ColumnName,
					WithTrimSuffix(),
					JoinListSuffix(),
				)
				my.Nodes[foreignClass].Fields[field] = &internal.Field{
					Name: field,
					Type: ast.ListType(ast.NamedType(class, nil), nil),
					Kind: MANY_TO_MANY,
					//Link: &internal.Entry{
					//	TableName:      r.TableRelation,
					//	ColumnName:     e.ColumnRelation,
					//	TableRelation:  e.TableName,
					//	ColumnRelation: r.ColumnName,
					//},
					//Join:      e,
					Arguments: inputs(class),
				}
			}
		}
	}

	return nil
}
