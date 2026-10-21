package sql

import (
	"maps"

	"github.com/pingcap/tidb/pkg/parser"
	"github.com/pingcap/tidb/pkg/parser/ast"
	_ "github.com/pingcap/tidb/pkg/parser/test_driver"
)

func extractMySQLTags(sql string) ([]string, bool) {
	// TiDB parser 持有可变状态，每次调用独立实例，避免并发查询互相污染。
	stmts, _, err := parser.New().Parse(sql, "", "")
	if err != nil || len(stmts) != 1 {
		return nil, false
	}
	switch stmts[0].(type) {
	case *ast.SelectStmt, *ast.SetOprStmt:
	default:
		return nil, false
	}
	v := &mysqlTableVisitor{tables: map[string]struct{}{}, ctes: map[string]struct{}{}}
	if _, ok := stmts[0].Accept(v); !ok {
		return nil, false
	}
	return sortedTags(v.tables), true
}

func extractMySQLWriteTags(sql string) ([]string, bool) {
	stmts, _, err := parser.New().Parse(sql, "", "")
	if err != nil || len(stmts) != 1 {
		return nil, false
	}
	switch stmts[0].(type) {
	case *ast.InsertStmt, *ast.UpdateStmt, *ast.DeleteStmt:
	default:
		return nil, false
	}
	v := &mysqlWriteVisitor{tables: map[string]struct{}{}}
	if _, ok := stmts[0].Accept(v); !ok {
		return nil, false
	}
	return sortedTags(v.tables), true
}

type mysqlWriteVisitor struct {
	tables map[string]struct{}
}

func (my *mysqlWriteVisitor) Enter(node ast.Node) (ast.Node, bool) {
	if source, ok := node.(*ast.TableSource); ok {
		if table, ok := source.Source.(*ast.TableName); ok {
			// 从 TableSource 取实际表名，避免把多表 DELETE 目标中的别名当作物理表。
			addTableTags(my.tables, table.Schema.L, table.Name.L)
		}
	}
	return node, false
}

func (my *mysqlWriteVisitor) Leave(node ast.Node) (ast.Node, bool) {
	return node, true
}

type mysqlTableVisitor struct {
	tables, ctes map[string]struct{}
	scopes       []map[string]struct{}
	unsafe       bool
}

func (my *mysqlTableVisitor) Enter(node ast.Node) (ast.Node, bool) {
	switch n := node.(type) {
	case *ast.SelectStmt:
		my.pushScope()
		if n.LockInfo != nil || n.SelectIntoOpt != nil {
			my.unsafe = true
		}
	case *ast.SetOprStmt, *ast.SetOprSelectList:
		my.pushScope()
	case *ast.CommonTableExpression:
		if n.IsRecursive {
			my.ctes[n.Name.L] = struct{}{}
		}
	case *ast.TableName:
		if _, isCTE := my.ctes[n.Name.L]; !isCTE || n.Schema.O != "" {
			// MySQL 的大小写规则随服务端配置变化，tag 折叠大小写以避免漏失效。
			addTableTags(my.tables, n.Schema.L, n.Name.L)
		}
	}
	return node, my.unsafe
}

func (my *mysqlTableVisitor) Leave(node ast.Node) (ast.Node, bool) {
	switch n := node.(type) {
	case *ast.SelectStmt, *ast.SetOprStmt, *ast.SetOprSelectList:
		my.ctes = my.scopes[len(my.scopes)-1]
		my.scopes = my.scopes[:len(my.scopes)-1]
	case *ast.CommonTableExpression:
		my.ctes[n.Name.L] = struct{}{}
	}
	return node, !my.unsafe
}

func (my *mysqlTableVisitor) pushScope() {
	my.scopes = append(my.scopes, my.ctes)
	my.ctes = maps.Clone(my.ctes)
}
