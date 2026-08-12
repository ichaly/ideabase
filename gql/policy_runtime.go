package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/log"
	"github.com/vektah/gqlparser/v2/ast"
)

type policyDecisionsKey struct{}

// PolicyDecisionFromContext exposes the normalized decision to a governed
// Resolver without making the Resolver call the provider a second time.
func PolicyDecisionFromContext(ctx context.Context, coordinate string) (PolicyDecision, bool) {
	decisions, _ := ctx.Value(policyDecisionsKey{}).(map[string]PolicyDecision)
	decision, ok := decisions[coordinate]
	if !ok {
		return PolicyDecision{}, false
	}
	decision, err := clonePolicyDecision(decision)
	return decision, err == nil
}

func (my *Engine) authorizeProvider(ctx context.Context, operation *ast.OperationDefinition, variables map[string]interface{}) (context.Context, error) {
	if my.policy == nil || operation == nil {
		return ctx, nil
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return ctx, publicPolicyFailure("IDENTITY_REQUIRED", "verified identity is required for policy")
	}
	route, ok := PinnedRoute(ctx)
	if !ok {
		route = TenantRoute{Tenant: identity.Tenant()}
	}
	targets, err := my.policyTargets(operation, variables)
	if err != nil {
		return ctx, err
	}
	decisions := make(map[string]PolicyDecision, len(targets))
	for _, target := range targets {
		capability, coordinate := target.capability, target.capability.Coordinate
		request := PolicyRequest{
			Identity: identity, Route: route, Coordinate: coordinate,
			Resource: capability.Resource, Actions: capability.Actions,
			FieldUses: target.uses(), Fields: target.fields,
			Values: scopeValues(ctx),
		}
		decision, err := decidePolicy(ctx, my.policy, request)
		if err != nil {
			return ctx, fmt.Errorf("policy %s: %w", coordinate, err)
		}
		if revision := identity.PolicyRevision(); revision != 0 && decision.PolicyRevision != revision {
			return ctx, fmt.Errorf("policy %s: %w (identity=%d decision=%d)", coordinate, publicPolicyFailure("POLICY_REVISION_MISMATCH", "policy revision mismatch"), revision, decision.PolicyRevision)
		}
		if !decision.Allow {
			return ctx, fmt.Errorf("%w: %s", publicPolicyFailure("POLICY_DENIED", "policy denied"), coordinate)
		}
		if err := my.validatePolicyBudget(target, decision, operation, variables); err != nil {
			return ctx, fmt.Errorf("%w: %s: %v", publicPolicyFailure(policyBudgetCode(err), "policy budget exceeded"), coordinate, err)
		}
		if capability.Handler != HandlerSQL && hasRowPolicy(decision) && !capability.PolicyAware {
			return ctx, fmt.Errorf("%w: %s", publicPolicyFailure("POLICY_HANDLER_REQUIRED", "policy requires a policy-aware handler"), coordinate)
		}
		if target.class != nil {
			if err := ValidateDecisionCatalog(target.class, decision); err != nil {
				return ctx, fmt.Errorf("%w: %s: %v", publicPolicyFailure("POLICY_CATALOG_INVALID", "policy catalog is invalid"), coordinate, err)
			}
			for _, use := range request.FieldUses {
				for _, field := range request.Fields[use] {
					if !policyFieldAllowed(target.class, decision.Fields[use], field) {
						return ctx, fmt.Errorf("%w: %s field-use=%s field=%s", publicPolicyFailure("POLICY_FIELD_DENIED", "policy denied field"), coordinate, use, field)
					}
				}
			}
			if err := evaluatePolicyChecks(target, decision, variables, scopeValues(ctx)); err != nil {
				return ctx, fmt.Errorf("%w: %s: %v", publicPolicyFailure("POLICY_CHECK_FAILED", "policy check failed"), coordinate, err)
			}
		}
		decisions[coordinate] = decision
	}
	return context.WithValue(ctx, policyDecisionsKey{}, decisions), nil
}

func decidePolicy(ctx context.Context, provider PolicyProvider, request PolicyRequest) (PolicyDecision, error) {
	decision, err := provider.Decide(ctx, request)
	if err == nil {
		decision, err = NormalizeDecision(request, decision)
	}
	if err != nil {
		log.Error().Err(err).Str("coordinate", request.Coordinate).Msg("policy evaluation failed")
		return PolicyDecision{}, publicPolicyFailure("POLICY_ERROR", "policy evaluation failed")
	}
	return decision, nil
}

func publicPolicyFailure(code, message string) error {
	return &publicFailure{message: message, code: code}
}

func policyBudgetCode(err error) string {
	if strings.HasPrefix(err.Error(), "cost ") {
		return "QUERY_COST_EXCEEDED"
	}
	return "QUERY_LIMIT_EXCEEDED"
}

func (my *Engine) validatePolicyBudget(target policyTarget, decision PolicyDecision, operation *ast.OperationDefinition, variables map[string]interface{}) error {
	if decision.Cost > 0 {
		cost := operationCost(operation, variables, my.metadata.DefaultLimit(), my.snapshot.index)
		if cost > uint64(decision.Cost) {
			return fmt.Errorf("cost %d exceeds %d", cost, decision.Cost)
		}
	}
	if decision.Limit <= 0 || !operationInList(target.capability.Actions, protocol.OP_SELECT) && !operationInList(target.capability.Actions, protocol.OP_AGGREGATE) {
		return nil
	}
	for _, node := range target.nodes {
		limit := my.metadata.DefaultLimit()
		if limit <= 0 {
			// Connection lists receive the compiler's bounded fallback even when
			// schema.default-limit is unset; authorize that effective value too.
			limit = protocol.DEFAULT_PAGE_SIZE
		}
		for _, name := range []string{protocol.LIMIT, protocol.FIRST, protocol.LAST} {
			if argument := node.Arguments.ForName(name); argument != nil {
				value, err := argument.Value.Value(variables)
				if err != nil {
					return err
				}
				if requested, ok := policyInt(value); ok {
					limit = requested
				}
			}
		}
		if limit > decision.Limit {
			return fmt.Errorf("limit %d exceeds %d", limit, decision.Limit)
		}
	}
	return nil
}

func policyInt(value any) (int, bool) {
	switch value := value.(type) {
	case int:
		return value, true
	case int32:
		return int(value), true
	case int64:
		return int(value), true
	case float64:
		return int(value), true
	default:
		return 0, false
	}
}

func hasRowPolicy(decision PolicyDecision) bool {
	if len(decision.RowPredicates)+len(decision.Preset)+len(decision.Check) > 0 {
		return true
	}
	for _, branch := range decision.RoleBranches {
		if len(branch.RowPredicates)+len(branch.Check) > 0 {
			return true
		}
	}
	return false
}

type policyTarget struct {
	capability Capability
	class      *protocol.Class
	fields     map[FieldUse][]string
	nodes      []*ast.Field
	rows       []map[string]interface{}
}

// policyOverlay is a request-local catalog view. Classes and fields are
// copied only when policy compilation needs to change them; all structural
// metadata remains shared with the frozen catalog and is treated as read-only.
type policyOverlay struct {
	metadata    *Metadata
	classes     map[*protocol.Class]*protocol.Class
	sources     map[*protocol.Class]*protocol.Class
	aliases     map[*protocol.Class][]string
	fieldCopies map[*protocol.Class]map[*protocol.Field]*protocol.Field
}

func newPolicyOverlay(base *Metadata) *policyOverlay {
	overlay := *base
	overlay.Nodes = make(map[string]*protocol.Class, len(base.Nodes))
	o := &policyOverlay{
		metadata:    &overlay,
		classes:     make(map[*protocol.Class]*protocol.Class),
		sources:     make(map[*protocol.Class]*protocol.Class),
		aliases:     make(map[*protocol.Class][]string),
		fieldCopies: make(map[*protocol.Class]map[*protocol.Field]*protocol.Field),
	}
	for key, class := range base.Nodes {
		if class == nil {
			continue
		}
		overlay.Nodes[key] = class
		o.aliases[class] = append(o.aliases[class], key)
	}
	// A policy overlay is authoritative for permissions. Clear metadata
	// permissions even for classes not selected by this operation so a reused
	// overlay can never append the same row predicate twice.
	for class := range o.aliases {
		if class.Permissions != nil {
			o.copyClass(class)
		}
	}
	return o
}

func (o *policyOverlay) copyClass(source *protocol.Class) *protocol.Class {
	if source == nil {
		return nil
	}
	if copy := o.classes[source]; copy != nil {
		return copy
	}
	copy := *source
	copy.Permissions = nil
	copy.Fields = source.Fields
	o.classes[source] = &copy
	o.sources[&copy] = source
	for _, key := range o.aliases[source] {
		o.metadata.Nodes[key] = &copy
	}
	return &copy
}

func (o *policyOverlay) class(source *protocol.Class) *protocol.Class {
	if original := o.sources[source]; original != nil {
		source = original
	}
	return o.copyClass(source)
}

func (o *policyOverlay) mutableField(class *protocol.Class, name string) *protocol.Field {
	source := o.sources[class]
	if source == nil {
		source = class
	}
	class = o.copyClass(source)
	if class == nil {
		return nil
	}
	copies := o.fieldCopies[source]
	if copies == nil {
		fields := make(map[string]*protocol.Field, len(class.Fields))
		for key, field := range class.Fields {
			fields[key] = field
		}
		class.Fields = fields
		copies = make(map[*protocol.Field]*protocol.Field)
		o.fieldCopies[source] = copies
	}
	field := class.Fields[name]
	if field == nil {
		return nil
	}
	if copy := copies[field]; copy != nil {
		return copy
	}
	copy := *field
	copies[field] = &copy
	for key, candidate := range class.Fields {
		if candidate == field {
			class.Fields[key] = &copy
		}
	}
	return &copy
}

func (t *policyTarget) add(use FieldUse, name string) {
	if name != "" {
		t.fields[use] = append(t.fields[use], name)
	}
}

func (t policyTarget) uses() []FieldUse {
	uses := make([]FieldUse, 0, len(t.fields))
	for use := range t.fields {
		t.fields[use] = sortedUnique(t.fields[use])
		uses = append(uses, use)
	}
	sort.Slice(uses, func(i, j int) bool { return uses[i] < uses[j] })
	return uses
}

func (my *Engine) policyTargets(operation *ast.OperationDefinition, variables map[string]interface{}) ([]policyTarget, error) {
	var targets []policyTarget
	indexes := make(map[string]int)
	targetFor := func(capability Capability, class *protocol.Class) *policyTarget {
		if index, exists := indexes[capability.Coordinate]; exists {
			return &targets[index]
		}
		indexes[capability.Coordinate] = len(targets)
		targets = append(targets, policyTarget{capability: capability, class: class, fields: make(map[FieldUse][]string)})
		return &targets[len(targets)-1]
	}
	var collect func(*ast.Field, *protocol.Class) error
	collect = func(root *ast.Field, class *protocol.Class) error {
		parent := "Query"
		if root.ObjectDefinition != nil {
			parent = root.ObjectDefinition.Name
		} else if operation.Operation == ast.Mutation {
			parent = "Mutation"
		} else if operation.Operation == ast.Subscription {
			parent = "Subscription"
		}
		coordinate := parent + "." + root.Name
		capability, ok := my.snapshot.Capability(coordinate)
		if !ok || capability.Resource == "" || len(capability.Actions) == 0 {
			return fmt.Errorf("policy capability is missing: %s", coordinate)
		}
		if class == nil {
			class, _ = my.metadata.GetNode(capability.Resource)
		}
		target := targetFor(capability, class)
		target.nodes = append(target.nodes, root)
		if class != nil {
			my.collectPolicyFields(target, class, root, variables)
			if operation.Operation == ast.Mutation {
				if err := my.collectRelationPolicyTargets(targetFor, class, root, variables); err != nil {
					return err
				}
			}
			if err := my.collectPolicyChildren(class, root.SelectionSet, collect); err != nil {
				return err
			}
		}
		return nil
	}
	for _, selection := range operation.SelectionSet {
		field, ok := selection.(*ast.Field)
		if ok && field.Name != "__typename" {
			if err := collect(field, nil); err != nil {
				return nil, err
			}
		}
	}
	return targets, nil
}

func (my *Engine) collectRelationPolicyTargets(targetFor func(Capability, *protocol.Class) *policyTarget, class *protocol.Class, root *ast.Field, variables map[string]interface{}) error {
	argument := root.Arguments.ForName(protocol.INPUT)
	if argument == nil {
		return nil
	}
	value, err := argument.Value.Value(variables)
	if err != nil {
		return err
	}
	rows, err := policyRows(value)
	if err != nil {
		return err
	}
	for _, row := range rows {
		for name, raw := range row {
			field := class.Fields[name]
			operations, ok := raw.(map[string]interface{})
			if !ok || field == nil || field.Column != "" || field.Relation == nil || !field.IsList {
				continue
			}
			targetClass, ok := my.metadata.GetNode(field.Relation.TargetClass)
			if !ok {
				return fmt.Errorf("relation target class is missing: %s", field.Relation.TargetClass)
			}
			if operations[protocol.CONNECT] != nil || operations[protocol.DISCONNECT] != nil {
				capability, ok := my.snapshot.Capability("Mutation." + protocol.UPDATE + targetClass.Name)
				if !ok {
					return fmt.Errorf("policy capability is missing for nested relation update: %s", targetClass.Name)
				}
				target := targetFor(capability, targetClass)
				if field.Relation.Through != nil {
					target.add(FieldUseWrite, targetClass.PrimaryKeys[0])
				} else {
					target.add(FieldUseWrite, field.Relation.TargetField)
				}
			}
			if rawCreate := operations[protocol.CREATE]; rawCreate != nil {
				capability, ok := my.snapshot.Capability("Mutation." + protocol.CREATE + targetClass.Name)
				if !ok {
					return fmt.Errorf("policy capability is missing for nested relation create: %s", targetClass.Name)
				}
				created, err := policyRows(rawCreate)
				if err != nil {
					return err
				}
				target := targetFor(capability, targetClass)
				target.rows = append(target.rows, created...)
				for _, createdRow := range created {
					for fieldName := range createdRow {
						if targetClass.Fields[fieldName] != nil {
							target.add(FieldUseWrite, fieldName)
						}
					}
				}
			}
		}
	}
	return nil
}

func evaluatePolicyChecks(target policyTarget, decision PolicyDecision, variables map[string]interface{}, scope map[string]any) error {
	if len(decision.Check) == 0 && len(decision.Preset) == 0 {
		empty := true
		for _, branch := range decision.RoleBranches {
			empty = empty && len(branch.Check) == 0
		}
		if empty {
			return nil
		}
	}
	rows := append([]map[string]interface{}(nil), target.rows...)
	for _, node := range target.nodes {
		argument := node.Arguments.ForName(protocol.INPUT)
		if argument == nil {
			if (operationInList(target.capability.Actions, protocol.OP_INSERT) || operationInList(target.capability.Actions, protocol.OP_UPDATE)) && (len(decision.Preset) > 0 || len(decision.Check) > 0) {
				return fmt.Errorf("write check/preset requires input")
			}
			continue
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			return err
		}
		resolved, err := policyRows(value)
		if err != nil {
			return err
		}
		rows = append(rows, resolved...)
	}
	for _, row := range rows {
		values := make(map[string]any, len(row)+len(decision.Preset))
		for name, value := range row {
			field := target.class.Fields[name]
			if field != nil && field.Column != "" {
				values[field.Column] = value
			}
		}
		for _, preset := range decision.Preset {
			field := target.class.Fields[preset.Column]
			if field == nil {
				continue
			}
			if _, submitted := values[field.Column]; submitted {
				return fmt.Errorf("client submitted preset field %s", field.Name)
			}
			value := preset.Value
			if preset.Context != "" {
				var ok bool
				value, ok = scope[preset.Context]
				if !ok {
					return fmt.Errorf("missing preset context %s", preset.Context)
				}
			}
			values[field.Column] = value
		}
		// Update checks are enforced by mutation SQL against the post-update
		// row. The request only contains submitted columns, so untouched columns
		// must be resolved by the database rather than rejected here.
		if !operationInList(target.capability.Actions, protocol.OP_UPDATE) || operationInList(target.capability.Actions, protocol.OP_INSERT) {
			if err := matchPolicyPredicates(target.class, decision.Check, values, scope); err != nil {
				return err
			}
		}
		if len(decision.RoleBranches) > 0 && (!operationInList(target.capability.Actions, protocol.OP_UPDATE) || operationInList(target.capability.Actions, protocol.OP_INSERT)) {
			matched := false
			for _, branch := range decision.RoleBranches {
				if matchPolicyPredicates(target.class, branch.Check, values, scope) == nil {
					matched = true
					break
				}
			}
			if !matched {
				return fmt.Errorf("row check failed")
			}
		}
	}
	return nil
}

func matchPolicyPredicates(class *protocol.Class, predicates []TypedPredicate, values, scope map[string]any) error {
	rules := make([]protocol.Predicate, 0, len(predicates))
	for _, predicate := range predicates {
		field := class.Fields[predicate.Column]
		if field == nil || field.Column == "" {
			return fmt.Errorf("unknown check field %s", predicate.Column)
		}
		operator := predicate.Operator
		if operator == "neq" {
			operator = "ne"
		}
		rules = append(rules, protocol.Predicate{Column: field.Column, Operator: operator, Context: predicate.Context, Value: predicate.Value})
	}
	return evaluatePredicates(class, "", nil, rules, values, scope)
}

func (my *Engine) collectPolicyChildren(class *protocol.Class, set ast.SelectionSet, collect func(*ast.Field, *protocol.Class) error) error {
	for _, selection := range set {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		declared := class.Fields[field.Name]
		if declared == nil {
			if err := my.collectPolicyChildren(class, field.SelectionSet, collect); err != nil {
				return err
			}
			continue
		}
		if declared.Relation == nil && declared.Resolver == "" && declared.Remote == nil {
			continue
		}
		var target *protocol.Class
		if declared.Relation != nil {
			target, _ = my.metadata.GetNode(declared.Relation.TargetClass)
		}
		if err := collect(field, target); err != nil {
			return err
		}
	}
	return nil
}

func (my *Engine) collectPolicyFields(target *policyTarget, class *protocol.Class, root *ast.Field, variables map[string]interface{}) {
	for _, argument := range root.Arguments {
		use := FieldUse("")
		switch argument.Name {
		case protocol.WHERE, protocol.KEY, protocol.SEARCH, protocol.DELETION:
			use = FieldUseFilter
		case protocol.SORT:
			use = FieldUseOrder
		case protocol.DISTINCT, protocol.GROUP_BY, protocol.HAVING:
			use = FieldUseGroup
		case protocol.INPUT:
			use = FieldUseWrite
		}
		if use == "" {
			continue
		}
		if argument.Name == protocol.SEARCH {
			for _, name := range class.Search {
				target.add(use, name)
			}
		}
		value, err := argument.Value.Value(variables)
		if err != nil {
			continue
		}
		if argument.Name == protocol.DELETION {
			if value != "ACTIVE" && class.SoftDelete != nil {
				target.add(use, class.SoftDelete.Column)
			}
			continue
		}
		if argument.Name == protocol.INPUT {
			rows, err := policyRows(value)
			if err != nil {
				continue
			}
			for _, row := range rows {
				for name := range row {
					field := class.Fields[name]
					if field == nil {
						continue
					}
					if field.Relation != nil {
						target.add(FieldUseJoin, name)
					} else {
						target.add(use, name)
					}
				}
			}
			continue
		}
		walkResolvedAny(value, func(name string) {
			if class.Fields[name] != nil {
				target.add(use, name)
			}
		})
	}
	var walk func(ast.SelectionSet)
	walk = func(set ast.SelectionSet) {
		for _, selection := range set {
			field, ok := selection.(*ast.Field)
			if !ok || field.Name == "__typename" {
				continue
			}
			declared := class.Fields[field.Name]
			if declared == nil {
				walk(field.SelectionSet)
				continue
			}
			if declared.Relation != nil || declared.Resolver != "" || declared.Remote != nil {
				target.add(FieldUseJoin, field.Name)
				if declared.Relation != nil {
					for _, name := range declared.Relation.SourceColumns() {
						target.add(FieldUseInternalDependency, name)
					}
				} else if declared.Resolver != "" {
					for _, name := range declared.Requires {
						target.add(FieldUseInternalDependency, name)
					}
				} else if declared.Remote != nil {
					target.add(FieldUseInternalDependency, declared.Remote.Key)
				}
				continue
			}
			target.add(FieldUseReturn, field.Name)
			if operationInList(target.capability.Actions, protocol.OP_SELECT) {
				target.add(FieldUseSelect, field.Name)
			}
		}
	}
	walk(root.SelectionSet)
	if root.ObjectDefinition != nil {
		if source, ok := my.metadata.GetNode(root.ObjectDefinition.Name); ok {
			if relation := source.Fields[root.Name]; relation != nil && relation.Relation != nil {
				for _, name := range relation.Relation.TargetColumns() {
					target.add(FieldUseInternalDependency, name)
				}
			}
		}
	}
}

func policyFieldAllowed(class *protocol.Class, allowed []string, requested string) bool {
	field := class.Fields[requested]
	return field != nil && allowedColumn(allowed, field)
}

func (my *Engine) policyPlan(ctx context.Context, base *Plan, variables map[string]interface{}) (*Plan, error) {
	decisions, _ := ctx.Value(policyDecisionsKey{}).(map[string]PolicyDecision)
	if len(decisions) == 0 || base == nil || base.operation == nil {
		return base, nil
	}
	routeKey := ""
	if route, ok := PinnedRoute(ctx); ok && route.Mode != TenantModeSharedTable {
		routeKey = route.CacheKey
	}
	key := planKey{query: fmt.Sprintf("%p", base.operation), shape: routeKey + "\x00" + policyPlanSignature(decisions)}
	if entry, ok := my.policyCache.Get(key); ok && entry.plan != nil {
		plan := *entry.plan
		plan.policyScope = my.policyDecisionScope(decisions)
		return &plan, nil
	}
	overlay := newPolicyOverlay(my.metadata)

	coordinates := make([]string, 0, len(decisions))
	for coordinate := range decisions {
		coordinates = append(coordinates, coordinate)
	}
	sort.Strings(coordinates)
	roles := make([]string, 0, len(coordinates))
	scope := my.policyDecisionScope(decisions)
	for coordinateIndex, coordinate := range coordinates {
		decision := decisions[coordinate]
		capability, _ := my.snapshot.Capability(coordinate)
		baseClass, ok := my.metadata.GetNode(capability.Resource)
		if !ok {
			continue // custom Resolver/Remote policies have no SQL catalog overlay
		}
		class := overlay.class(baseClass)
		if class.Permissions == nil {
			class.Permissions = make(map[string]*protocol.Permission)
		}
		branches := decision.RoleBranches
		if len(branches) == 0 {
			branches = []RolePolicyBranch{{Role: "__policy"}}
		}
		for branchIndex, branch := range branches {
			role := branch.Role
			if role == "" {
				return nil, fmt.Errorf("policy %s has empty role branch", coordinate)
			}
			permission := class.Permissions[role]
			if permission == nil {
				permission = &protocol.Permission{}
				class.Permissions[role] = permission
			}
			applyDecisionFields(permission, capability.Actions, decision.Fields)
			var err error
			filters := policyFilterPredicates(capability.Actions, decision, branch)
			permission.Filter, err = appendPolicyPredicates(permission.Filter, class, filters, scope, fmt.Sprintf("%d_%d_f", coordinateIndex, branchIndex))
			if err != nil {
				return nil, fmt.Errorf("policy %s: %w", coordinate, err)
			}
			permission.Check, err = appendPolicyPredicates(permission.Check, class, append(append([]TypedPredicate(nil), decision.Check...), branch.Check...), scope, fmt.Sprintf("%d_%d_c", coordinateIndex, branchIndex))
			if err != nil {
				return nil, fmt.Errorf("policy %s: %w", coordinate, err)
			}
			roles = append(roles, role)
		}
		if err := applyPolicyPresets(overlay, class, capability.Actions, decision.Preset, scope, fmt.Sprintf("%d_p", coordinateIndex)); err != nil {
			return nil, fmt.Errorf("policy %s: %w", coordinate, err)
		}
	}
	overlay.metadata.operations = buildOperationCatalog(overlay.metadata)
	compiler := &Compiler{meta: overlay.metadata, dialect: my.compiler.dialect, cursorFingerprint: my.compiler.cursorFingerprint}
	plan, err := compiler.Compile(base.operation, variables)
	if err != nil {
		return nil, err
	}
	plan.resolvers, plan.paths, plan.intro = base.resolvers, base.paths, base.intro
	plan.policyRoles = sortedUnique(roles)
	plan.policyScope = scope
	if !plan.Volatile() {
		template := *plan
		template.policyScope = nil
		my.policyCache.Put(key, &planEntry{plan: &template})
	}
	return plan, nil
}

func applyDecisionFields(permission *protocol.Permission, actions []string, fields map[FieldUse][]string) {
	read := append([]string(nil), fields[FieldUseSelect]...)
	for _, use := range []FieldUse{FieldUseFilter, FieldUseOrder, FieldUseGroup, FieldUseAggregate, FieldUseJoin, FieldUseReturn, FieldUseInternalDependency} {
		read = append(read, fields[use]...)
	}
	write := fields[FieldUseWrite]
	for _, action := range actions {
		switch action {
		case protocol.OP_SELECT:
			permission.Select = append(permission.Select, read...)
			if len(read) == 0 {
				permission.Select = append(permission.Select, "*")
			}
		case protocol.OP_AGGREGATE:
			permission.Select = append(permission.Select, read...)
			permission.Aggregate = true
		case protocol.OP_INSERT:
			permission.Insert = append(permission.Insert, write...)
		case protocol.OP_UPDATE:
			permission.Update = append(permission.Update, write...)
		case protocol.OP_DELETE:
			permission.Delete = true
		case protocol.OP_RESTORE:
			permission.Restore = true
		case protocol.OP_PURGE:
			permission.Purge = true
		}
	}
	permission.Select = sortedUnique(permission.Select)
	permission.Insert = sortedUnique(permission.Insert)
	permission.Update = sortedUnique(permission.Update)
}

func appendPolicyPredicates(out []protocol.Predicate, class *protocol.Class, predicates []TypedPredicate, scope map[string]any, prefix string) ([]protocol.Predicate, error) {
	for index, predicate := range predicates {
		field := class.Fields[predicate.Column]
		if field == nil || field.Column == "" {
			return nil, fmt.Errorf("unknown predicate field %s.%s", class.Name, predicate.Column)
		}
		operator := predicate.Operator
		if operator == "neq" {
			operator = "ne"
		}
		item := protocol.Predicate{Column: field.Column, Operator: operator, Context: predicate.Context}
		if item.Context == "" && operator != "is-null" && operator != "not-null" {
			item.Context = fmt.Sprintf("__gql_policy_%s_%d", prefix, index)
			scope[item.Context] = predicate.Value
		}
		out = append(out, item)
	}
	return out, nil
}

func applyPolicyPresets(overlay *policyOverlay, class *protocol.Class, actions []string, presets []TypedPredicate, scope map[string]any, prefix string) error {
	for index, preset := range presets {
		if preset.Operator != "eq" {
			return fmt.Errorf("preset %s only supports eq", preset.Column)
		}
		field := overlay.mutableField(class, preset.Column)
		if field == nil || field.Column == "" {
			return fmt.Errorf("unknown preset field %s.%s", class.Name, preset.Column)
		}
		source := &protocol.ValueSource{Context: preset.Context}
		if source.Context == "" {
			source.Context = fmt.Sprintf("__gql_policy_%s_%d", prefix, index)
			scope[source.Context] = preset.Value
		}
		for _, action := range actions {
			switch action {
			case protocol.OP_INSERT:
				if field.InsertPreset != nil && !reflect.DeepEqual(field.InsertPreset, source) {
					return fmt.Errorf("conflicting insert preset %s.%s", class.Name, field.Name)
				}
				field.InsertPreset = source
			case protocol.OP_UPDATE:
				if field.UpdatePreset != nil && !reflect.DeepEqual(field.UpdatePreset, source) {
					return fmt.Errorf("conflicting update preset %s.%s", class.Name, field.Name)
				}
				field.UpdatePreset = source
			}
		}
	}
	return nil
}

func (my *Engine) policyDecisionScope(decisions map[string]PolicyDecision) map[string]any {
	coordinates := make([]string, 0, len(decisions))
	for coordinate := range decisions {
		coordinates = append(coordinates, coordinate)
	}
	sort.Strings(coordinates)
	scope := make(map[string]any)
	for coordinateIndex, coordinate := range coordinates {
		decision := decisions[coordinate]
		capability, _ := my.snapshot.Capability(coordinate)
		branches := decision.RoleBranches
		if len(branches) == 0 {
			branches = []RolePolicyBranch{{Role: "__policy"}}
		}
		for branchIndex, branch := range branches {
			putPolicyValues(scope, fmt.Sprintf("%d_%d_f", coordinateIndex, branchIndex), policyFilterPredicates(capability.Actions, decision, branch))
			putPolicyValues(scope, fmt.Sprintf("%d_%d_c", coordinateIndex, branchIndex), append(append([]TypedPredicate(nil), decision.Check...), branch.Check...))
		}
		putPolicyValues(scope, fmt.Sprintf("%d_p", coordinateIndex), decision.Preset)
	}
	return scope
}

func policyFilterPredicates(actions []string, decision PolicyDecision, branch RolePolicyBranch) []TypedPredicate {
	filters := append(append([]TypedPredicate(nil), decision.RowPredicates...), branch.RowPredicates...)
	// Check is a post-write invariant for updates, not an old-row filter.
	// Delete/restore still use it as a precondition over the existing row.
	if operationInList(actions, protocol.OP_DELETE) || operationInList(actions, protocol.OP_PURGE) || operationInList(actions, protocol.OP_RESTORE) {
		filters = append(filters, decision.Check...)
		filters = append(filters, branch.Check...)
	}
	return filters
}

func putPolicyValues(scope map[string]any, prefix string, predicates []TypedPredicate) {
	for index, predicate := range predicates {
		if predicate.Context == "" && predicate.Operator != "is-null" && predicate.Operator != "not-null" {
			scope[fmt.Sprintf("__gql_policy_%s_%d", prefix, index)] = predicate.Value
		}
	}
}

func policyPlanSignature(decisions map[string]PolicyDecision) string {
	parts := make([]string, 0, len(decisions))
	for coordinate, decision := range decisions {
		parts = append(parts, fmt.Sprintf("%s:%d:%s:%s", coordinate, decision.PolicyRevision, decision.PermissionSignature, decision.ShapeSignature))
	}
	sort.Strings(parts)
	return strings.Join(parts, "\x00")
}

// policyRuntimeSignature extends the plan shape signature with predicate
// values. Shape caches may reuse SQL, but a subscription lease must expire when
// the values feeding that SQL change.
func policyRuntimeSignature(decisions map[string]PolicyDecision) string {
	parts := make([]string, 0, len(decisions))
	for coordinate, decision := range decisions {
		value, _ := stdjson.Marshal(struct {
			Row      []TypedPredicate
			Preset   []TypedPredicate
			Check    []TypedPredicate
			Branches []RolePolicyBranch
		}{decision.RowPredicates, decision.Preset, decision.Check, decision.RoleBranches})
		parts = append(parts, coordinate+"\x00"+string(value))
	}
	sort.Strings(parts)
	return policyPlanSignature(decisions) + "\x00" + strings.Join(parts, "\x00")
}
