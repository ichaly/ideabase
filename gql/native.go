package gql

import (
	"context"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

type nativeResolver struct {
	input, output *inputBinding
	operation     string
}

func (my *Engine) bindNative(d Define, field *ast.FieldDefinition) error {
	if d.inputType == nil || d.inputType.Kind() != reflect.Struct || d.outputType == nil {
		return fmt.Errorf("原生Resolver必须提供强类型struct参数")
	}
	if d.sourceType == nil {
		return fmt.Errorf("原生根Resolver必须使用Root[I, O]")
	}
	if _, ok := reflect.Zero(d.sourceType).Interface().(rootFactory); !ok {
		return fmt.Errorf("原生根Resolver必须使用Root[I, O]")
	}
	var args ast.FieldList
	for _, arg := range field.Arguments {
		args = append(args, &ast.FieldDefinition{Name: arg.Name, Type: arg.Type})
	}
	var failure error
	eachField(d.inputType, func(f reflect.StructField, name, _ string) {
		if args.ForName(name) != nil || failure != nil {
			return
		}
		kind := deriveType(f.Type, true)
		if scalar := my.schema.Types[kind]; scalar == nil || scalar.Kind != ast.Scalar {
			failure = fmt.Errorf("%s扩展参数必须是已有标量: %s", d.Class+"."+d.Name, name)
			return
		}
		typ := ast.NamedType(kind, nil)
		if required(f.Tag.Get("validate")) {
			typ.NonNull = true
		}
		field.Arguments = append(field.Arguments, &ast.ArgumentDefinition{Name: name, Type: typ})
		args = append(args, &ast.FieldDefinition{Name: name, Type: typ})
	})
	if failure != nil {
		return failure
	}
	input, err := my.bindInput(d.inputType, nil, args)
	if err != nil {
		return err
	}
	output, err := my.bindInput(d.outputType, field.Type, nil)
	if err != nil {
		return fmt.Errorf("返回值: %w", err)
	}
	spec, _ := my.metadata.LookupOperation(d.Class + "." + d.Name)
	if d.Class == "Mutation" {
		for _, arg := range input.fields {
			if arg.name == protocol.INPUT {
				row := arg.plan
				if row.elem != nil {
					row = row.elem
				}
				for _, f := range row.fields {
					if column := spec.Class.Fields[f.name]; column != nil && column.Column != "" {
						f.plan.nullable = column.Nullable
						if f.plan.scalar != nil {
							f.plan.scalar.nullable = column.Nullable
						}
					}
				}
			}
		}
	}
	if my.native == nil {
		my.native = make(map[string]*nativeResolver)
	}
	my.native[d.Class+"."+d.Name] = &nativeResolver{input, output, spec.Operation}
	return nil
}

// 只标记需要检查嵌套写入的入口；直接调用的其他CRUD不会执行当前字段的Resolver。
func (my *Engine) indexNativeRelations() {
	my.nativeRoots = make(map[string]bool)
	my.protectedWrites = make(map[string]map[string]bool)
	parents := make(map[string][]string)
	for _, class := range my.metadata.Nodes {
		for _, field := range class.Fields {
			if r := field.Relation; r != nil {
				parents[r.TargetClass] = append(parents[r.TargetClass], class.Name)
				if r.Through != nil {
					parents[r.Through.TableName] = append(parents[r.Through.TableName], class.Name)
				}
			}
		}
	}
	var queue []string
	for name := range my.native {
		spec, ok := my.metadata.LookupOperation(name)
		if !ok || !strings.HasPrefix(name, "Mutation.") {
			continue
		}
		if my.protectedWrites[spec.Class.Name] == nil {
			my.protectedWrites[spec.Class.Name] = make(map[string]bool)
		}
		my.protectedWrites[spec.Class.Name][spec.Operation] = true
		queue = append(queue, spec.Class.Name)
	}
	affected := make(map[string]bool)
	for i := 0; i < len(queue); i++ {
		name := queue[i]
		if affected[name] {
			continue
		}
		affected[name] = true
		class, _ := my.metadata.GetNode(name)
		queue = append(queue, parents[name]...)
		queue = append(queue, parents[class.Table]...)
	}
	for name := range my.metadata.operations {
		spec, ok := my.metadata.LookupOperation(name)
		if ok {
			my.nativeRoots[name] = affected[spec.Class.Name] && (spec.Operation == protocol.CREATE || spec.Operation == protocol.UPDATE || spec.Operation == protocol.UPSERT)
		}
	}
}

func (my *Engine) checkNativeRelations(class *protocol.Class, rows []map[string]any) error {
	for _, row := range rows {
		for name, value := range row {
			field := class.Fields[name]
			operations, ok := value.(map[string]any)
			if !ok || field == nil || field.Column != "" || field.Relation == nil {
				continue
			}
			r := field.Relation
			if r.Through == nil && (operations[protocol.CONNECT] != nil || operations[protocol.DISCONNECT] != nil) && my.protectedWrites[r.TargetClass][protocol.UPDATE] {
				return fmt.Errorf("%s的关系更新必须通过对应根Resolver执行", r.TargetClass)
			}
			if _, create := operations[protocol.CREATE]; create && my.protectedWrites[r.TargetClass][protocol.CREATE] {
				return fmt.Errorf("%s的嵌套create必须通过对应根Resolver执行", r.TargetClass)
			}
			if r.Through != nil {
				if target, ok := my.metadata.GetNode(r.Through.TableName); ok && len(my.protectedWrites[target.Name]) > 0 {
					return fmt.Errorf("%s的关系写入必须通过对应根Resolver执行", target.Name)
				}
			}
			if input, ok := operations[protocol.CREATE]; ok {
				nested, err := policyRows(input)
				if err != nil {
					return err
				}
				if target, ok := my.metadata.GetNode(r.TargetClass); ok {
					if err := my.checkNativeRelations(target, nested); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

func mutationRows(args map[string]any) ([]map[string]any, error) {
	if input, ok := args[protocol.INPUT]; ok {
		return policyRows(input)
	}
	return nil, nil
}

func (my *Engine) executeNativeResolver(ctx context.Context, operation *ast.OperationDefinition, field *ast.Field, variables map[string]any, resolver Resolver) (any, gqlerror.List, error) {
	coordinate := resolver.Name()
	plan := my.native[coordinate]
	args := cloneVariableValue(field.ArgumentMap(variables)).(map[string]any)
	call := &rootCall{coordinate: coordinate, native: plan}
	hctx, cancel := handlerContext(my.executionContextFor(ctx, field.SelectionSet, coordinate), resolver)
	defer cancel()
	defer func() { call.mu.Lock(); call.closed = true; call.mu.Unlock() }()
	validate := func(input any) (map[string]any, error) {
		v, err := inputValidator(hctx)
		if err != nil {
			return nil, err
		}
		value := reflect.ValueOf(input).Elem()
		var selected []string
		// 先取得PATCH字段集，验证器规范化后再编码，避免写入未经裁剪的值。
		if plan.operation == protocol.UPDATE {
			plan.input.selectFields(value, "", &call.input, &selected)
			err = v.StructPartialCtx(hctx, input, selected...)
		} else {
			err = v.StructCtx(hctx, input)
		}
		if err != nil {
			return nil, inputValidationError(err)
		}
		encoded, err := plan.input.encode(value, args, &call.input)
		if err != nil {
			return nil, &publicFailure{err.Error(), "VALIDATION_ERROR"}
		}
		return encoded.(map[string]any), nil
	}
	call.next = func(input any) (any, error) {
		values, err := validate(input)
		if err != nil {
			return nil, err
		}
		root, sub := *field, *operation
		root.Arguments = make(ast.ArgumentList, 0, len(values))
		vars := make(map[string]any, len(variables)+len(values))
		maps.Copy(vars, variables)
		for _, definition := range field.Definition.Arguments {
			value, exists := values[definition.Name]
			if !exists {
				continue
			}
			key := "_resolver_" + definition.Name
			for {
				if _, exists := vars[key]; !exists {
					break
				}
				key += "_"
			}
			vars[key] = value
			root.Arguments = append(root.Arguments, &ast.Argument{Name: definition.Name, Value: &ast.Value{Kind: ast.Variable, Raw: key, ExpectedType: definition.Type}})
		}
		root.SelectionSet = slices.Clone(field.SelectionSet)
		projection := plan.output.project(my.schema, field.Definition.Type, &root.SelectionSet)
		sub.SelectionSet = ast.SelectionSet{&root}
		nctx, err := my.authorizeProvider(hctx, &sub, vars)
		if err != nil {
			return nil, err
		}
		if operation.Operation == ast.Mutation {
			spec, _ := my.metadata.LookupOperation(coordinate)
			rows, err := mutationRows(values)
			if err != nil {
				return nil, err
			}
			if err = my.checkNativeRelations(spec.Class, rows); err != nil {
				return nil, err
			}
		}
		call.result, call.warnings, err = my.executeDefaultRootField(nctx, &sub, &root, vars, &sync.Map{})
		if err != nil {
			return nil, err
		}
		return extractOutput(call.result, projection), nil
	}
	call.finish = func(input, out any) (any, error) {
		call.mu.Lock()
		defer call.mu.Unlock()
		if call.err != nil {
			return nil, call.err
		}
		if call.called && call.result == nil {
			return nil, nil
		}
		if !call.called {
			if _, err := validate(input); err != nil {
				return nil, err
			}
			if decision, ok := PolicyDecisionFromContext(hctx, coordinate); ok && hasRowPolicy(decision) && !resolver.Define().PolicyAware {
				return nil, publicPolicyFailure("POLICY_HANDLER_REQUIRED", "覆盖原生Resolver必须显式处理行策略，或调用Next")
			}
		}
		mapped, err := plan.output.encode(reflect.ValueOf(out), nil, nil)
		if err != nil {
			return nil, err
		}
		return mergeOutput(call.result, mapped, field.SelectionSet), nil
	}
	var result any
	err := safely(func() error {
		var err error
		result, err = resolver.Resolve(hctx, call, args)
		return handlerResult(hctx, err)
	})
	return result, call.warnings, err
}
