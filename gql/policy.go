package gql

import (
	"context"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2/ast"
)

func operationNeedsRole(meta *Metadata, op *ast.OperationDefinition) bool {
	role, _ := operationGovernanceNeeds(meta, op)
	return role
}

func operationNeedsPolicy(meta *Metadata, op *ast.OperationDefinition) bool {
	_, policy := operationGovernanceNeeds(meta, op)
	return policy
}

func operationGovernanceNeeds(meta *Metadata, op *ast.OperationDefinition) (role, policy bool) {
	if meta == nil || op == nil {
		return false, false
	}
	includeRelations := op.Operation == ast.Mutation
	seen := make(map[*protocol.Class]bool)
	var visit func(ast.SelectionSet)
	visit = func(set ast.SelectionSet) {
		for _, s := range set {
			f, ok := s.(*ast.Field)
			if !ok {
				continue
			}
			c, _ := classForField(meta, f)
			r, p := classTreeGovernance(meta, c, includeRelations, seen)
			role, policy = role || r, policy || p
			visit(f.SelectionSet)
		}
	}
	visit(op.SelectionSet)
	return role, policy
}

func classTreeGovernance(meta *Metadata, class *protocol.Class, includeRelations bool, seen map[*protocol.Class]bool) (role, policy bool) {
	if class == nil || seen[class] {
		return false, false
	}
	seen[class] = true
	role = class.Permissions != nil
	policy = role || len(class.Operations) > 0
	if !includeRelations {
		return role, policy
	}
	for _, field := range class.Fields {
		if field == nil || field.Relation == nil {
			continue
		}
		if target, ok := meta.GetNode(field.Relation.TargetClass); ok {
			r, p := classTreeGovernance(meta, target, true, seen)
			role, policy = role || r, policy || p
		}
	}
	return role, policy
}

func (my *Executor) authorize(ctx context.Context, op *ast.OperationDefinition, vars map[string]interface{}) error {
	if op == nil || my.metadata == nil {
		return nil
	}
	roles := Roles(ctx)
	hasRole := len(roles) > 0
	var first error
	var visit func(ast.SelectionSet, *protocol.Class, string, int)
	visit = func(set ast.SelectionSet, inherited *protocol.Class, inheritedAction string, depth int) {
		for _, s := range set {
			f, ok := s.(*ast.Field)
			if !ok {
				continue
			}
			class, action := classForField(my.metadata, f)
			if class == nil {
				class, action = inherited, inheritedAction
			}
			if first != nil || class == nil {
				continue
			}
			if depth == 0 && op.Operation == ast.Mutation && action == protocol.OP_SELECT {
				first = policyError(class, "mutation", f.Name)
				continue
			}
			if isGeneratedField(class, f.Name) {
				continue
			}
			if class.Permissions == nil {
				if len(class.Operations) > 0 && !operationInList(class.Operations, action) {
					first = policyError(class, action, "operation")
					continue
				}
				if action == protocol.OP_INSERT || action == protocol.OP_UPDATE {
					if err := my.authorizeMutation(class, f, nil, action, vars, scopeWithRole(ctx, scopeValues(ctx), true)); err != nil {
						first = err
						continue
					}
				}
				for _, required := range additionalActions(my.metadata, f) {
					if len(class.Operations) > 0 && !operationInList(class.Operations, required) {
						first = policyError(class, required, "operation")
						break
					}
					if err := my.authorizeMutation(class, f, nil, required, vars, scopeWithRole(ctx, scopeValues(ctx), true)); err != nil {
						first = err
						break
					}
				}
				if first != nil {
					continue
				}
				visit(f.SelectionSet, class, action, depth+1)
				continue
			}
			if !hasRole {
				first = policyError(class, action, "role")
				continue
			}
			perm := mergePermissions(class, roles)
			if perm == nil {
				first = policyError(class, action, "role")
				continue
			}
			if !operationAllowed(class, perm, action) {
				first = policyError(class, action, "operation")
				continue
			}
			if action == protocol.OP_SELECT || action == protocol.OP_AGGREGATE {
				if err := my.authorizeSelection(class, f, perm, action, vars); err != nil {
					first = err
				}
			} else if action == protocol.OP_INSERT || action == protocol.OP_UPDATE {
				if err := my.authorizeMutation(class, f, perm, action, vars, scopeWithRole(ctx, scopeValues(ctx), true)); err != nil {
					first = err
				}
			}
			for _, required := range additionalActions(my.metadata, f) {
				permission, err := permissionForRoles(class, required, roles)
				if err != nil {
					first = err
				} else if err = my.authorizeMutation(class, f, permission, required, vars, scopeWithRole(ctx, scopeValues(ctx), true)); err != nil {
					first = err
				}
				if first != nil {
					break
				}
			}
			visit(f.SelectionSet, class, protocol.OP_SELECT, depth+1)
		}
	}
	visit(op.SelectionSet, nil, actionForOperation(op), 0)
	return first
}

func additionalActions(meta *Metadata, field *ast.Field) []string {
	operation, ok := operationForField(meta, field)
	if !ok || len(operation.actions) < 2 {
		return nil
	}
	return operation.actions[1:]
}

func policyError(class *protocol.Class, action, field string) error {
	return fmt.Errorf("授权拒绝 class=%s action=%s field=%s", class.Name, action, field)
}
func operationAllowed(c *protocol.Class, p *protocol.Permission, action string) bool {
	if len(c.Operations) > 0 {
		allowed := false
		for _, x := range c.Operations {
			if x == action {
				allowed = true
				break
			}
		}
		if !allowed {
			return false
		}
	}
	switch action {
	case protocol.OP_SELECT:
		return len(p.Select) > 0
	case protocol.OP_AGGREGATE:
		return p.Aggregate
	case protocol.OP_INSERT:
		return len(p.Insert) > 0
	case protocol.OP_UPDATE:
		return len(p.Update) > 0
	case protocol.OP_DELETE:
		return p.Delete
	case protocol.OP_RESTORE:
		return p.Restore
	}
	return false
}

func operationInList(list []string, action string) bool {
	for _, x := range list {
		if x == action {
			return true
		}
	}
	return false
}
func allowedColumn(list []string, f *protocol.Field) bool {
	if f == nil {
		return false
	}
	for _, x := range list {
		if x == "*" || x == f.Name || x == f.Column {
			return true
		}
	}
	return false
}

func (my *Executor) authorizeSelection(c *protocol.Class, root *ast.Field, p *protocol.Permission, action string, vars map[string]interface{}) error {
	for _, child := range root.SelectionSet {
		x, ok := child.(*ast.Field)
		if !ok {
			continue
		}
		name := x.Name
		if isGeneratedField(c, name) {
			continue
		}
		field := c.Fields[name]
		if field == nil {
			continue
		}
		if field.Relation != nil {
			continue
		}
		if (action == protocol.OP_SELECT || action == protocol.OP_AGGREGATE) && !allowedColumn(p.Select, field) {
			return policyError(c, action, name)
		}
	}
	return authorizeArgs(c, root, p, action, vars)
}
func authorizeArgs(c *protocol.Class, f *ast.Field, p *protocol.Permission, action string, vars map[string]interface{}) error {
	for _, arg := range f.Arguments {
		queryAction := action == protocol.OP_SELECT || action == protocol.OP_AGGREGATE
		mutationAction := action == protocol.OP_INSERT || action == protocol.OP_UPDATE || action == protocol.OP_DELETE || action == protocol.OP_RESTORE
		if (!queryAction || (arg.Name != protocol.WHERE && arg.Name != protocol.KEY && arg.Name != protocol.SORT && arg.Name != protocol.DISTINCT && arg.Name != protocol.GROUP_BY && arg.Name != protocol.HAVING && arg.Name != protocol.SEARCH)) &&
			(!mutationAction || (arg.Name != protocol.WHERE && arg.Name != protocol.KEY && arg.Name != protocol.ON)) {
			continue
		}
		if arg.Name == protocol.SEARCH {
			for _, name := range c.Search {
				if field := c.Fields[name]; field != nil && !allowedColumn(p.Select, field) {
					return policyError(c, action, name)
				}
			}
			continue
		}
		value, err := arg.Value.Value(vars)
		if err != nil {
			return fmt.Errorf("授权参数解析失败: %w", err)
		}
		bad := ""
		check := func(name string) {
			if bad == "" {
				if field := c.Fields[name]; field != nil && field.Relation == nil && !allowedColumn(p.Select, field) {
					bad = name
				}
			}
		}
		if arg.Name == protocol.KEY {
			for _, name := range c.PrimaryKeys {
				check(name)
			}
		} else if arg.Name == protocol.ON {
			label, ok := value.(string)
			if !ok {
				return policyError(c, action, protocol.ON)
			}
			key, ok := c.ConflictTarget(label)
			if !ok {
				return policyError(c, action, protocol.ON)
			}
			for _, column := range key.Columns {
				check(column)
			}
		} else if arg.Name == protocol.DISTINCT || arg.Name == protocol.GROUP_BY {
			walkResolvedStrings(value, check)
		} else {
			walkResolvedAny(value, check)
		}
		if bad != "" {
			return policyError(c, action, bad)
		}
	}
	return nil
}
func (my *Executor) authorizeMutation(c *protocol.Class, f *ast.Field, p *protocol.Permission, action string, vars, scope map[string]interface{}) error {
	if p != nil {
		if err := authorizeArgs(c, f, p, action, vars); err != nil {
			return err
		}
	}
	arg := f.Arguments.ForName(protocol.INPUT)
	if arg == nil {
		if p == nil {
			return nil
		}
		return evaluateCheck(c, p, action, map[string]any{}, scope)
	}
	value, err := arg.Value.Value(vars)
	if err != nil {
		return fmt.Errorf("授权 input 解析失败: %w", err)
	}
	return my.authorizeInputValue(c, p, action, value, scope)
}

func isGeneratedField(class *protocol.Class, name string) bool {
	if name == "__typename" {
		return true
	}
	if class != nil && class.Fields[name] != nil {
		return false
	}
	switch name {
	case "items", "pageInfo", "total", "affectedCount", "returning", "count", "countDistinct", "key":
		return true
	default:
		return false
	}
}

func (my *Executor) authorizeInputValue(class *protocol.Class, permission *protocol.Permission, action string, value any, scope map[string]interface{}) error {
	rows, err := policyRows(value)
	if err != nil {
		return policyError(class, action, "input")
	}
	var columns []string
	if permission != nil {
		columns = permission.Insert
		if action == protocol.OP_UPDATE {
			columns = permission.Update
		}
	}
	for _, row := range rows {
		candidate := make(map[string]any, len(row)+len(class.Scope))
		keys := make([]string, 0, len(row))
		for name := range row {
			keys = append(keys, name)
		}
		sort.Strings(keys)
		for _, name := range keys {
			field := class.Fields[name]
			if field == nil {
				return policyError(class, action, name)
			}
			if field.Column == "" && field.Relation != nil {
				if err := my.authorizeRelationInput(field, row[name], scope); err != nil {
					return err
				}
				continue
			}
			if permission != nil && !allowedColumn(columns, field) {
				return policyError(class, action, name)
			}
			candidate[field.Column] = row[name]
		}
		if permission != nil {
			var checkErr error
			if roles, ok := scope[protocol.CONTEXT_ROLES].([]string); ok && len(roles) > 0 {
				checkErr = evaluateCheckAny(class, roles, action, candidate, scope)
			} else {
				checkErr = evaluateCheck(class, permission, action, candidate, scope)
			}
			if checkErr != nil {
				return checkErr
			}
		}
	}
	return nil
}

func evaluateCheckAny(class *protocol.Class, roles []string, action string, values map[string]any, scope map[string]interface{}) error {
	var first error
	for _, role := range roles {
		permission := class.Permissions[role]
		if permission == nil || !operationAllowed(class, permission, action) {
			continue
		}
		copyValues := make(map[string]any, len(values))
		for key, value := range values {
			copyValues[key] = value
		}
		if err := evaluateCheck(class, permission, action, copyValues, scope); err == nil {
			return nil
		} else if first == nil {
			first = err
		}
	}
	if first != nil {
		return first
	}
	return policyError(class, action, "role")
}

func (my *Executor) authorizeRelationInput(field *protocol.Field, value any, scope map[string]interface{}) error {
	if my.metadata == nil || field.Relation == nil {
		return nil
	}
	target, ok := my.metadata.GetNode(field.Relation.TargetClass)
	if !ok {
		return policyError(&protocol.Class{Name: field.Relation.TargetClass}, protocol.OP_UPDATE, field.Name)
	}
	object, ok := value.(map[string]interface{})
	if !ok {
		return policyError(target, protocol.OP_UPDATE, field.Name)
	}
	for _, operation := range []string{protocol.CONNECT, protocol.DISCONNECT} {
		if raw, exists := object[operation]; exists && raw != nil {
			permission, err := permissionFor(target, protocol.OP_UPDATE, scope)
			if err != nil {
				return err
			}
			if permission != nil {
				if field.Relation.Through == nil && field.Relation.TargetField != "" {
					targetField := target.Fields[field.Relation.TargetField]
					if targetField == nil || !allowedColumn(permission.Update, targetField) {
						return policyError(target, protocol.OP_UPDATE, field.Relation.TargetField)
					}
				}
				if roles, ok := scope[protocol.CONTEXT_ROLES].([]string); ok && len(roles) > 0 {
					err = evaluateCheckAny(target, roles, protocol.OP_UPDATE, map[string]any{}, scope)
				} else {
					err = evaluateCheck(target, permission, protocol.OP_UPDATE, map[string]any{}, scope)
				}
				if err != nil {
					return err
				}
			}
		}
	}
	if raw, exists := object[protocol.CREATE]; exists && raw != nil {
		permission, err := permissionFor(target, protocol.OP_INSERT, scope)
		if err != nil {
			return err
		}
		if permission != nil {
			return my.authorizeInputValue(target, permission, protocol.OP_INSERT, raw, scope)
		}
	}
	return nil
}

func permissionFor(class *protocol.Class, action string, scope map[string]interface{}) (*protocol.Permission, error) {
	if len(class.Operations) > 0 && !operationInList(class.Operations, action) {
		return nil, policyError(class, action, "operation")
	}
	if class.Permissions == nil {
		return nil, nil
	}
	if roles, ok := scope[protocol.CONTEXT_ROLES].([]string); ok && len(roles) > 0 {
		return permissionForRoles(class, action, roles)
	}
	role, _ := scope[protocol.CONTEXT_ROLE].(string)
	permission := class.Permissions[role]
	if role == "" || permission == nil || !operationAllowed(class, permission, action) {
		return nil, policyError(class, action, "role")
	}
	return permission, nil
}

func permissionForRoles(class *protocol.Class, action string, roles []string) (*protocol.Permission, error) {
	permission := mergePermissions(class, roles)
	if permission == nil || !operationAllowed(class, permission, action) {
		return nil, policyError(class, action, "role")
	}
	return permission, nil
}

func mergePermissions(class *protocol.Class, roles []string) *protocol.Permission {
	if class == nil || class.Permissions == nil {
		return nil
	}
	out := &protocol.Permission{}
	found := false
	for _, role := range roles {
		p := class.Permissions[role]
		if p == nil {
			continue
		}
		found = true
		out.Select = append(out.Select, p.Select...)
		out.Insert = append(out.Insert, p.Insert...)
		out.Update = append(out.Update, p.Update...)
		out.Delete = out.Delete || p.Delete
		out.Restore = out.Restore || p.Restore
		out.Aggregate = out.Aggregate || p.Aggregate
	}
	if !found {
		return nil
	}
	out.Select = uniqueStrings(out.Select)
	out.Insert = uniqueStrings(out.Insert)
	out.Update = uniqueStrings(out.Update)
	return out
}

func uniqueStrings(in []string) []string {
	seen := make(map[string]bool, len(in))
	out := make([]string, 0, len(in))
	for _, item := range in {
		if !seen[item] {
			seen[item] = true
			out = append(out, item)
		}
	}
	return out
}

func policyRows(value any) ([]map[string]interface{}, error) {
	switch typed := value.(type) {
	case map[string]interface{}:
		return []map[string]interface{}{typed}, nil
	case []interface{}:
		rows := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			row, ok := item.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("input row is not an object")
			}
			rows = append(rows, row)
		}
		return rows, nil
	default:
		return nil, fmt.Errorf("input is not an object or list")
	}
}

func evaluateCheck(c *protocol.Class, p *protocol.Permission, action string, values map[string]any, scope map[string]interface{}) error {
	for _, field := range c.Fields {
		if field == nil || field.Column == "" {
			continue
		}
		if _, ok := values[field.Column]; !ok {
			if src := preset(field, action); src != nil {
				if src.Context != "" {
					value, exists := scope[src.Context]
					if !exists {
						return policyError(c, action, field.Column)
					}
					values[field.Column] = value
				} else {
					values[field.Column] = src.Value
				}
			}
		}
	}
	for _, rule := range c.Scope {
		value, exists := scope[rule.Context]
		if !exists {
			return policyError(c, action, rule.Column)
		}
		values[rule.Column] = value
	}
	return evaluatePredicates(c, action, p.Filter, p.Check, values, scope)
}

func evaluatePredicates(c *protocol.Class, action string, filters, predicates []protocol.Predicate, values map[string]any, scope map[string]interface{}) error {
	for _, pred := range predicates {
		v, ok := values[pred.Column]
		if !ok {
			if action == protocol.OP_UPDATE && predicateIn(filters, pred) {
				continue
			}
			return policyError(c, action, pred.Column)
		}
		expected := pred.Value
		if pred.Context != "" {
			value, exists := scope[pred.Context]
			if !exists {
				return fmt.Errorf("missing check context %s", pred.Context)
			}
			expected = value
		}
		if !predicateMatches(pred.Operator, v, expected) {
			return fmt.Errorf("check failed for %s", pred.Column)
		}
	}
	return nil
}

func predicateMatches(operator string, actual, expected any) bool {
	switch operator {
	case "eq":
		return fmt.Sprint(actual) == fmt.Sprint(expected)
	case "ne", "neq":
		return fmt.Sprint(actual) != fmt.Sprint(expected)
	case "is-null":
		return actual == nil
	case "not-null", "is-not-null":
		return actual != nil
	case "in":
		list := reflect.ValueOf(expected)
		if list.IsValid() && (list.Kind() == reflect.Array || list.Kind() == reflect.Slice) {
			for index := 0; index < list.Len(); index++ {
				if fmt.Sprint(actual) == fmt.Sprint(list.Index(index).Interface()) {
					return true
				}
			}
		}
	case "lt", "lte", "gt", "gte":
		order := predicateOrder(actual, expected)
		return operator == "lt" && order < 0 || operator == "lte" && order <= 0 || operator == "gt" && order > 0 || operator == "gte" && order >= 0
	}
	return false
}

func predicateOrder(left, right any) int {
	a, aerr := strconv.ParseFloat(fmt.Sprint(left), 64)
	b, berr := strconv.ParseFloat(fmt.Sprint(right), 64)
	if aerr == nil && berr == nil {
		if a < b {
			return -1
		}
		if a > b {
			return 1
		}
		return 0
	}
	return strings.Compare(fmt.Sprint(left), fmt.Sprint(right))
}
func predicateIn(list []protocol.Predicate, want protocol.Predicate) bool {
	for _, p := range list {
		if p.Column == want.Column && p.Operator == want.Operator && p.Context == want.Context && fmt.Sprint(p.Value) == fmt.Sprint(want.Value) {
			return true
		}
	}
	return false
}
func preset(f *protocol.Field, action string) *protocol.ValueSource {
	if action == protocol.OP_INSERT {
		return f.InsertPreset
	}
	return f.UpdatePreset
}
func walkResolvedAny(value any, fn func(string)) {
	switch x := value.(type) {
	case map[string]interface{}:
		for k, v := range x {
			fn(k)
			walkResolvedAny(v, fn)
		}
	case []interface{}:
		for _, v := range x {
			walkResolvedAny(v, fn)
		}
	}
}

func walkResolvedStrings(value any, fn func(string)) {
	switch typed := value.(type) {
	case string:
		fn(typed)
	case []interface{}:
		for _, item := range typed {
			walkResolvedStrings(item, fn)
		}
	}
}
func classForField(meta *Metadata, f *ast.Field) (*protocol.Class, string) {
	if f == nil || f.Definition == nil || meta == nil {
		return nil, ""
	}
	if operation, ok := operationForField(meta, f); ok {
		return operation.class, operation.actions[0]
	}
	name := f.Definition.Type.Name()
	for _, suffix := range []string{protocol.SUFFIX_RESULT, protocol.SUFFIX_STATS} {
		if strings.HasSuffix(name, suffix) {
			name = strings.TrimSuffix(name, suffix)
			break
		}
	}
	if c, ok := meta.GetNode(name); ok {
		return c, protocol.OP_SELECT
	}
	return nil, ""
}

func operationForField(meta *Metadata, f *ast.Field) (operationCapability, bool) {
	if meta == nil || f == nil {
		return operationCapability{}, false
	}
	if f.ObjectDefinition != nil {
		operation, ok := meta.operations[f.ObjectDefinition.Name+"."+f.Name]
		return operation, ok
	}
	// Tests and programmatic callers may construct a root field without the
	// validator-populated ObjectDefinition. Exact catalog lookup still avoids
	// guessing from prefixes or return types.
	var matched operationCapability
	matches := 0
	for coordinate, operation := range meta.operations {
		if strings.HasSuffix(coordinate, "."+f.Name) {
			if matches == 0 || matched.class != operation.class || matched.actions[0] != operation.actions[0] {
				matched, matches = operation, matches+1
			}
		}
	}
	if matches == 1 {
		return matched, true
	}
	return operationCapability{}, false
}
func actionForOperation(op *ast.OperationDefinition) string {
	if op.Operation == ast.Mutation {
		return protocol.OP_UPDATE
	}
	return protocol.OP_SELECT
}
