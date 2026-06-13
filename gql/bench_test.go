package gql

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// 引擎核心热路径微基准：纯CPU/分配，不依赖数据库容器，随时 go test -bench 可跑。
// 固化 simplify 过程中量化过的两处关键优化为可回归基线：
//   - 响应直通（MarshalJSON 拼接 vs 解包重序列化）
//   - resolver 宿主拍平（hosts 单遍复用 vs interface{} 装箱中间层）

// sampleRows 构造含 n 个对象的列表 JSON（每对象若干标量 + 一层嵌套数组），
// 模拟典型列表响应的 __root 字节
func sampleRows(n int) []byte {
	var b strings.Builder
	b.WriteString(`{"posts":{"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteByte(',')
		}
		fmt.Fprintf(&b, `{"id":%d,"title":"标题文章编号%d","body":"正文内容示例文本片段","tags":["go","sql","graphql"],"user":{"name":"作者%d","email":"a%d@demo.dev"}}`, i, i, i%7, i%7)
	}
	b.WriteString(`],"total":`)
	b.WriteString(strconv.Itoa(n))
	b.WriteString(`}}`)
	return []byte(b.String())
}

// BenchmarkReplyDirect 直通路径：无 resolver 时 __root 字节经 MarshalJSON 直接拼入响应
func BenchmarkReplyDirect(b *testing.B) {
	data := sampleRows(50)
	reply := gqlReply{raw: data}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := json.Marshal(reply); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkReplyUnpack 对照：解包为 map 再序列化（resolver 路径与公开 Execute 走此路）
func BenchmarkReplyUnpack(b *testing.B) {
	data := sampleRows(50)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result := make(map[string]interface{})
		if err := json.Unmarshal(data, &result); err != nil {
			b.Fatal(err)
		}
		if _, err := json.Marshal(gqlReply{Data: result}); err != nil {
			b.Fatal(err)
		}
	}
}

// sampleTree 构造 width 宽、两层嵌套的结果树：root.items[w].children[w]
// 模拟列表查询里嵌套一对多关系上挂 resolver 字段的宿主分布
func sampleTree(width int) map[string]interface{} {
	items := make([]interface{}, width)
	for i := 0; i < width; i++ {
		children := make([]interface{}, width)
		for j := 0; j < width; j++ {
			children[j] = map[string]interface{}{"id": i*width + j, "name": "节点"}
		}
		items[i] = map[string]interface{}{"id": i, "children": children}
	}
	return map[string]interface{}{"items": items}
}

// BenchmarkHosts 宿主拍平：按路径展开嵌套一对多的所有宿主对象为扁平数组（resolver 批量收集）
func BenchmarkHosts(b *testing.B) {
	root := sampleTree(8) // 8*8 = 64 个深层宿主
	path := []string{"items", "children"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if got := hosts(root, path); len(got) != 64 {
			b.Fatalf("宿主数不符: %d", len(got))
		}
	}
}
