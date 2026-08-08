package gql

import (
	"fmt"
	"strings"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/lexer"
)

// normalizeSelection evaluates the standard conditional directives and expands
// fragments before authorization, compilation, and resolver collection. The
// compiler consequently receives one honest, directive-free selection shape.
func normalizeSelection(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]interface{}) (ast.SelectionSet, error) {
	return normalizeSelections(set, fragments, variables, make(map[string]bool))
}

func normalizeSelections(set ast.SelectionSet, fragments ast.FragmentDefinitionList, variables map[string]interface{}, active map[string]bool) (ast.SelectionSet, error) {
	out := make(ast.SelectionSet, 0, len(set))
	for _, selection := range set {
		var directives ast.DirectiveList
		var nested ast.SelectionSet
		switch node := selection.(type) {
		case *ast.Field:
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
			active[node.Name] = true
			expanded, err := normalizeSelections(fragment.SelectionSet, fragments, variables, active)
			delete(active, node.Name)
			if err != nil {
				return nil, err
			}
			out = append(out, expanded...)
			continue
		case *ast.InlineFragment:
			directives, nested = node.Directives, node.SelectionSet
		default:
			continue
		}
		if skip, err := shouldSkip(directives, variables); err != nil {
			return nil, err
		} else if skip {
			continue
		}
		if nested != nil {
			expanded, err := normalizeSelections(nested, fragments, variables, active)
			if err != nil {
				return nil, err
			}
			switch node := selection.(type) {
			case *ast.Field:
				node.SelectionSet = expanded
			case *ast.InlineFragment:
				// Type conditions are already validated by gqlparser; the SQL
				// compiler consumes the same flattened field shape as named
				// fragments, so inline fragments disappear at this boundary.
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
	return out, nil
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
