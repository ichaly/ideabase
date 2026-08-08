package pgsql

import (
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/vektah/gqlparser/v2"
)

func (my *_DialectSuite) TestCompositePrimaryKeyInputCompilesEveryColumn() {
	classes := map[string]*internal.ClassConfig{"Order": {
		Table: "orders", PrimaryKeys: []string{"tenantId", "code"},
		Fields: map[string]*internal.FieldConfig{
			"tenantId": {Type: "Int", Column: "tenant_id", IsPrimary: true},
			"code":     {Type: "String", Column: "code", IsPrimary: true},
			"note":     {Type: "String", Column: "note", IsNullable: true},
		},
	}}
	meta, schema, dialect := my.newSuite(map[string]interface{}{"metadata.classes": classes})
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)

	query, gqlErr := gqlparser.LoadQuery(schema, `query ($key: OrderKeyInput!) {
		orders(key: $key) { items { tenantId code } }
	}`)
	my.Require().Empty(gqlErr)
	sql, args, err := compile.Build(query.Operations[0], map[string]interface{}{"key": map[string]interface{}{"tenantId": 7, "code": "A"}})
	my.Require().NoError(err)
	my.Contains(normalizeSQL(sql), `WHERE ("orders"."tenant_id" = $1 AND "orders"."code" = $2)`)
	my.Equal([]any{7, "A"}, args)

	mutation, gqlErr := gqlparser.LoadQuery(schema, `mutation {
		updateOrderByPk(key: {tenantId: 7, code: "A"}, input: {note: "ready"}) { code }
	}`)
	my.Require().Empty(gqlErr)
	sql, args, err = compile.Build(mutation.Operations[0], nil)
	my.Require().NoError(err)
	my.Contains(normalizeSQL(sql), `WHERE ("orders"."tenant_id" = $2 AND "orders"."code" = $3)`)
	my.Equal([]any{"ready", int64(7), "A"}, args)
}
