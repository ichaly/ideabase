package gql

import (
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/utl"
	"github.com/samber/lo"
)

func (my *Metadata) Named(class, field string, ops ...internal.NamedOption) (string, string) {
	//移除表前缀
	if val, ok := utl.StartWithAny(class, my.cfg.Prefixes...); ok {
		class = strings.TrimPrefix(class, val)
	}

	//应用配置选项
	for _, o := range ops {
		field = o(class, field)
	}

	//是否驼峰命名
	if my.cfg.UseCamel {
		class = strcase.ToCamel(class)
		field = strcase.ToLowerCamel(field)
	}

	return class, field
}

// WithTrimSuffix 移除`_id`后缀
func WithTrimSuffix() internal.NamedOption {
	return func(t, s string) string {
		return strings.TrimSuffix(s, "_id")
	}
}

// JoinListSuffix 追加`_list`后缀
func JoinListSuffix() internal.NamedOption {
	return func(t, s string) string {
		return strings.Join([]string{s, "list"}, "_")
	}
}

// SwapPrimaryKey 替换id列的名称
func SwapPrimaryKey(table string) internal.NamedOption {
	return func(t, s string) string {
		if s == "id" {
			s = table
		}
		return s
	}
}

// NamedRecursion 重命名递归关联列名
func NamedRecursion(c *internal.Entry, b bool) internal.NamedOption {
	return func(t, s string) string {
		if c.TableRelation == c.TableName {
			s = lo.Ternary(b, PARENTS, CHILDREN)
		}
		return s
	}
}
