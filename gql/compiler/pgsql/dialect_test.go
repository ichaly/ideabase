package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/stretchr/testify/assert"
)

func TestDialect_BuildQuery(t *testing.T) {
	tests := []struct {
		name    string
		query   string
		wantSQL string
		wantErr bool
	}{
		{
			name: "简单查询所有字段",
			query: `
				query GetUsers {
					users {
						id
						name
						email
					}
				}
			`,
			wantSQL: `SELECT "id", "name", "email" FROM "users"`,
			wantErr: false,
		},
		{
			name: "带分页的查询",
			query: `
				query GetUsersWithPagination {
					users(limit: 10, offset: 20) {
						id
						name
					}
				}
			`,
			wantSQL: `SELECT "id", "name" FROM "users" LIMIT 10 OFFSET 20`,
			wantErr: false,
		},
	}

	dialect := NewDialect()
	meta := &gql.Metadata{} // 创建空的元数据对象

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 解析GraphQL查询
			op, err := parseQuery(tt.query)
			if err != nil {
				t.Fatalf("failed to parse query: %v", err)
			}
			if op == nil {
				t.Fatal("operation is nil")
			}
			if len(op.SelectionSet) == 0 {
				t.Fatal("selection set is empty")
			}

			// 创建编译器
			compiler := gql.NewCompiler(meta, dialect)

			// 执行BuildQuery
			err = dialect.BuildQuery(compiler, op.SelectionSet)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantSQL, compiler.String())
		})
	}
}
