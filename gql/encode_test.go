package gql

import (
	"context"
	gojson "encoding/json"
	"strings"
	"testing"

	_ "github.com/ichaly/ideabase/gql/compiler/pgsql" // 自注册PostgreSQL方言
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func token(t *testing.T, id uint64) string {
	t.Helper()
	s := std.Id(id).Encode()
	require.NotEmpty(t, s)
	return s
}

// TestEncodeIdBytes 流式扫描器：仅ID路径上的整数被编码，其余字节零触碰
func TestEncodeIdBytes(t *testing.T) {
	paths := codecPaths{
		"users": codecPaths{
			"items": codecPaths{
				"id":     idCodec{},
				"userId": idCodec{},
				"tags":   codecPaths{"id": idCodec{}},
				"ids":    idCodec{}, // ID列表叶子
			},
		},
	}
	t1, t2 := token(t, 540800000000000001), token(t, 42)

	cases := []struct{ name, in, want string }{
		{
			"主键外键与嵌套列表",
			`{"users":{"items":[{"id":540800000000000001,"userId":42,"tags":[{"id":42,"name":"a"}]}],"total":3}}`,
			`{"users":{"items":[{"id":"` + t1 + `","userId":"` + t2 + `","tags":[{"id":"` + t2 + `","name":"a"}]}],"total":3}}`,
		},
		{
			"ID数组叶子",
			`{"users":{"items":[{"ids":[42,540800000000000001]}]}}`,
			`{"users":{"items":[{"ids":["` + t2 + `","` + t1 + `"]}]}}`,
		},
		{
			"字符串内容与转义零触碰",
			`{"users":{"items":[{"id":42,"note":"id: 99","remark":"{\"id\":7} \\ \" 文本"}]}}`,
			`{"users":{"items":[{"id":"` + t2 + `","note":"id: 99","remark":"{\"id\":7} \\ \" 文本"}]}}`,
		},
		{
			"非ID路径的数字不动",
			`{"users":{"items":[{"age":42}],"total":42}}`,
			`{"users":{"items":[{"age":42}],"total":42}}`,
		},
		{
			"null与0与负数小数透传",
			`{"users":{"items":[{"id":null},{"id":0},{"userId":-5},{"userId":1.5}]}}`,
			`{"users":{"items":[{"id":null},{"id":0},{"userId":-5},{"userId":1.5}]}}`,
		},
		{
			"空白与布尔",
			"{\n  \"users\": { \"items\": [ { \"id\": 42, \"ok\": true, \"x\": false } ] }\n}",
			"{\n  \"users\": { \"items\": [ { \"id\": \"" + t2 + "\", \"ok\": true, \"x\": false } ] }\n}",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, string(encodeBytes([]byte(c.in), paths)))
		})
	}

	t.Run("结构异常原样返回", func(t *testing.T) {
		broken := `{"users":{"items":[{"id":42}`
		assert.Equal(t, broken, string(encodeBytes([]byte(broken), paths)))
	})
	t.Run("空路径树原样返回", func(t *testing.T) {
		in := `{"a":1}`
		assert.Equal(t, in, string(encodeBytes([]byte(in), nil)))
	})
}

// TestEncodeIdBytes_Equivalence 与参考实现（解包→按树编码→重序列化）语义等价
func TestEncodeIdBytes_Equivalence(t *testing.T) {
	paths := codecPaths{"data": codecPaths{"id": idCodec{}, "sub": codecPaths{"userId": idCodec{}}}}
	payloads := []string{
		`{"data":{"id":123,"name":"张三 \"quote\" \\","sub":[{"userId":456,"v":1.5},{"userId":null}],"n":-7,"e":2e3,"empty":{},"list":[]}}`,
		`{"data":{"id":9007199254740993,"sub":{"userId":540800000000000001},"deep":{"id":1}}}`,
		`{}`,
	}
	// 参考实现：完整解包后按同一路径树编码再序列化
	var walk func(v any, node any) any
	walk = func(v any, node any) any {
		switch val := v.(type) {
		case map[string]any:
			tree, _ := node.(codecPaths)
			for k, e := range val {
				val[k] = walk(e, tree[k])
			}
		case []any:
			for i, e := range val {
				val[i] = walk(e, node)
			}
		case gojson.Number:
			if codec, ok := node.(Codec); ok {
				if repl := codec.Encode([]byte(val)); repl != nil {
					var s string
					require.NoError(t, gojson.Unmarshal(repl, &s))
					return s
				}
			}
		}
		return v
	}
	for _, p := range payloads {
		got := encodeBytes([]byte(p), paths)

		var scanned, reference any
		require.NoError(t, gojson.Unmarshal(got, &scanned), "扫描器输出应是合法JSON: %s", got)
		dec := gojson.NewDecoder(strings.NewReader(p))
		dec.UseNumber()
		require.NoError(t, dec.Decode(&reference))
		reference = walk(reference, paths)

		refBytes, err := gojson.Marshal(reference)
		require.NoError(t, err)
		var refAny any
		require.NoError(t, gojson.Unmarshal(refBytes, &refAny))
		assert.Equal(t, refAny, scanned, "payload: %s", p)
	}
}

// setupEncodeExecutor 注册内置ID codec的执行器，extra 追加业务codec
func setupEncodeExecutor(t *testing.T, extra ...Codec) (*Executor, func()) {
	executor, _, cleanup := newTestExecutor(t, nil, WithCodecs(append([]Codec{NewIdCodec()}, extra...)...))
	return executor, cleanup
}

// maskCodec 自定义codec示例：认领email列为Masked标量，出参打码、入参原样
type maskCodec struct{}

func (maskCodec) Name() string { return "Masked" }
func (maskCodec) Encode(token []byte) []byte {
	if len(token) < 6 || token[0] != '"' {
		return nil
	}
	masked := append(append([]byte(nil), token[:3]...), []byte(`***`)...)
	return append(masked, token[len(token)-3:]...)
}
func (maskCodec) Decode(value any) any                            { return value }
func (maskCodec) Match(_ *protocol.Class, f *protocol.Field) bool { return f.Column == "email" }

// TestCustomCodec 业务codec全链路：Match认领改写标量 + schema渲染 + 出参流式打码
func TestCustomCodec(t *testing.T) {
	executor, cleanup := setupEncodeExecutor(t, maskCodec{})
	defer cleanup()
	ctx := context.Background()

	require.Contains(t, executor.source, "scalar Masked", "自定义标量应进入schema")
	require.Contains(t, executor.source, "email: Masked", "email字段应被认领为Masked")
	require.Contains(t, executor.source, "input MaskedWhereInput", "过滤器应借用底层标量操作符集")

	reply := executor.Execute(ctx, `mutation {
		createUser(input: { name: "Bob", email: "bob@fish.ai" }) { id name email }
	}`, nil, "")
	require.Empty(t, reply.Errors, "创建失败: %v", reply.Errors)
	created := reply.Data["createUser"].(map[string]interface{})
	require.Equal(t, `bo***ai`, created["email"], "出参应打码")
	require.True(t, strings.HasPrefix(created["id"].(string), "~"), "ID codec应同时生效")
}

// TestEncodeIdRoundTrip ID出入参加解密端到端：
// 出参主键/外键编码为shortId，入参shortId经变量/字面量/where过滤还原为数字
func TestEncodeIdRoundTrip(t *testing.T) {
	executor, cleanup := setupEncodeExecutor(t)
	defer cleanup()
	ctx := context.Background()

	// 1. 创建用户：出参id应为shortId字符串
	reply := executor.Execute(ctx, `mutation {
		createUser(input: { name: "Alice", email: "alice@x.com" }) { id name }
	}`, nil, "")
	require.Empty(t, reply.Errors, "创建用户失败: %v", reply.Errors)
	created := reply.Data["createUser"].(map[string]interface{})
	uid, ok := created["id"].(string)
	require.True(t, ok, "id应编码为字符串, 实际: %T(%v)", created["id"], created["id"])
	require.True(t, strings.HasPrefix(uid, "~"), "id应带shortId前缀: %s", uid)

	// 2. shortId作为ID变量入参：创建文章（外键userId在schema中即ID标量）
	reply = executor.Execute(ctx, `mutation ($title: String!, $uid: ID!) {
		createPost(input: { title: $title, userId: $uid }) { id title userId user { id name } }
	}`, map[string]interface{}{"title": "Hello", "uid": uid}, "")
	require.Empty(t, reply.Errors, "创建文章失败: %v", reply.Errors)
	post := reply.Data["createPost"].(map[string]interface{})
	require.True(t, strings.HasPrefix(post["id"].(string), "~"), "文章主键应编码")
	require.Equal(t, uid, post["userId"], "外键出参应编码且与用户id一致")
	require.Equal(t, uid, post["user"].(map[string]interface{})["id"], "嵌套关系id应编码")

	// 3. shortId作为字面量入参（AST改写路径）
	reply = executor.Execute(ctx, `query { users(id: "`+uid+`") { items { id name } } }`, nil, "")
	require.Empty(t, reply.Errors, "字面量入参查询失败: %v", reply.Errors)
	items := reply.Data["users"].(map[string]interface{})["items"].([]interface{})
	require.Len(t, items, 1)
	require.Equal(t, "Alice", items[0].(map[string]interface{})["name"])

	// 4. where过滤中的shortId（input对象递归解码）与字面量列表
	reply = executor.Execute(ctx, `query ($id: ID!) {
		users(where: { id: { eq: $id } }) { items { id } total }
	}`, map[string]interface{}{"id": uid}, "")
	require.Empty(t, reply.Errors, "where过滤查询失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["users"].(map[string]interface{})["total"])

	reply = executor.Execute(ctx, `query {
		users(where: { id: { in: ["`+uid+`"] } }) { items { id } total }
	}`, nil, "")
	require.Empty(t, reply.Errors, "in列表字面量失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["users"].(map[string]interface{})["total"])

	// 5. 兼容历史入参：数字与shortId等价
	var id std.Id
	require.NoError(t, id.Decode(uid))
	reply = executor.Execute(ctx, `query ($id: ID!) { users(id: $id) { items { name } } }`,
		map[string]interface{}{"id": uid}, "")
	require.Empty(t, reply.Errors)
	reply2 := executor.Execute(ctx, `query ($id: ID!) { users(id: $id) { items { name } } }`,
		map[string]interface{}{"id": int64(id)}, "")
	require.Empty(t, reply2.Errors, "数字入参应兼容: %v", reply2.Errors)
	require.Equal(t, reply.Data, reply2.Data)

	// 6. 计划缓存复用后出入参转换仍生效（同查询第二次走缓存路径）
	reply = executor.Execute(ctx, `query ($id: ID!) {
		users(where: { id: { eq: $id } }) { items { id } total }
	}`, map[string]interface{}{"id": uid}, "")
	require.Empty(t, reply.Errors, "缓存命中路径失败: %v", reply.Errors)
	wrapper := reply.Data["users"].(map[string]interface{})
	require.EqualValues(t, 1, wrapper["total"])
	first := wrapper["items"].([]interface{})[0].(map[string]interface{})
	require.Equal(t, uid, first["id"], "缓存路径出参应仍为shortId")
}

// TestEncodeIdListVariable [ID!]变量数组入参：shortId解码 + in变量逐元素展开(volatile)
func TestEncodeIdListVariable(t *testing.T) {
	executor, cleanup := setupEncodeExecutor(t)
	defer cleanup()
	ctx := context.Background()

	var ids []interface{}
	for _, name := range []string{"u1", "u2"} {
		reply := executor.Execute(ctx, `mutation ($n: String!, $e: String!) {
			createUser(input: { name: $n, email: $e }) { id }
		}`, map[string]interface{}{"n": name, "e": name + "@x.com"}, "")
		require.Empty(t, reply.Errors)
		ids = append(ids, reply.Data["createUser"].(map[string]interface{})["id"])
	}

	query := `query ($ids: [ID!]) { users(where: { id: { in: $ids } }) { items { id } total } }`
	reply := executor.Execute(ctx, query, map[string]interface{}{"ids": ids}, "")
	require.Empty(t, reply.Errors, "ID列表变量过滤失败: %v", reply.Errors)
	require.EqualValues(t, 2, reply.Data["users"].(map[string]interface{})["total"])

	// 空列表恒不匹配且SQL合法
	reply = executor.Execute(ctx, query, map[string]interface{}{"ids": []interface{}{}}, "")
	require.Empty(t, reply.Errors, "空列表应合法: %v", reply.Errors)
	require.EqualValues(t, 0, reply.Data["users"].(map[string]interface{})["total"])

	// 第二次执行走volatile缓存分支（AST复用重编译）
	reply = executor.Execute(ctx, query, map[string]interface{}{"ids": ids[:1]}, "")
	require.Empty(t, reply.Errors, "volatile缓存路径失败: %v", reply.Errors)
	require.EqualValues(t, 1, reply.Data["users"].(map[string]interface{})["total"])
}

// BenchmarkEncodeBytes 流式转换性能：典型列表响应（100行×2个ID字段）
func BenchmarkEncodeBytes(b *testing.B) {
	paths := codecPaths{"users": codecPaths{"items": codecPaths{"id": idCodec{}, "userId": idCodec{}}}}
	var sb strings.Builder
	sb.WriteString(`{"users":{"items":[`)
	for i := 0; i < 100; i++ {
		if i > 0 {
			sb.WriteString(",")
		}
		sb.WriteString(`{"id":540800000000000001,"userId":540800000000000002,"name":"用户名","note":"一段较长的文本内容用于模拟真实负载"}`)
	}
	sb.WriteString(`],"total":100}}`)
	data := []byte(sb.String())

	b.Run("有命中", func(b *testing.B) {
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			encodeBytes(data, paths)
		}
	})
	miss := codecPaths{"other": idCodec{}} // 路径不命中：应零分配返回原字节
	b.Run("无命中", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(data)))
		for i := 0; i < b.N; i++ {
			encodeBytes(data, miss)
		}
	})
}
