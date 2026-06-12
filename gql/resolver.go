package gql

import (
	"context"
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
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

// binding 编译期收集的resolver绑定：宿主对象路径 + 目标字段 + resolver名
type binding struct {
	Path  []string // data根到宿主对象的字段别名路径（数组层级在执行期透明展开）
	Field string   // 要填充的字段别名
	Name  string   // resolver名
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
			if f.Name == ITEMS {
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
		// 查询根字段类型为XxxResult，变更读回直接是实体类型
		className := strings.TrimSuffix(f.Definition.Type.Name(), SUFFIX_RESULT)
		if class, ok := meta.GetNode(className); ok {
			walk(class, f.SelectionSet, []string{f.Alias})
		}
	}
	return bindings
}

// hosts 按路径收集宿主对象，数组层级自动展开
func hosts(root map[string]interface{}, path []string) []map[string]interface{} {
	nodes := []interface{}{root}
	for _, segment := range path {
		flat := make([]interface{}, 0, len(nodes))
		for _, node := range nodes {
			object, ok := node.(map[string]interface{})
			if !ok {
				continue
			}
			switch value := object[segment].(type) {
			case []interface{}:
				flat = append(flat, value...)
			case nil:
			default:
				flat = append(flat, value)
			}
		}
		nodes = flat
	}

	out := make([]map[string]interface{}, 0, len(nodes))
	for _, node := range nodes {
		if object, ok := node.(map[string]interface{}); ok {
			out = append(out, object)
		}
	}
	return out
}

// resolve 按绑定填充resolver字段：批量解析器整列表一次调用，
// 普通解析器逐宿主并行计算；计算与写回分离，避免并发写共享对象
func (my *Executor) resolve(ctx context.Context, bindings []binding, data map[string]interface{}) error {
	for _, b := range bindings {
		resolver, ok := my.resolvers[b.Name]
		if !ok {
			return fmt.Errorf("resolver未注册: %s", b.Name)
		}
		sources := hosts(data, b.Path)
		if len(sources) == 0 {
			continue
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
			return fmt.Errorf("resolver %s 执行失败: %w", b.Name, err)
		}
		for i, source := range sources {
			source[b.Field] = values[i]
		}
	}
	return nil
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
