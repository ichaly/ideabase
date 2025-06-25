// Package pgsql WHERE子句处理模块
package pgsql

import (
	"fmt"
	"strings"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/vektah/gqlparser/v2/ast"
)

// buildWhere 构建WHERE子句 - 使用完善的WHERE处理
func (my *Dialect) buildWhere(ctx *compiler.Context, args ast.ArgumentList) error {
	whereArg := args.ForName(gql.WHERE)
	if whereArg == nil || whereArg.Value == nil {
		return nil
	}

	ctx.Space("WHERE")
	return my.buildWhereValue(ctx, whereArg.Value)
}

// buildWhereValue 构建WHERE条件值
func (my *Dialect) buildWhereValue(ctx *compiler.Context, value *ast.Value) error {
	if value == nil {
		return nil
	}

	// 处理原始值（字面量）
	if value.Raw != "" {
		return my.buildRawValue(ctx, value)
	}

	// 处理复合条件
	if len(value.Children) == 0 {
		return nil
	}

	// 如果只有一个子条件，不需要额外的括号
	if len(value.Children) == 1 {
		return my.buildChildValue(ctx, value.Children[0])
	}

	// 多个子条件，使用AND连接
	ctx.Write("(")
	for i, child := range value.Children {
		if i > 0 {
			ctx.Space("AND")
		}
		if err := my.buildChildValue(ctx, child); err != nil {
			return err
		}
	}
	ctx.Write(")")

	return nil
}

// buildRawValue 构建原始值
func (my *Dialect) buildRawValue(ctx *compiler.Context, value *ast.Value) error {
	switch value.Kind {
	case ast.EnumValue:
		// 枚举值处理（如排序方向）
		ctx.Write(strings.ReplaceAll(value.Raw, "_", " "))
	case ast.BlockValue:
		// 块值直接写入（如原始SQL片段）
		ctx.Write(value.Raw)
	default:
		// 其他值作为参数处理
		return my.buildParam(ctx, value)
	}
	return nil
}

// buildChildValue 构建子条件
func (my *Dialect) buildChildValue(ctx *compiler.Context, child *ast.ChildValue) error {
	if child == nil || child.Name == "" {
		return fmt.Errorf("invalid child value: empty name")
	}

	switch child.Name {
	case gql.AND:
		return my.buildLogicalOperator(ctx, child, "AND")
	case gql.OR:
		return my.buildLogicalOperator(ctx, child, "OR")
	case gql.NOT:
		return my.buildNotOperator(ctx, child)
	default:
		return my.buildFieldCondition(ctx, child)
	}
}

// buildLogicalOperator 构建逻辑操作符（AND/OR）
func (my *Dialect) buildLogicalOperator(ctx *compiler.Context, child *ast.ChildValue, operator string) error {
	if child.Value == nil || len(child.Value.Children) == 0 {
		return fmt.Errorf("logical operator %s requires at least one condition", operator)
	}

	ctx.Write("(")
	for i, subChild := range child.Value.Children {
		if i > 0 {
			ctx.Space(operator)
		}
		if err := my.buildWhereValue(ctx, subChild.Value); err != nil {
			return err
		}
	}
	ctx.Write(")")

	return nil
}

// buildNotOperator 构建NOT操作符
func (my *Dialect) buildNotOperator(ctx *compiler.Context, child *ast.ChildValue) error {
	if child.Value == nil {
		return fmt.Errorf("NOT operator requires a condition")
	}

	ctx.Write("NOT (")
	err := my.buildWhereValue(ctx, child.Value)
	ctx.Write(")")

	return err
}

// buildFieldCondition 构建字段条件
func (my *Dialect) buildFieldCondition(ctx *compiler.Context, child *ast.ChildValue) error {
	fieldName := child.Name
	if child.Value == nil || len(child.Value.Children) == 0 {
		return fmt.Errorf("field condition %s requires operator and value", fieldName)
	}

	// 获取字段信息并构建字段引用
	if err := my.buildFieldReference(ctx, fieldName, child.Value); err != nil {
		return err
	}

	// 处理字段的操作符条件
	for _, opChild := range child.Value.Children {
		if err := my.buildOperatorCondition(ctx, opChild); err != nil {
			return err
		}
	}

	return nil
}

// buildFieldReference 构建字段引用
func (my *Dialect) buildFieldReference(ctx *compiler.Context, fieldName string, value *ast.Value) error {
	// 尝试从Definition获取表和列信息
	if value.Definition != nil {
		typeName := strings.TrimSuffix(value.Definition.Name, gql.SUFFIX_WHERE_INPUT)
		if table, ok := ctx.TableName(typeName); ok {
			if field, ok := ctx.FindField(typeName, fieldName); ok {
				ctx.Quote(table).Write(".").Quote(field.Column)
				return nil
			}
		}
	}

	// 回退到直接使用字段名
	ctx.Quote(fieldName)
	return nil
}

// buildOperatorCondition 构建操作符条件
func (my *Dialect) buildOperatorCondition(ctx *compiler.Context, opChild *ast.ChildValue) error {
	operator := opChild.Name
	if operator == "" {
		return fmt.Errorf("empty operator name")
	}

	// 获取操作符的SQL表示
	sqlOp, err := my.getSQLOperator(operator)
	if err != nil {
		return err
	}

	ctx.Space(sqlOp)

	// 处理操作符的值
	return my.buildOperatorValue(ctx, operator, opChild.Value)
}

// getSQLOperator 获取操作符的SQL表示
func (my *Dialect) getSQLOperator(operator string) (string, error) {
	// 从全局字典获取操作符信息
	if op, ok := gql.GetOperator(operator); ok {
		return strings.ToUpper(op.Value), nil
	}

	// 处理特殊操作符
	switch operator {
	case gql.IS:
		return "IS", nil
	case gql.IN:
		return "IN", nil
	case gql.EQ:
		return "=", nil
	case gql.NE:
		return "!=", nil
	case gql.GT:
		return ">", nil
	case gql.GE:
		return ">=", nil
	case gql.LT:
		return "<", nil
	case gql.LE:
		return "<=", nil
	case gql.LIKE:
		return "LIKE", nil
	case gql.I_LIKE:
		return "ILIKE", nil
	case gql.REGEX:
		return "~", nil
	case gql.I_REGEX:
		return "~*", nil
	default:
		return "", fmt.Errorf("unsupported operator: %s", operator)
	}
}

// buildOperatorValue 构建操作符的值
func (my *Dialect) buildOperatorValue(ctx *compiler.Context, operator string, value *ast.Value) error {
	if value == nil {
		return fmt.Errorf("operator %s requires a value", operator)
	}

	switch operator {
	case gql.IN:
		return my.buildInValue(ctx, value)
	case gql.IS:
		return my.buildIsValue(ctx, value)
	default:
		return my.buildParam(ctx, value)
	}
}

// buildInValue 构建IN操作符的值
func (my *Dialect) buildInValue(ctx *compiler.Context, value *ast.Value) error {
	if value.Kind == ast.ListValue {
		ctx.Write("(")
		for i, child := range value.Children {
			if i > 0 {
				ctx.Write(", ")
			}
			if err := my.buildParam(ctx, child.Value); err != nil {
				return err
			}
		}
		ctx.Write(")")
		return nil
	}

	// 单个值的情况
	ctx.Write("(")
	err := my.buildParam(ctx, value)
	ctx.Write(")")
	return err
}

// buildIsValue 构建IS操作符的值（NULL检查）
func (my *Dialect) buildIsValue(ctx *compiler.Context, value *ast.Value) error {
	val, err := value.Value(nil)
	if err != nil {
		return err
	}

	if boolVal, ok := val.(bool); ok {
		if boolVal {
			ctx.Write("NULL")
		} else {
			ctx.Write("NOT NULL")
		}
		return nil
	}

	return fmt.Errorf("IS operator requires a boolean value, got %T", val)
}

// buildParam 构建参数值
func (my *Dialect) buildParam(ctx *compiler.Context, value *ast.Value) error {
	val, err := value.Value(nil)
	if err != nil {
		return fmt.Errorf("failed to get parameter value: %w", err)
	}

	placeholder := my.Placeholder(len(ctx.Args()) + 1)
	ctx.Write(placeholder)
	ctx.AddParam(val)

	return nil
}
