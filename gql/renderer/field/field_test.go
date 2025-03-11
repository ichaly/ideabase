package field

import (
	"testing"
)

func TestMakeField(t *testing.T) {
	tests := []struct {
		name      string
		fieldName string
		typeName  string
		options   []Option
		expected  string
	}{
		{
			name:      "基础字段",
			fieldName: "id",
			typeName:  "ID",
			options:   []Option{NonNull()},
			expected:  "  id: ID!",
		},
		{
			name:      "带注释字段",
			fieldName: "name",
			typeName:  "String",
			options:   []Option{WithComment("用户名")},
			expected:  "  name: String  # 用户名",
		},
		{
			name:      "列表字段",
			fieldName: "items",
			typeName:  "User",
			options:   []Option{ListNonNull(), NonNull()},
			expected:  "  items: [User!]!",
		},
		{
			name:      "带参数字段",
			fieldName: "users",
			typeName:  "User",
			options: []Option{
				ListNonNull(),
				WithArgs(
					Argument{Name: "filter", Type: "UserFilter"},
					Argument{Name: "limit", Type: "Int"},
				),
			},
			expected: "  users(filter: UserFilter, limit: Int): [User!]",
		},
		{
			name:      "多行参数字段",
			fieldName: "comments",
			typeName:  "Comment",
			options: []Option{
				ListNonNull(),
				WithMultilineArgs(),
				WithArgs(
					Argument{Name: "filter", Type: "CommentFilter"},
					Argument{Name: "sort", Type: "[CommentSort!]"},
					Argument{Name: "limit", Type: "Int"},
					Argument{Name: "offset", Type: "Int"},
				),
			},
			expected: "  comments(\n    filter: CommentFilter\n    sort: [CommentSort!]\n    limit: Int\n    offset: Int\n  ): [Comment!]",
		},
		{
			name:      "自定义缩进",
			fieldName: "title",
			typeName:  "String",
			options:   []Option{WithIndent(4), NonNull()},
			expected:  "    title: String!",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MakeField(tt.fieldName, tt.typeName, tt.options...)
			if result != tt.expected {
				t.Errorf("MakeField() = %v, want %v", result, tt.expected)
			}
		})
	}
}
