package gql

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	"github.com/vektah/gqlparser/v2/lexer"
	"github.com/vektah/gqlparser/v2/parser"
	gqlvalidator "github.com/vektah/gqlparser/v2/validator"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
)

const maxDocumentBytes = 1 << 20

// preparedOperation is the immutable request frontend result shared by every
// execution entry point. The selection tree is normalized and variables are
// coerced before a caller may cache, cost, authorize, or execute it.
type preparedOperation struct {
	operation *ast.OperationDefinition
	variables map[string]any
}

// prepareFrontend parses, validates, selects, coerces, and normalizes one
// operation. Caches may retain the operation tree, but request variables are
// always produced afresh by this boundary.
func prepareFrontend(schema *ast.Schema, source, operationName string, variables map[string]any, maxTokens int, rules *validatorrules.Rules) (*preparedOperation, error) {
	doc, errs := loadQuery(schema, source, maxTokens, rules)
	if len(errs) > 0 {
		return nil, errs
	}
	operation := doc.Operations.ForName(operationName)
	if operation == nil {
		if operationName != "" || len(doc.Operations) != 1 {
			return nil, fmt.Errorf("未找到名为'%s'的操作（多操作查询必须提供operationName）", operationName)
		}
		operation = doc.Operations[0]
	}
	coerced, err := coerceVariables(schema, operation, variables)
	if err != nil {
		return nil, err
	}
	selection, err := normalizeSelectionWithSchema(operation.SelectionSet, doc.Fragments, coerced, schema)
	if err != nil {
		return nil, err
	}
	operation.SelectionSet = selection
	return &preparedOperation{operation: operation, variables: coerced}, nil
}

// loadQuery is the sole executable-document parser. The rule set belongs to
// the Engine snapshot, and the lexer stops oversized documents before the
// validator, planner, SQL, or user code can observe them.
func loadQuery(schema *ast.Schema, source string, maxTokens int, rules *validatorrules.Rules) (*ast.QueryDocument, gqlerror.List) {
	doc, err := parser.ParseQueryWithTokenLimit(&ast.Source{Name: "query.graphql", Input: source}, maxTokens)
	if err != nil {
		var gqlErr *gqlerror.Error
		if errors.As(err, &gqlErr) {
			return nil, gqlerror.List{gqlErr}
		}
		return nil, gqlerror.List{gqlerror.Wrap(err)}
	}
	if errs := gqlvalidator.ValidateWithRules(schema, doc, rules); len(errs) > 0 {
		return nil, errs
	}
	if errs := normalizeExactLiterals(schema, doc); len(errs) > 0 {
		return nil, errs
	}
	return doc, nil
}

func normalizeSelectionWithSchema(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]interface{}, schema *ast.Schema) (ast.SelectionSet, error) {
	return normalizeSelections(set, fragments, variables, make(map[string]bool), schema, false)
}

func normalizeSelectionTemplate(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]interface{}, schema *ast.Schema) (ast.SelectionSet, error) {
	return normalizeSelections(set, fragments, variables, make(map[string]bool), schema, true)
}

func normalizeSelections(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]interface{}, active map[string]bool, schema *ast.Schema, copyNodes bool) (ast.SelectionSet, error) {
	out := make(ast.SelectionSet, 0, len(set))
	for _, selection := range set {
		var directives ast.DirectiveList
		var nested ast.SelectionSet
		switch node := selection.(type) {
		case *ast.Field:
			if copyNodes {
				copied := *node
				copied.Arguments = cloneArguments(node.Arguments)
				node, selection = &copied, &copied
			}
			directives, nested = node.Directives, node.SelectionSet
		case *ast.FragmentSpread:
			directives = node.Directives
			fragment := fragments.ForName(node.Name)
			if fragment == nil {
				return nil, fmt.Errorf("未找到fragment %q", node.Name)
			}
			if active[node.Name] {
				return nil, fmt.Errorf("fragment循环引用: %s", node.Name)
			}
			if skip, err := shouldSkip(directives, variables); err != nil {
				return nil, err
			} else if skip {
				continue
			}
			if node.Definition != nil {
				if err := validateFragmentCondition(node.Definition.Definition, node.ObjectDefinition); err != nil {
					return nil, err
				}
			}
			active[node.Name] = true
			expanded, err := normalizeSelections(fragment.SelectionSet, fragments, variables, active, schema, copyNodes)
			delete(active, node.Name)
			if err != nil {
				return nil, err
			}
			if node.ObjectDefinition != nil && node.ObjectDefinition.IsAbstractType() && fragment.TypeCondition != "" {
				out = append(out, &ast.InlineFragment{
					TypeCondition: fragment.TypeCondition, SelectionSet: expanded,
					ObjectDefinition: node.ObjectDefinition, Position: node.Position,
				})
				continue
			}
			out = append(out, expanded...)
			continue
		case *ast.InlineFragment:
			if copyNodes {
				copied := *node
				node, selection = &copied, &copied
			}
			directives, nested = node.Directives, node.SelectionSet
		default:
			continue
		}
		if skip, err := shouldSkip(directives, variables); err != nil {
			return nil, err
		} else if skip {
			continue
		}
		if inline, ok := selection.(*ast.InlineFragment); ok {
			if err := validateInlineCondition(inline, schema); err != nil {
				return nil, err
			}
		}
		if nested != nil {
			expanded, err := normalizeSelections(nested, fragments, variables, active, schema, copyNodes)
			if err != nil {
				return nil, err
			}
			switch node := selection.(type) {
			case *ast.Field:
				node.SelectionSet = expanded
			case *ast.InlineFragment:
				if node.TypeCondition != "" && node.ObjectDefinition != nil && node.ObjectDefinition.IsAbstractType() {
					node.SelectionSet = expanded
					node.Directives = nil
					out = append(out, node)
					continue
				}
				out = append(out, expanded...)
				continue
			}
		}
		// Directives are consumed here; leaving them attached would make a later
		// compiler or resolver accidentally apply a second, different policy.
		switch node := selection.(type) {
		case *ast.Field:
			node.Directives = nil
		case *ast.InlineFragment:
			node.Directives = nil
		}
		out = append(out, selection)
	}
	return mergeSelectionSet(out), nil
}

func cloneArguments(arguments ast.ArgumentList) ast.ArgumentList {
	if len(arguments) == 0 {
		return nil
	}
	out := make(ast.ArgumentList, len(arguments))
	for i, argument := range arguments {
		copy := *argument
		copy.Value = cloneValue(argument.Value)
		out[i] = &copy
	}
	return out
}

func cloneValue(value *ast.Value) *ast.Value {
	if value == nil {
		return nil
	}
	copy := *value
	if len(value.Children) > 0 {
		copy.Children = make(ast.ChildValueList, len(value.Children))
		for i, child := range value.Children {
			if child == nil {
				continue
			}
			childCopy := *child
			childCopy.Value = cloneValue(child.Value)
			copy.Children[i] = &childCopy
		}
	}
	return &copy
}

func validateFragmentCondition(fragment, parent *ast.Definition) error {
	if fragment == nil {
		return nil
	}
	if !fragment.IsCompositeType() {
		return fmt.Errorf("fragment类型条件必须是复合类型: %q", fragment.Name)
	}
	if parent != nil && !parent.IsCompositeType() {
		return fmt.Errorf("fragment父类型必须是复合类型: %q", parent.Name)
	}
	return nil
}

func validateInlineCondition(fragment *ast.InlineFragment, schema *ast.Schema) error {
	if fragment.TypeCondition == "" {
		return nil
	}
	if fragment.ObjectDefinition != nil && !fragment.ObjectDefinition.IsCompositeType() {
		return fmt.Errorf("inline fragment父类型必须是复合类型")
	}
	if schema != nil {
		if def := schema.Types[fragment.TypeCondition]; def == nil || !def.IsCompositeType() {
			return fmt.Errorf("inline fragment类型条件无效: %s", fragment.TypeCondition)
		}
	}
	return nil
}

// mergeSelectionSet performs GraphQL field collection after directives and
// fragments have been normalized. The first occurrence owns response order;
// subsequent compatible occurrences contribute only their child selections.
func mergeSelectionSet(set ast.SelectionSet) ast.SelectionSet {
	if len(set) < 2 {
		return set
	}
	merged := make(ast.SelectionSet, 0, len(set))
	byKey := make(map[string]*ast.Field, len(set))
	for _, selection := range set {
		field, ok := selection.(*ast.Field)
		if !ok {
			merged = append(merged, selection)
			continue
		}
		key := field.Name
		if field.Alias != "" {
			key = field.Alias
		}
		if prior := byKey[key]; prior != nil {
			prior.SelectionSet = append(prior.SelectionSet, field.SelectionSet...)
			prior.SelectionSet = mergeSelectionSet(prior.SelectionSet)
			continue
		}
		field.SelectionSet = mergeSelectionSet(field.SelectionSet)
		byKey[key] = field
		merged = append(merged, field)
	}
	return merged
}

func shouldSkip(directives ast.DirectiveList, variables map[string]interface{}) (bool, error) {
	for _, directive := range directives {
		if directive.Name != "skip" && directive.Name != "include" {
			continue
		}
		arg := directive.Arguments.ForName("if")
		if arg == nil || arg.Value == nil {
			return false, fmt.Errorf("指令@%s缺少if参数", directive.Name)
		}
		value, err := arg.Value.Value(variables)
		if err != nil {
			return false, fmt.Errorf("求值@%s失败: %w", directive.Name, err)
		}
		flag, ok := value.(bool)
		if !ok {
			return false, fmt.Errorf("指令@%s的if参数必须是Boolean", directive.Name)
		}
		if (directive.Name == "skip" && flag) || (directive.Name == "include" && !flag) {
			return true, nil
		}
	}
	return false, nil
}

func hasConditionalDirective(query string) bool {
	if strings.IndexByte(query, '@') < 0 {
		return false
	}
	scanner := lexer.New(&ast.Source{Name: "query.graphql", Input: query})
	afterAt := false
	for {
		token, err := scanner.ReadToken()
		if err != nil {
			return false // 正常解析路径负责返回语法错误
		}
		if afterAt && token.Kind == lexer.Comment {
			continue
		}
		if afterAt && token.Kind == lexer.Name && (token.Value == "skip" || token.Value == "include") {
			return true
		}
		afterAt = token.Kind == lexer.At
		if token.Kind == lexer.EOF {
			return false
		}
	}
}

func selectionShape(set ast.SelectionSet) string {
	var shape strings.Builder
	var write func(ast.SelectionSet)
	write = func(current ast.SelectionSet) {
		shape.WriteByte('{')
		for _, selection := range current {
			field, ok := selection.(*ast.Field)
			if !ok {
				continue
			}
			shape.WriteString(field.Alias)
			shape.WriteByte(':')
			shape.WriteString(field.Name)
			write(field.SelectionSet)
		}
		shape.WriteByte('}')
	}
	write(set)
	return shape.String()
}

const maxUint64 = ^uint64(0)

func saturatingAdd(a, b uint64) uint64 {
	if maxUint64-a < b {
		return maxUint64
	}
	return a + b
}
func saturatingMul(a, b uint64) uint64 {
	if a != 0 && b > maxUint64/a {
		return maxUint64
	}
	return a * b
}

// operationCost consumes the normalized selection tree. Its one formula covers
// scalar fields, bounded result lists, and bulk input without a second planner:
// input count * (field weight + result bound * child cost).
func operationCost(operation *ast.OperationDefinition, variables map[string]interface{}, defaultLimit int, capabilities map[string]Capability) uint64 {
	if operation == nil {
		return 0
	}
	root := "Query"
	if operation.Operation == ast.Mutation {
		root = "Mutation"
	} else if operation.Operation == ast.Subscription {
		root = "Subscription"
	}
	var walk func(ast.SelectionSet) uint64
	walk = func(set ast.SelectionSet) uint64 {
		var total uint64
		for _, selection := range set {
			field, ok := selection.(*ast.Field)
			if !ok {
				if fragment, inline := selection.(*ast.InlineFragment); inline {
					total = saturatingAdd(total, walk(fragment.SelectionSet))
				}
				continue
			}
			cost := uint64(1)
			parent := root
			if field.ObjectDefinition != nil {
				parent = field.ObjectDefinition.Name
			}
			if capability, ok := capabilities[parent+"."+field.Name]; ok && capability.Cost > 0 {
				cost = uint64(capability.Cost)
			}
			child := walk(field.SelectionSet)
			resultMultiplier := uint64(1)
			if child > 0 {
				if field.Definition != nil && field.Definition.Type != nil && field.Definition.Type.Elem != nil {
					resultMultiplier = maxUint64
					if defaultLimit > 0 {
						resultMultiplier = uint64(defaultLimit)
					}
					for _, name := range []string{"limit", "first", "last"} {
						if arg := field.Arguments.ForName(name); arg != nil {
							if n, ok := costIntValue(arg.Value, variables); ok && n > 0 {
								resultMultiplier = uint64(n)
							}
						}
					}
				}
			}
			unit := saturatingAdd(cost, saturatingMul(child, resultMultiplier))
			inputWork := bulkInputLength(field, variables)
			if operation.Operation == ast.Mutation {
				inputWork = mutationInputWorkload(field, variables, inputWork)
			}
			total = saturatingAdd(total, saturatingMul(unit, inputWork))
		}
		return total
	}
	return walk(operation.SelectionSet)
}

func mutationInputWorkload(field *ast.Field, variables map[string]interface{}, base uint64) uint64 {
	arg := field.Arguments.ForName("input")
	if arg == nil || arg.Value == nil {
		return base
	}
	value, err := arg.Value.Value(variables)
	if err != nil {
		return base
	}
	return saturatingAdd(base, nestedMutationInputWork(value))
}

func nestedMutationInputWork(value any) uint64 {
	switch value := value.(type) {
	case []any:
		var total uint64
		for _, item := range value {
			total = saturatingAdd(total, nestedMutationInputWork(item))
		}
		return total
	case map[string]any:
		var total uint64
		for name, nested := range value {
			switch name {
			case "connect", "disconnect":
				for range mutationInputItems(nested) {
					total = saturatingAdd(total, 2) // relation operation + key parameter
				}
			case "create":
				for _, item := range mutationInputItems(nested) {
					total = saturatingAdd(total, 1) // nested row
					total = saturatingAdd(total, nestedMutationInputWork(item))
				}
			default:
				switch nested.(type) {
				case map[string]any, []any:
					// Descend through relation wrappers and nested input rows.
					total = saturatingAdd(total, nestedMutationInputWork(nested))
				default:
					total = saturatingAdd(total, 1) // SQL parameter
				}
			}
		}
		return total
	default:
		return 0
	}
}

func mutationInputItems(value any) []any {
	if value == nil {
		return nil
	}
	if list, ok := value.([]any); ok {
		return list
	}
	return []any{value}
}

func bulkInputLength(field *ast.Field, variables map[string]interface{}) uint64 {
	arg := field.Arguments.ForName("input")
	if arg == nil || field.Definition == nil || arg.Value == nil {
		return 1
	}
	definition := field.Definition.Arguments.ForName("input")
	if definition == nil || definition.Type == nil || definition.Type.Elem == nil {
		return 1
	}
	if arg.Value.Kind == ast.ListValue {
		return uint64(len(arg.Value.Children))
	}
	if arg.Value.Kind == ast.Variable {
		if list, ok := variables[arg.Value.Raw].([]interface{}); ok {
			return uint64(len(list))
		}
	}
	return 1
}

func costIntValue(value *ast.Value, variables map[string]interface{}) (int, bool) {
	if value == nil {
		return 0, false
	}
	if value.Kind == ast.Variable {
		v, ok := variables[value.Raw]
		if !ok {
			return 0, false
		}
		switch n := v.(type) {
		case int:
			return n, true
		case int8:
			return int(n), true
		case int16:
			return int(n), true
		case int32:
			return int(n), true
		case int64:
			return int(n), true
		case uint:
			return int(n), true
		case uint8:
			return int(n), true
		case uint16:
			return int(n), true
		case uint32:
			return int(n), true
		case uint64:
			return int(n), true
		}
		return 0, false
	}
	if n, err := strconv.Atoi(value.Raw); err == nil {
		return n, true
	}
	return 0, false
}
