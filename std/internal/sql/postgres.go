package sql

import (
	"maps"

	pg "github.com/pganalyze/pg_query_go/v6"
	pgquery "github.com/wasilibs/go-pgquery"
	"google.golang.org/protobuf/reflect/protoreflect"
)

func extractPostgresTags(sql string) ([]string, bool) {
	// 仅使用 pg 包的 AST 类型，解析入口必须走 WASM，不能调用 CGO 入口。
	tree, err := pgquery.Parse(sql)
	if err != nil || len(tree.GetStmts()) != 1 {
		return nil, false
	}
	stmt := tree.Stmts[0].GetStmt()
	if stmt.GetSelectStmt() == nil {
		return nil, false
	}
	seen := map[string]struct{}{}
	if !walkPostgres(stmt.ProtoReflect(), map[string]struct{}{}, seen) {
		return nil, false
	}
	return sortedTags(seen), true
}

func walkPostgres(m protoreflect.Message, ctes, seen map[string]struct{}) bool {
	switch n := m.Interface().(type) {
	case *pg.InsertStmt, *pg.UpdateStmt, *pg.DeleteStmt, *pg.MergeStmt:
		return false
	case *pg.RangeVar:
		if _, isCTE := ctes[n.Relname]; !isCTE || n.Schemaname != "" || n.Catalogname != "" {
			addTableTags(seen, n.Schemaname, n.Relname)
		}
		return true
	case *pg.SelectStmt:
		if n.IntoClause != nil || len(n.LockingClause) > 0 {
			return false
		}
		if n.WithClause != nil {
			ctes = maps.Clone(ctes)
			if n.WithClause.Recursive {
				for _, node := range n.WithClause.Ctes {
					ctes[node.GetCommonTableExpr().GetCtename()] = struct{}{}
				}
			}
			for _, node := range n.WithClause.Ctes {
				cte := node.GetCommonTableExpr()
				if cte == nil || cte.Ctequery == nil || !walkPostgres(cte.Ctequery.ProtoReflect(), ctes, seen) {
					return false
				}
				// 非递归 CTE 在自己的定义之后才可见，不能遮蔽定义内同名物理表。
				ctes[cte.Ctename] = struct{}{}
			}
		}
	}
	valid := true
	m.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind || field.Name() == "with_clause" {
			return true
		}
		if field.IsList() {
			list := value.List()
			for i := 0; i < list.Len() && valid; i++ {
				valid = walkPostgres(list.Get(i).Message(), ctes, seen)
			}
		} else {
			valid = walkPostgres(value.Message(), ctes, seen)
		}
		return valid
	})
	return valid
}
