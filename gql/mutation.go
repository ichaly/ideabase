package gql

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"sync"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

// Mutation 为一个实体的原生根变更注册校验和事务钩子，不替换 SQL 执行器。
// 嵌套写入受保护实体会被拒绝，必须通过其根变更执行，避免绕过钩子。
type Mutation struct {
	Class       string
	Arguments   map[string]string                                              // 可选的标量参数，仅提供给钩子，不映射数据库列。
	Returning   []string                                                       // 钩子必需的标量字段，仍受字段读取权限约束，不泄漏到响应。
	After       func(context.Context, MutationRequest, []map[string]any) error // 原生 SQL 后、提交前执行。
	inputType   reflect.Type
	inputFields *mutationInput
	bind        func(context.Context, MutationRequest, []map[string]any, *mutationInput) error
}

// MutationRequest 提供操作类型及请求参数；输入处理通过 Change.Input 写回。
type MutationRequest struct {
	Operation string
	Arguments map[string]any
}

// Mutations 在 Build 前一次注册每个实体的扩展。
func (my *Builder) Mutations(items ...Mutation) error {
	if my.built {
		return fmt.Errorf("builder已冻结：Mutations仅限Build前调用")
	}
	my.mutations = append(my.mutations, items...)
	return nil
}

func (my *Engine) installMutations(items []Mutation) error {
	my.mutations = make(map[string]Mutation, len(items))
	my.mutationRoots = make(map[string]bool)
	for _, item := range items {
		class, ok := my.metadata.GetNode(item.Class)
		if !ok || class == nil || class.Name != item.Class || my.schema.Mutation == nil || my.mutations[item.Class].Class != "" {
			return fmt.Errorf("mutation实体不存在或重复: %s", item.Class)
		}
		for _, name := range item.Returning {
			if field := class.Fields[name]; field == nil || field.Column == "" {
				return fmt.Errorf("mutation字段不存在或不是标量: %s.%s", item.Class, name)
			}
		}
		item.Arguments, item.Returning = maps.Clone(item.Arguments), slices.Clone(item.Returning)
		if item.After == nil {
			item.Returning = nil
		}
		var err error
		item.inputFields, err = my.mutationInputFields(item, class)
		if err != nil {
			return err
		}
		my.mutations[item.Class] = item
		for _, field := range my.schema.Mutation.Fields {
			spec, ok := my.metadata.LookupOperation("Mutation." + field.Name)
			if !ok || spec.Class.Name != item.Class {
				continue
			}
			for _, name := range slices.Sorted(maps.Keys(item.Arguments)) {
				typeName := item.Arguments[name]
				if kind := my.schema.Types[typeName]; kind == nil || kind.Kind != ast.Scalar || field.Arguments.ForName(name) != nil {
					return fmt.Errorf("mutation扩展参数无效: %s.%s", field.Name, name)
				}
				field.Arguments = append(field.Arguments, &ast.ArgumentDefinition{Name: name, Type: ast.NamedType(typeName, nil)})
			}
		}
	}
	if len(items) > 0 {
		// 启动期计算关系可达性；无关根变更仍走单 SQL 快路径。
		affected := make(map[string]bool, len(items))
		for name := range my.mutations {
			affected[name] = true
			class, _ := my.metadata.GetNode(name)
			affected[class.Table] = true
		}
		for changed := true; changed; {
			changed = false
			for _, class := range my.metadata.Nodes {
				if affected[class.Name] {
					continue
				}
				for _, field := range class.Fields {
					if r := field.Relation; r != nil && (affected[r.TargetClass] || r.Through != nil && affected[r.Through.TableName]) {
						affected[class.Name], changed = true, true
						affected[class.Table] = true
						break
					}
				}
			}
		}
		for name := range my.metadata.operations {
			spec, ok := my.metadata.LookupOperation(name)
			if !ok || spec.Class == nil {
				continue
			}
			_, protected := my.mutations[spec.Class.Name]
			my.mutationRoots[name] = protected || affected[spec.Class.Name] && (spec.Operation == protocol.CREATE || spec.Operation == protocol.UPDATE || spec.Operation == protocol.UPSERT)
		}
		my.source = formatSchema(my.schema)
		return my.rebuild()
	}
	return nil
}

func (my *Engine) executeMutation(ctx context.Context, operation *ast.OperationDefinition, field *ast.Field, variables map[string]any, plans *sync.Map) (any, gqlerror.List, error) {
	spec, ok := my.metadata.LookupOperation("Mutation." + field.Name)
	if !ok {
		return my.executeDefaultRootField(ctx, operation, field, variables, plans)
	}
	args := cloneVariableValue(field.ArgumentMap(variables)).(map[string]any)
	rows, err := mutationRows(args)
	if err != nil {
		return nil, nil, err
	}
	if err := my.checkMutationRelations(spec.Class, rows); err != nil {
		return nil, nil, err
	}
	hook, ok := my.mutations[spec.Class.Name]
	if !ok {
		return my.executeDefaultRootField(ctx, operation, field, variables, plans)
	}
	if len(hook.Returning) > 0 && field.Definition.Type.Name() == "Int" {
		return nil, nil, &publicFailure{fmt.Sprintf("%s的事务钩子需要返回行，请使用按主键或批量返回行的变更", hook.Class), "MUTATION_RETURNING_REQUIRED"}
	}
	ctx = my.executionContextFor(ctx, field.SelectionSet, "Mutation."+field.Name)
	if hook.bind != nil {
		if err = safely(func() error { return hook.bind(ctx, MutationRequest{spec.Operation, args}, rows, hook.inputFields) }); err != nil {
			return nil, nil, err
		}
	}
	// 不修改共享 AST/变量/缓存；让原编译器处理变换后的输入和投影。
	root, sub := *field, *operation
	root.Arguments = nil
	values := make(map[string]any, len(variables)+len(args))
	maps.Copy(values, variables)
	for _, name := range slices.Sorted(maps.Keys(args)) {
		definition := field.Definition.Arguments.ForName(name)
		if definition == nil {
			return nil, nil, fmt.Errorf("未知mutation参数: %s", name)
		}
		key := "_mutation_" + name
		for {
			if _, exists := values[key]; !exists {
				break
			}
			key += "_"
		}
		values[key] = args[name]
		root.Arguments = append(root.Arguments, &ast.Argument{Name: name, Value: &ast.Value{Kind: ast.Variable, Raw: key, ExpectedType: definition.Type}})
	}
	root.SelectionSet = slices.Clone(field.SelectionSet)
	selection := &root.SelectionSet
	wrapper := ""
	if spec.Shape.Result() && len(hook.Returning) > 0 {
		wrapper = mutationAlias(root.SelectionSet)
		definition := my.schema.Types[field.Definition.Type.Name()].Fields.ForName("returning")
		returning := &ast.Field{Name: "returning", Alias: wrapper, Definition: definition, ObjectDefinition: my.schema.Types[field.Definition.Type.Name()]}
		root.SelectionSet = append(root.SelectionSet, returning)
		selection = &returning.SelectionSet
	}
	aliases := make(map[string]string, len(hook.Returning))
	if field.Definition.Type.Name() != "Int" {
		for _, name := range hook.Returning {
			alias := mutationAlias(*selection)
			kind := my.schema.Types[hook.Class]
			*selection = append(*selection, &ast.Field{Name: name, Alias: alias, Definition: kind.Fields.ForName(name), ObjectDefinition: kind})
			aliases[alias] = name
		}
	}
	sub.SelectionSet = ast.SelectionSet{&root}
	if my.policy != nil {
		if ctx, err = my.authorizeProvider(ctx, &sub, values); err != nil {
			return nil, nil, err
		}
	}
	result, warnings, err := my.executeDefaultRootField(ctx, &sub, &root, values, &sync.Map{})
	if err != nil {
		return nil, warnings, err
	}
	if hook.After == nil {
		return result, warnings, nil
	}
	projected := result
	if wrapper != "" {
		object, _ := result.(map[string]any)
		projected = object[wrapper]
		delete(object, wrapper)
	}
	var returned []map[string]any
	collect := func(object map[string]any) {
		row := make(map[string]any, len(aliases))
		for alias, name := range aliases {
			row[name] = object[alias]
			delete(object, alias)
		}
		returned = append(returned, row)
	}
	switch value := projected.(type) {
	case map[string]any:
		collect(value)
	case []any:
		for _, item := range value {
			if row, ok := item.(map[string]any); ok {
				collect(row)
			}
		}
	}
	err = safely(func() error { return hook.After(ctx, MutationRequest{spec.Operation, args}, returned) })
	return result, warnings, err
}

func mutationRows(args map[string]any) ([]map[string]any, error) {
	if input, ok := args[protocol.INPUT]; ok {
		return policyRows(input)
	}
	return nil, nil
}

// 关系 CTE 不具有独立根钩子的结果边界；包括中间表和更深的 create 都必须显式拒绝。
func (my *Engine) checkMutationRelations(class *protocol.Class, rows []map[string]any) error {
	for _, row := range rows {
		for name, value := range row {
			field := class.Fields[name]
			operations, ok := value.(map[string]any)
			if !ok || field == nil || field.Column != "" || field.Relation == nil {
				continue
			}
			names := []string{field.Relation.TargetClass}
			if through := field.Relation.Through; through != nil {
				names = append(names, through.TableName)
			}
			for _, name := range names {
				if target, ok := my.metadata.GetNode(name); ok {
					if _, protected := my.mutations[target.Name]; protected {
						return &publicFailure{fmt.Sprintf("%s必须通过原生根变更执行事务钩子", target.Name), "MUTATION_HOOK_REQUIRED"}
					}
				}
			}
			if input, ok := operations[protocol.CREATE]; ok {
				nested, err := policyRows(input)
				if err != nil {
					return err
				}
				if target, ok := my.metadata.GetNode(field.Relation.TargetClass); ok {
					if err := my.checkMutationRelations(target, nested); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func mutationAlias(selection ast.SelectionSet) string {
	alias := "_mutation"
	for {
		found := false
		for _, item := range selection {
			if field, ok := item.(*ast.Field); ok && field.Alias == alias {
				found = true
				break
			}
		}
		if !found {
			return alias
		}
		alias += "_"
	}
}
