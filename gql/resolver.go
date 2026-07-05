package gql

import (
	"context"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"golang.org/x/sync/errgroup"
)

// Resolver 自定义字段解析器：处理无法用SQL表达的字段逻辑
// 元数据中通过 Field.Resolver 按名绑定，编译期跳过SQL，执行期填充结果
// 列表场景下Resolve会被并发调用，实现须线程安全；有状态逻辑请实现BatchResolver
type Resolver interface {
	Name() string
	// Resolve 计算单个宿主对象的字段值，source为该对象已查出的字段
	Resolve(ctx context.Context, source map[string]interface{}, args map[string]interface{}) (interface{}, error)
}

// BatchResolver 批量解析器：列表场景一次调用处理全部宿主对象，避免N+1
// 返回值必须与sources等长且一一对应
type BatchResolver interface {
	Resolver
	ResolveBatch(ctx context.Context, sources []map[string]interface{}, args map[string]interface{}) ([]interface{}, error)
}

// binding 编译期收集的后处理绑定：宿主对象路径 + 目标字段 + 处理器名。
// Key非空即远程关系绑定（Name为数据源名，Key为补投影的内部键别名），否则为resolver绑定
type binding struct {
	Path  []string // data根到宿主对象的字段别名路径（数组层级在执行期透明展开）
	Field string   // 要填充的字段别名
	Name  string   // resolver名或远程数据源名
	Key   string   // 远程绑定的宿主键别名（编译期补投影的内部列）
}

// collectBindings 遍历操作选择集，收集所有resolver字段的绑定
func collectBindings(meta *Metadata, operation *ast.OperationDefinition) []binding {
	var bindings []binding

	var walk func(class *protocol.Class, set ast.SelectionSet, path []string)
	walk = func(class *protocol.Class, set ast.SelectionSet, path []string) {
		for _, s := range set {
			f, ok := s.(*ast.Field)
			if !ok {
				continue
			}
			if f.Name == protocol.ITEMS {
				walk(class, f.SelectionSet, append(path[:len(path):len(path)], f.Alias))
				continue
			}
			field, ok := class.Fields[f.Name]
			if !ok {
				continue
			}
			if field.Resolver != "" {
				bindings = append(bindings, binding{
					Path:  path,
					Field: f.Alias,
					Name:  field.Resolver,
				})
				continue
			}
			if field.Remote != nil {
				bindings = append(bindings, binding{
					Path:  path,
					Field: f.Alias,
					Name:  field.Remote.Source,
					Key:   remoteKeyAlias(field.Remote.Key),
				})
				continue
			}
			if field.Column == "" && field.Relation != nil {
				if target, ok := meta.GetNode(field.Relation.TargetClass); ok {
					walk(target, f.SelectionSet, append(path[:len(path):len(path)], f.Alias))
				}
			}
		}
	}

	for _, s := range operation.SelectionSet {
		f, ok := s.(*ast.Field)
		if !ok {
			continue
		}
		// 变更读回直接是实体类型，查询根字段类型为XxxResult包装：
		// 先按原名匹配（本名以Result结尾的实体如ExamResult不可误剪），miss再剪后缀
		className := f.Definition.Type.Name()
		class, ok := meta.GetNode(className)
		if !ok {
			class, ok = meta.GetNode(strings.TrimSuffix(className, protocol.SUFFIX_RESULT))
		}
		if ok {
			walk(class, f.SelectionSet, []string{f.Alias})
		}
	}
	return bindings
}

// hosts 按路径收集宿主对象，数组层级自动展开
// 单遍直收map，无interface{}装箱中间层；每段新建next切片
// （不可复用cur底层数组：数组段展开后宿主数可超上层，原地append会覆写未读元素）
func hosts(root map[string]interface{}, path []string) []map[string]interface{} {
	cur := []map[string]interface{}{root}
	for _, segment := range path {
		var next []map[string]interface{}
		for _, node := range cur {
			switch value := node[segment].(type) {
			case []interface{}:
				for _, item := range value {
					if object, ok := item.(map[string]interface{}); ok {
						next = append(next, object)
					}
				}
			case map[string]interface{}:
				next = append(next, value)
			}
		}
		cur = next
	}
	return cur
}

// resolve 按绑定填充后处理字段。远程关系绑定先行：网络取数并发（各job独立）、
// 回填串行（宿主map非并发安全），失败字段置null并以警告随响应errors返回（不中断）；
// resolver绑定随后：批量解析器整列表一次调用，普通解析器逐宿主并行计算
func (my *Executor) resolve(ctx context.Context, bindings []binding, data map[string]interface{}) (gqlerror.List, error) {
	var warnings gqlerror.List
	var jobs []*remoteJob
	for _, b := range bindings {
		if b.Key == "" {
			continue
		}
		if sources := hosts(data, b.Path); len(sources) > 0 {
			jobs = append(jobs, &remoteJob{binding: b, sources: sources})
		}
	}
	if len(jobs) > 0 {
		group, gctx := errgroup.WithContext(ctx)
		for _, job := range jobs {
			group.Go(func() error { job.fetch(gctx, my.remotes); return nil })
		}
		_ = group.Wait() // job错误不经group传播(容错语义),此处仅同步
		for _, job := range jobs {
			job.fill()
			if job.err != nil {
				warnings = append(warnings, gqlerror.Wrap(job.err))
			}
		}
	}

	for _, b := range bindings {
		if b.Key != "" {
			continue
		}
		sources := hosts(data, b.Path)
		if len(sources) == 0 {
			continue
		}
		resolver, ok := my.resolvers[b.Name]
		if !ok {
			return warnings, fmt.Errorf("resolver未注册: %s", b.Name)
		}

		var values []interface{}
		var err error
		if batch, ok := resolver.(BatchResolver); ok {
			values, err = batch.ResolveBatch(ctx, sources, nil)
			if err == nil && len(values) != len(sources) {
				err = fmt.Errorf("返回数量不匹配: 期望%d实际%d", len(sources), len(values))
			}
		} else {
			values, err = resolveEach(ctx, resolver, sources)
		}
		if err != nil {
			return warnings, fmt.Errorf("resolver %s 执行失败: %w", b.Name, err)
		}
		for i, source := range sources {
			source[b.Field] = values[i]
		}
	}
	return warnings, nil
}

// resolveEach 普通resolver逐宿主有界并发计算，返回与sources对位的结果
func resolveEach(ctx context.Context, resolver Resolver, sources []map[string]interface{}) ([]interface{}, error) {
	values := make([]interface{}, len(sources))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(8)
	for i, source := range sources {
		group.Go(func() error {
			value, err := resolver.Resolve(ctx, source, nil)
			values[i] = value
			return err
		})
	}
	return values, group.Wait()
}
