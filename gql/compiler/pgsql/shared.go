// Package pgsql 包内共享的SQL写入小工具
package pgsql

import "github.com/ichaly/ideabase/gql/compiler"

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

// writeColumns 逗号连接的限定列清单："限定符"."列1", "限定符"."列2"...
func writeColumns(ctx *compiler.Context, qualifier string, columns []string) {
	next := comma(ctx)
	for _, column := range columns {
		next()
		ctx.Column(qualifier, column)
	}
}
