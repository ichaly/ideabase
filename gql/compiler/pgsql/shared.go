// Package pgsql 包内共享的SQL写入小工具
package pgsql

import (
	"fmt"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// comma 逗号分隔写入器：首项不写，后续项前写", "
func comma(ctx *compiler.Context) func() {
	n := 0
	return func() {
		if n > 0 {
			ctx.Write(`, `)
		}
		n++
	}
}

// fieldNames 解析字符串列表参数为字段名（兼容单值写法），校验须为实体真实列
func fieldNames(sc scope, args ast.ArgumentList, argName string) ([]string, error) {
	arg := args.ForName(argName)
	if arg == nil || arg.Value == nil {
		return nil, nil
	}
	values := arg.Value.Children
	if len(values) == 0 && arg.Value.Raw != "" { // 单值写法
		values = []*ast.ChildValue{{Value: arg.Value}}
	}
	names := make([]string, 0, len(values))
	for _, child := range values {
		name := child.Value.Raw
		if field, ok := sc.class.Fields[name]; !ok || field.Column == "" {
			return nil, fmt.Errorf("%s包含无效字段: %s", argName, name)
		}
		names = append(names, name)
	}
	return names, nil
}

// columnsOf 字段名映射为列名
func columnsOf(sc scope, names []string) []string {
	columns := make([]string, len(names))
	for i, name := range names {
		columns[i] = sc.column(name)
	}
	return columns
}

// writeColumns 逗号连接的限定列清单："限定符"."列1", "限定符"."列2"...
func writeColumns(ctx *compiler.Context, qualifier string, columns []string) {
	next := comma(ctx)
	for _, column := range columns {
		next()
		ctx.Column(qualifier, column)
	}
}

// hasAlias 选择集切片中是否已有指定别名（远程键补投影去重，数量极小线性即可）
func hasAlias(fields []*ast.Field, alias string) bool {
	for _, f := range fields {
		if f.Alias == alias {
			return true
		}
	}
	return false
}
