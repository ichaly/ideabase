package gql

import (
	"encoding/base64"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/stretchr/testify/assert"
)

// encodeCursor 测试辅助：按引擎游标格式（base64(JSON数组)）构造入参
func encodeCursor(keys []any) string {
	data, _ := json.Marshal(keys)
	return base64.StdEncoding.EncodeToString(data)
}

// TestPlanResolveArgs 默认值回填：仅变量缺失时回退默认值（显式null不被覆盖，符合GraphQL规范），
// 游标/列表槽位的默认值同样经解码与规范化
func TestPlanResolveArgs(t *testing.T) {
	plan := &Plan{
		slots:    []compiler.Slot{{Variable: "name", Cursor: -1}},
		defaults: map[string]interface{}{"name": "dft"},
	}

	args, err := plan.ResolveArgs(nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, []any{"dft"}, args, "变量缺失回退默认值")

	args, err = plan.ResolveArgs(map[string]interface{}{"name": nil}, nil)
	assert.NoError(t, err)
	assert.Equal(t, []any{nil}, args, "显式null不被默认值覆盖")

	cursorPlan := &Plan{
		slots:    []compiler.Slot{{Variable: "c", Cursor: 0}, {Variable: "c", Cursor: 1}},
		defaults: map[string]interface{}{"c": encodeCursor([]any{"Bob", 2})},
	}
	args, err = cursorPlan.ResolveArgs(nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, []any{"Bob", int64(2)}, args, "游标默认值须解码后按下标抽取（整数键经UseNumber精确还原int64）")

	listPlan := &Plan{
		slots:    []compiler.Slot{{Variable: "ids", Cursor: -1, List: true}},
		defaults: map[string]interface{}{"ids": []any{int64(1), int64(2)}},
	}
	args, err = listPlan.ResolveArgs(nil, nil)
	assert.NoError(t, err)
	assert.Equal(t, []any{[]int64{1, 2}}, args, "列表默认值须规范化为具体类型数组")
}

// TestPlanArgsCursorError 变量游标解码失败向上传播错误（静默空页会掩盖坏游标）
func TestPlanArgsCursorError(t *testing.T) {
	plan := &Plan{slots: []compiler.Slot{{Variable: "c", Cursor: 0}}}
	broken := map[string]interface{}{"c": "!!!"}

	_, err := plan.ResolveArgs(broken, nil)
	assert.ErrorContains(t, err, "无效的游标")

	args, err := plan.ResolveArgs(nil, nil) // 游标为null=首页，非错误
	assert.NoError(t, err)
	assert.Equal(t, []any{nil}, args)
}
