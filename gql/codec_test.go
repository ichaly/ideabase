package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/assert"
)

// TestFinalize 元数据定型规则：结构推导（主外键→ID）、codec认领（_by审计列）、
// 配置显式类型豁免、Virtual字段跳过
func TestFinalize(t *testing.T) {
	class := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{
		"id":        {Name: "id", Column: "id", Type: protocol.SCALAR_INT, IsPrimary: true},
		"parentId":  {Name: "parentId", Column: "parent_id", Type: protocol.SCALAR_INT, Relation: &protocol.Relation{}},
		"createdBy": {Name: "createdBy", Column: "created_by", Type: protocol.SCALAR_INT},
		"updatedBy": {Name: "updatedBy", Column: "updated_by", Type: protocol.SCALAR_INT},
		"sortBy":    {Name: "sortBy", Column: "sort_by", Type: protocol.SCALAR_STRING}, // 同后缀非整型，不应误伤
		"age":       {Name: "age", Column: "age", Type: protocol.SCALAR_INT},
		"parent":    {Name: "parent", Column: "", Type: "User", Virtual: true, Relation: &protocol.Relation{}},
	}}
	meta := &Metadata{
		Nodes: map[string]*protocol.Class{"User": class, "sys_user": class}, // 表名索引应跳过
		cfg: &internal.Config{Metadata: internal.MetadataConfig{Classes: map[string]*internal.ClassConfig{
			"User": {Fields: map[string]*internal.FieldConfig{
				"updatedBy": {Type: protocol.SCALAR_INT}, // 配置显式指定，豁免认领
			}},
		}}},
	}
	meta.setCodecs([]Codec{idCodec{}})

	meta.finalize()

	assert.Equal(t, protocol.SCALAR_ID, class.Fields["id"].Type, "主键结构推导为ID")
	assert.Equal(t, protocol.SCALAR_ID, class.Fields["parentId"].Type, "外键实列结构推导为ID")
	assert.Equal(t, protocol.SCALAR_ID, class.Fields["createdBy"].Type, "_by整型列被idCodec认领")
	assert.Equal(t, protocol.SCALAR_INT, class.Fields["updatedBy"].Type, "配置显式类型豁免认领")
	assert.Equal(t, protocol.SCALAR_STRING, class.Fields["sortBy"].Type, "非整型_by列不受认领")
	assert.Equal(t, protocol.SCALAR_INT, class.Fields["age"].Type, "普通列不动")
	assert.Equal(t, "User", class.Fields["parent"].Type, "Virtual关系载体不动")
}

// TestIdCodec 内置ID codec契约：数字token→带引号shortId，0/非数字不处理；
// 入参shortId/十进制字符串→int64，其余原样
func TestIdCodec(t *testing.T) {
	codec := idCodec{}

	repl := codec.Encode([]byte("42"))
	assert.NotNil(t, repl)
	assert.Equal(t, byte('"'), repl[0])
	assert.Nil(t, codec.Encode([]byte("0")), "0不编码")
	assert.Nil(t, codec.Encode([]byte("1.5")), "非整数token不编码")
	assert.Nil(t, codec.Encode([]byte(`"str"`)), "字符串token不编码")

	var token string
	assert.NoError(t, json.Unmarshal(repl, &token))
	assert.Equal(t, int64(42), codec.Decode(token), "shortId还原")
	assert.Equal(t, int64(42), codec.Decode("42"), "十进制字符串兼容")
	assert.Equal(t, int64(7), codec.Decode(int64(7)), "数字入参原样")
}
