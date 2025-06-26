package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/stretchr/testify/assert"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestSortBuilder(t *testing.T) {
	dialect := &Dialect{}

	tests := []struct {
		name     string
		args     ast.ArgumentList
		expected string
		wantErr  bool
	}{
		{
			name: "简单升序排序",
			args: ast.ArgumentList{
				{
					Name: gql.SORT,
					Value: &ast.Value{
						Kind: ast.ListValue,
						Children: []*ast.ChildValue{
							{
								Name: "name",
								Value: &ast.Value{
									Kind: ast.EnumValue,
									Raw:  "ASC",
								},
							},
						},
					},
				},
			},
			expected: ` ORDER BY "name" ASC`,
			wantErr:  false,
		},
		{
			name: "PostgreSQL NULL值排序",
			args: ast.ArgumentList{
				{
					Name: gql.SORT,
					Value: &ast.Value{
						Kind: ast.ListValue,
						Children: []*ast.ChildValue{
							{
								Name: "email",
								Value: &ast.Value{
									Kind: ast.EnumValue,
									Raw:  "DESC_NULLS_LAST",
								},
							},
						},
					},
				},
			},
			expected: ` ORDER BY "email" DESC NULLS LAST`,
			wantErr:  false,
		},
		{
			name: "多字段排序",
			args: ast.ArgumentList{
				{
					Name: gql.SORT,
					Value: &ast.Value{
						Kind: ast.ListValue,
						Children: []*ast.ChildValue{
							{
								Name: "name",
								Value: &ast.Value{
									Kind: ast.EnumValue,
									Raw:  "ASC",
								},
							},
							{
								Name: "createdAt",
								Value: &ast.Value{
									Kind: ast.EnumValue,
									Raw:  "DESC",
								},
							},
						},
					},
				},
			},
			expected: ` ORDER BY "name" ASC, "createdAt" DESC`,
			wantErr:  false,
		},
		{
			name: "旧版本orderBy兼容",
			args: ast.ArgumentList{
				{
					Name: "orderBy",
					Value: &ast.Value{
						Kind: ast.ListValue,
						Children: []*ast.ChildValue{
							{
								Name: "name",
								Value: &ast.Value{
									Kind: ast.StringValue,
									Raw:  "ASC",
								},
							},
						},
					},
				},
			},
			expected: ` ORDER BY "name" ASC`,
			wantErr:  false,
		},
		{
			name:     "无排序参数",
			args:     ast.ArgumentList{},
			expected: "",
			wantErr:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// 创建测试上下文
			ctx := compiler.NewContext(nil, `"`, nil)

			// 执行排序构建
			err := dialect.buildOrderBy(ctx, tt.args)

			// 验证结果
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.expected, ctx.String())
			}
		})
	}
}

func TestSortWithAlias(t *testing.T) {
	dialect := &Dialect{}

	args := ast.ArgumentList{
		{
			Name: gql.SORT,
			Value: &ast.Value{
				Kind: ast.ListValue,
				Children: []*ast.ChildValue{
					{
						Name: "name",
						Value: &ast.Value{
							Kind: ast.EnumValue,
							Raw:  "ASC",
						},
					},
				},
			},
		},
	}

	ctx := compiler.NewContext(nil, `"`, nil)
	err := dialect.buildOrderByWithAlias(ctx, args, "u")

	assert.NoError(t, err)
	assert.Equal(t, ` ORDER BY "u"."name" ASC`, ctx.String())
}

func TestSortErrorHandling(t *testing.T) {
	dialect := &Dialect{}

	tests := []struct {
		name string
		args ast.ArgumentList
	}{
		{
			name: "空字段名",
			args: ast.ArgumentList{
				{
					Name: gql.SORT,
					Value: &ast.Value{
						Kind: ast.ListValue,
						Children: []*ast.ChildValue{
							{
								Name: "",
								Value: &ast.Value{
									Kind: ast.EnumValue,
									Raw:  "ASC",
								},
							},
						},
					},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := compiler.NewContext(nil, `"`, nil)
			err := dialect.buildOrderBy(ctx, tt.args)
			assert.Error(t, err)
		})
	}
}
