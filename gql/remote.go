package gql

import (
	"context"
	"fmt"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/log"
)

// Remote 远程数据源：把外部服务（REST/gRPC/另一个GraphQL）声明为图里的关系字段。
// 元数据配置 fields.<名>.remote:{source,key} 声明关系，编译期自动把key列补进投影
// （内部别名，不受codec转换影响），执行期按去重后的键集合一次批量取数、按键回填——
// 天然免N+1。超时与重试由实现自行控制（Fetch收到请求ctx）。
type Remote interface {
	Name() string // 数据源名，与配置 remote.source 对应
	// Fetch 批量取数：keys为本批宿主的键集合（已去重、原始数据库值），
	// 返回 键→字段值 映射；缺失的键对应字段为null
	Fetch(ctx context.Context, keys []any) (map[any]any, error)
}

// RegisterRemote 注册远程数据源（与RegisterAction同构；启动期调用）
func (my *Executor) RegisterRemote(remotes ...Remote) {
	for _, r := range remotes {
		my.remotes[r.Name()] = r
	}
}

// remoteJob 一条远程绑定的执行单元：fetch阶段各job独立可并发，
// fill阶段串行回填（宿主map非并发安全）
type remoteJob struct {
	binding binding
	sources []map[string]any
	values  map[any]any
	err     error
}

// fetch 收集去重键并批量取数。容错语义：数据源未注册或Fetch失败时
// 记录错误（随响应errors返回）、字段整体置null，不中断主查询
func (my *remoteJob) fetch(ctx context.Context, remotes map[string]Remote) {
	seen := make(map[any]bool, len(my.sources))
	keys := make([]any, 0, len(my.sources))
	for _, source := range my.sources {
		if k, ok := source[my.binding.Key]; ok && k != nil && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}

	remote, ok := remotes[my.binding.Name]
	if !ok {
		my.err = fmt.Errorf("远程数据源未注册: %s", my.binding.Name)
	} else if len(keys) > 0 {
		if my.values, my.err = remote.Fetch(ctx, keys); my.err != nil {
			my.err = fmt.Errorf("远程数据源 %s 取数失败: %w", my.binding.Name, my.err)
			my.values = nil
		}
	}
	if my.err != nil {
		log.Warn().Err(my.err).Str("field", my.binding.Field).Msg("远程关系字段置null")
	}
}

// fill 按键回填并剥掉编译期补投影的内部键，响应形状严格等于选择集
func (my *remoteJob) fill() {
	for _, source := range my.sources {
		if v, ok := my.values[source[my.binding.Key]]; ok {
			source[my.binding.Field] = v
		} else {
			source[my.binding.Field] = nil
		}
		delete(source, my.binding.Key)
	}
}

// remoteKeyAlias 远程键的内部投影别名（与编译器约定一致）
func remoteKeyAlias(key string) string {
	return (&protocol.RemoteRef{Key: key}).Alias()
}
