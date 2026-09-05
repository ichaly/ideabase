package pgsql

import (
	"fmt"
	"sort"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
)

type expression func() error

// updatePolicyConjunct keeps authorization atomic per role: Filter reads the
// old row and Check reads the value that the same statement will commit.
func (my *Dialect) updatePolicyConjunct(ctx *compiler.Context, class *protocol.Class, row inputRow, overrides map[string]expression) func() error {
	return func() error {
		roles := make([]string, 0, len(class.Permissions))
		for role, permission := range class.Permissions {
			if permission != nil && len(permission.Update) > 0 {
				roles = append(roles, role)
			}
		}
		sort.Strings(roles)
		if len(roles) == 0 {
			ctx.Write(`FALSE`)
			return nil
		}
		ctx.Write(`(`)
		for i, role := range roles {
			if i > 0 {
				ctx.Space(`OR`)
			}
			permission := class.Permissions[role]
			ctx.Write(`(`).Write(my.Placeholder(ctx.AddParam(role))).Write(` = ANY(`).Write(my.Placeholder(ctx.AddContextListSlot(protocol.CONTEXT_ROLES))).Write(`)`)
			for _, predicate := range permission.Filter {
				ctx.Space(`AND`).Column(class.Table, predicate.Column)
				if err := my.writePredicateRHS(ctx, predicate); err != nil {
					return err
				}
			}
			for _, predicate := range permission.Check {
				ctx.Space(`AND`)
				if override := overrides[predicate.Column]; override != nil {
					if err := override(); err != nil {
						return err
					}
				} else if write, ok := row.values[predicate.Column]; ok {
					if err := write(ctx); err != nil {
						return err
					}
				} else {
					ctx.Column(class.Table, predicate.Column)
				}
				if err := my.writePredicateRHS(ctx, predicate); err != nil {
					return err
				}
			}
			ctx.Write(`)`)
		}
		ctx.Write(`)`)
		return nil
	}
}

func (my *Dialect) appendUpdatePolicy(ctx *compiler.Context, class *protocol.Class, overrides map[string]expression) error {
	if class.Permissions == nil {
		return nil
	}
	ctx.Space(`AND`)
	return my.updatePolicyConjunct(ctx, class, inputRow{}, overrides)()
}

// appendInsertPolicy authorizes database-derived rows, such as many-to-many
// links, whose complete values are unavailable to the request pre-check.
func (my *Dialect) appendInsertPolicy(ctx *compiler.Context, qualifier string, class *protocol.Class) error {
	for _, predicate := range my.predicateConjuncts(ctx, qualifier, class.Where, "") {
		ctx.Space(`AND`)
		if err := predicate(); err != nil {
			return err
		}
	}
	if class.Permissions == nil {
		return nil
	}
	roles := make([]string, 0, len(class.Permissions))
	for role, permission := range class.Permissions {
		if permission != nil && len(permission.Insert) > 0 {
			roles = append(roles, role)
		}
	}
	sort.Strings(roles)
	if len(roles) == 0 {
		ctx.Space(`AND FALSE`)
		return nil
	}
	ctx.Space(`AND`).Write(`(`)
	for i, role := range roles {
		if i > 0 {
			ctx.Space(`OR`)
		}
		ctx.Write(`(`).Write(my.Placeholder(ctx.AddParam(role))).Write(` = ANY(`).Write(my.Placeholder(ctx.AddContextListSlot(protocol.CONTEXT_ROLES))).Write(`)`)
		for _, predicate := range class.Permissions[role].Check {
			ctx.Space(`AND`)
			if err := my.writePredicate(ctx, qualifier, predicate); err != nil {
				return err
			}
		}
		ctx.Write(`)`)
	}
	ctx.Write(`)`)
	return nil
}

func (my *Dialect) appendMetadataWhere(ctx *compiler.Context, class *protocol.Class) error {
	for _, predicate := range my.predicateConjuncts(ctx, class.Table, class.Where, "") {
		ctx.Space(`AND`)
		if err := predicate(); err != nil {
			return err
		}
	}
	return nil
}

func (my *Dialect) relationUpdateOverrides(ctx *compiler.Context, class *protocol.Class, fk string, fkValue expression) map[string]expression {
	overrides := map[string]expression{fk: fkValue}
	for _, field := range sortedClassFields(class) {
		if field.Column == "" || field.UpdatePreset == nil || field.Column == fk || auditColumn(field.Column) {
			continue
		}
		preset := field.UpdatePreset
		overrides[field.Column] = func() error { return my.writePreset(ctx, preset) }
	}
	columns := auditColumnsFor(class)
	for _, column := range []string{columns.updatedAt, columns.updatedBy} {
		if column == "" {
			continue
		}
		name := column
		overrides[name] = func() error { return my.auditWriter(name)(ctx) }
	}
	return overrides
}

func (my *Dialect) writePredicateRHS(ctx *compiler.Context, predicate protocol.Predicate) error {
	switch predicate.Operator {
	case "eq", "ne", "lt", "lte", "gt", "gte":
		ctx.Space(map[string]string{"eq": "=", "ne": "<>", "lt": "<", "lte": "<=", "gt": ">", "gte": ">="}[predicate.Operator])
		if predicate.Context != "" {
			ctx.Write(my.Placeholder(ctx.AddContextSlot(predicate.Context)))
		} else {
			ctx.Write(my.Placeholder(ctx.AddParam(normalizeArg(predicate.Value))))
		}
	case "in":
		ctx.Write(` = ANY(`)
		if predicate.Context != "" {
			ctx.Write(my.Placeholder(ctx.AddContextListSlot(predicate.Context)))
		} else {
			ctx.Write(my.Placeholder(ctx.AddParam(compiler.ListArg(predicate.Value))))
		}
		ctx.Write(`)`)
	case "is-null":
		ctx.Write(` IS NULL`)
	case "not-null", "is-not-null":
		ctx.Write(` IS NOT NULL`)
	default:
		return fmt.Errorf("不支持的固定谓词操作符: %s", predicate.Operator)
	}
	return nil
}
