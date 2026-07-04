package pgsql

import (
	"encoding/base64"
	"encoding/json"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2"
)

// EncodeCursor 测试辅助：按引擎游标格式（base64(JSON数组)）构造入参
func EncodeCursor(keys []any) string {
	data, _ := json.Marshal(keys)
	return base64.StdEncoding.EncodeToString(data)
}

func (my *_DialectSuite) TestCursor() {
	cases := []Case{
		{
			name:  "首页向前翻页",
			query: `query { users(first: 2, sort: { name: ASC }) { items { id name } pageInfo { hasNext hasPrev end } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT(
						'items', COALESCE(JSONB_AGG((TO_JSONB("__sr_0".*) - '__rn' - '__cursor') ORDER BY "__sr_0"."__rn") FILTER (WHERE "__sr_0"."__rn" <= 2), '[]'),
						'pageInfo', JSONB_BUILD_OBJECT(
							'hasNext', COALESCE(MAX("__sr_0"."__rn") > 2, FALSE),
							'hasPrev', false,
							'end', (JSONB_AGG("__sr_0"."__cursor" ORDER BY "__sr_0"."__rn") FILTER (WHERE "__sr_0"."__rn" <= 2) ->> -1))
					) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name",
							ROW_NUMBER() OVER () AS "__rn",
							encode(convert_to(JSONB_BUILD_ARRAY("sys_user_0"."name", "sys_user_0"."id")::text, 'UTF8'), 'base64') AS "__cursor"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user ORDER BY "sys_user"."name" ASC, "sys_user"."id" ASC LIMIT 3) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:      "first变量页大小",
			query:     `query ($n: Int) { users(first: $n, sort: { name: ASC }) { items { id name } pageInfo { hasNext hasPrev end } } }`,
			variables: map[string]interface{}{"n": 3},
			args:      []any{3, 3, 3, 3},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT(
						'items', COALESCE(JSONB_AGG((TO_JSONB("__sr_0".*) - '__rn' - '__cursor') ORDER BY "__sr_0"."__rn") FILTER (WHERE "__sr_0"."__rn" <= $1), '[]'),
						'pageInfo', JSONB_BUILD_OBJECT(
							'hasNext', COALESCE(MAX("__sr_0"."__rn") > $2, FALSE),
							'hasPrev', false,
							'end', (JSONB_AGG("__sr_0"."__cursor" ORDER BY "__sr_0"."__rn") FILTER (WHERE "__sr_0"."__rn" <= $3) ->> -1))
					) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id", "sys_user_0"."name" AS "name",
							ROW_NUMBER() OVER () AS "__rn",
							encode(convert_to(JSONB_BUILD_ARRAY("sys_user_0"."name", "sys_user_0"."id")::text, 'UTF8'), 'base64') AS "__cursor"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user ORDER BY "sys_user"."name" ASC, "sys_user"."id" ASC LIMIT $4 + 1) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:      "续页after变量游标",
			query:     `query ($c: Cursor) { users(first: 2, after: $c, sort: { name: ASC }) { items { id } pageInfo { hasNext hasPrev } } }`,
			variables: map[string]interface{}{"c": EncodeCursor([]any{"Bob", 2})},
			args:      []any{EncodeCursor([]any{"Bob", 2}), "Bob", float64(2), EncodeCursor([]any{"Bob", 2})},
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT(
						'items', COALESCE(JSONB_AGG((TO_JSONB("__sr_0".*) - '__rn' - '__cursor') ORDER BY "__sr_0"."__rn") FILTER (WHERE "__sr_0"."__rn" <= 2), '[]'),
						'pageInfo', JSONB_BUILD_OBJECT(
							'hasNext', COALESCE(MAX("__sr_0"."__rn") > 2, FALSE),
							'hasPrev', ($1::text IS NOT NULL))
					) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id",
							ROW_NUMBER() OVER () AS "__rn",
							encode(convert_to(JSONB_BUILD_ARRAY("sys_user_0"."name", "sys_user_0"."id")::text, 'UTF8'), 'base64') AS "__cursor"
						FROM (SELECT "sys_user"."id", "sys_user"."name" FROM sys_user
							WHERE ($4::text IS NULL OR "sys_user"."name" > $2 OR ("sys_user"."name" = $2 AND "sys_user"."id" > $3))
							ORDER BY "sys_user"."name" ASC, "sys_user"."id" ASC LIMIT 3) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
		{
			name:  "向后翻页last",
			query: `query { users(last: 2) { items { id } pageInfo { hasNext hasPrev start } } }`,
			expected: `SELECT JSONB_BUILD_OBJECT('users', "__sj_0"."json") AS "__root" FROM (SELECT TRUE) AS "__root_x"
				LEFT OUTER JOIN LATERAL (
					SELECT JSONB_BUILD_OBJECT(
						'items', COALESCE(JSONB_AGG((TO_JSONB("__sr_0".*) - '__rn' - '__cursor') ORDER BY "__sr_0"."__rn" DESC) FILTER (WHERE "__sr_0"."__rn" <= 2), '[]'),
						'pageInfo', JSONB_BUILD_OBJECT(
							'hasNext', false,
							'hasPrev', COALESCE(MAX("__sr_0"."__rn") > 2, FALSE),
							'start', (JSONB_AGG("__sr_0"."__cursor" ORDER BY "__sr_0"."__rn" DESC) FILTER (WHERE "__sr_0"."__rn" <= 2) ->> 0))
					) AS "json"
					FROM (
						SELECT "sys_user_0"."id" AS "id",
							ROW_NUMBER() OVER () AS "__rn",
							encode(convert_to(JSONB_BUILD_ARRAY("sys_user_0"."id")::text, 'UTF8'), 'base64') AS "__cursor"
						FROM (SELECT "sys_user"."id" FROM sys_user ORDER BY "sys_user"."id" DESC LIMIT 3) AS "sys_user_0"
					) AS "__sr_0"
				) AS "__sj_0" ON TRUE`,
		},
	}
	my.runCases(cases)
}

// TestCursorGuards 游标分页参数约束
func (my *_DialectSuite) TestCursorGuards() {
	for name, c := range map[string]struct{ query, wants string }{
		"first与last互斥": {`query { users(first: 1, last: 1) { items { id } } }`, "不能同时使用"},
		"与limit互斥":     {`query { users(first: 1, limit: 5) { items { id } } }`, "不能同时使用"},
		"after需要first": {`query ($c: Cursor) { users(after: $c) { items { id } } }`, "必须配合first/last"},
		"pageInfo需要游标": {`query { users { items { id } pageInfo { hasNext } } }`, "需要配合first/last"},
		"first字面量须正":   {`query { users(first: 0) { items { id } } }`, "必须是正整数或变量"},
	} {
		my.Run(name, func() {
			doc, gqlErr := gqlparser.LoadQuery(my.schema, c.query)
			my.Require().Empty(gqlErr, "解析失败")

			compile, err := gql.NewCompiler(my.meta, []compiler.Dialect{my.dialect})
			my.Require().NoError(err)
			_, _, err = compile.Build(doc.Operations[0], nil)
			my.Assert().ErrorContains(err, c.wants)
		})
	}
}
