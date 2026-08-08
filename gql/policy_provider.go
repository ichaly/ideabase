package gql

import (
	"context"
	"crypto/sha256"
	"fmt"
	"sort"
	"strings"

	"github.com/ichaly/ideabase/gql/protocol"
)

type FieldUse string

const (
	FieldUseSelect             FieldUse = "select"
	FieldUseFilter             FieldUse = "filter"
	FieldUseOrder              FieldUse = "order"
	FieldUseGroup              FieldUse = "group"
	FieldUseAggregate          FieldUse = "aggregate"
	FieldUseJoin               FieldUse = "join"
	FieldUseWrite              FieldUse = "write"
	FieldUseReturn             FieldUse = "return"
	FieldUseInternalDependency FieldUse = "internal-dependency"
)

type PolicyRequest struct {
	Identity   Identity
	Route      TenantRoute
	Coordinate string
	Resource   string
	Actions    []string
	FieldUses  []FieldUse
	Fields     map[FieldUse][]string
	Values     map[string]any
}

type TypedPredicate struct {
	Column   string
	Operator string
	Context  string
	Value    any
}

type RolePolicyBranch struct {
	Role          string
	RowPredicates []TypedPredicate
	Check         []TypedPredicate
}

type PolicyDecision struct {
	Allow               bool
	Fields              map[FieldUse][]string
	RowPredicates       []TypedPredicate
	RoleBranches        []RolePolicyBranch
	Preset              []TypedPredicate
	Check               []TypedPredicate
	Limit               int
	Cost                int
	PolicyRevision      uint64
	PermissionSignature string
	ShapeSignature      string
}

type PolicyProvider interface {
	Decide(context.Context, PolicyRequest) (PolicyDecision, error)
}

func NormalizeDecision(request PolicyRequest, decision PolicyDecision) (PolicyDecision, error) {
	if err := validatePolicyRequest(request); err != nil {
		return PolicyDecision{}, err
	}
	if decision.Limit < 0 || decision.Cost < 0 {
		return PolicyDecision{}, fmt.Errorf("policy limit and cost must be non-negative")
	}
	decision.Fields = cloneFieldMap(decision.Fields)
	var err error
	decision.RowPredicates, err = clonePredicates(decision.RowPredicates)
	if err != nil {
		return PolicyDecision{}, err
	}
	decision.Preset, err = clonePredicates(decision.Preset)
	if err != nil {
		return PolicyDecision{}, err
	}
	decision.Check, err = clonePredicates(decision.Check)
	if err != nil {
		return PolicyDecision{}, err
	}
	decision.RoleBranches, err = cloneRoleBranches(decision.RoleBranches)
	if err != nil {
		return PolicyDecision{}, err
	}
	for _, p := range append(append(append([]TypedPredicate{}, decision.RowPredicates...), decision.Preset...), decision.Check...) {
		if err := validateTypedPredicate(p); err != nil {
			return PolicyDecision{}, err
		}
	}
	if decision.PolicyRevision == 0 {
		decision.PolicyRevision = request.Identity.PolicyRevision()
	}
	decision.PermissionSignature = permissionSignature(request, decision)
	decision.ShapeSignature = shapeSignature(decision)
	return decision, nil
}

func cloneFieldMap(in map[FieldUse][]string) map[FieldUse][]string {
	if in == nil {
		return nil
	}
	out := make(map[FieldUse][]string, len(in))
	for use, fields := range in {
		out[use] = sortedUnique(fields)
	}
	return out
}

func cloneRoleBranches(in []RolePolicyBranch) ([]RolePolicyBranch, error) {
	out := make([]RolePolicyBranch, len(in))
	for n, branch := range in {
		out[n] = branch
		var err error
		out[n].RowPredicates, err = clonePredicates(branch.RowPredicates)
		if err != nil {
			return nil, err
		}
		out[n].Check, err = clonePredicates(branch.Check)
		if err != nil {
			return nil, err
		}
		for _, predicate := range append(append([]TypedPredicate(nil), out[n].RowPredicates...), out[n].Check...) {
			if err := validateTypedPredicate(predicate); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Role < out[j].Role })
	return out, nil
}

func sortedUnique(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	if len(result) == 0 {
		return result
	}
	n := 1
	for _, value := range result[1:] {
		if value != result[n-1] {
			result[n], n = value, n+1
		}
	}
	return result[:n]
}

func validatePolicyRequest(request PolicyRequest) error {
	if request.Identity.Tenant() == "" || request.Identity.Subject() == "" {
		return fmt.Errorf("policy identity is required")
	}
	if request.Route.Tenant != "" && request.Route.Tenant != request.Identity.Tenant() {
		return fmt.Errorf("policy tenant route does not match identity")
	}
	if request.Coordinate == "" || request.Resource == "" {
		return fmt.Errorf("policy coordinate and resource are required")
	}
	if len(request.Actions) == 0 {
		return fmt.Errorf("policy action is required")
	}
	return nil
}

func validateTypedPredicate(p TypedPredicate) error {
	if p.Column == "" || strings.IndexByte(p.Column, 0) >= 0 {
		return fmt.Errorf("unsafe policy column %q", p.Column)
	}
	switch p.Operator {
	case "eq", "neq", "lt", "lte", "gt", "gte", "in", "is-null", "not-null":
	default:
		return fmt.Errorf("unsupported policy operator %q", p.Operator)
	}
	return nil
}

func clonePredicates(in []TypedPredicate) ([]TypedPredicate, error) {
	out := make([]TypedPredicate, len(in))
	for n, p := range in {
		out[n] = p
		value, err := cloneClaim(p.Value)
		if err != nil {
			return nil, err
		}
		out[n].Value = value
	}
	return out, nil
}

func permissionSignature(r PolicyRequest, d PolicyDecision) string {
	actions := append([]string(nil), r.Actions...)
	sort.Strings(actions)
	uses := make([]string, len(r.FieldUses))
	for n := range r.FieldUses {
		uses[n] = string(r.FieldUses[n])
	}
	sort.Strings(uses)
	fieldShape := make([]string, 0, len(d.Fields))
	for use, fields := range d.Fields {
		fieldShape = append(fieldShape, string(use)+"="+strings.Join(fields, ","))
	}
	sort.Strings(fieldShape)
	return digest(strings.Join([]string{fmt.Sprintf("allow=%t", d.Allow), r.Coordinate, r.Resource, strings.Join(actions, ","), strings.Join(uses, ","), strings.Join(fieldShape, ";")}, "\x00"))
}

func shapeSignature(d PolicyDecision) string {
	parts := []string{fmt.Sprintf("limit=%d", d.Limit), fmt.Sprintf("cost=%d", d.Cost)}
	for _, group := range [][]TypedPredicate{d.RowPredicates, d.Preset, d.Check} {
		shape := make([]string, len(group))
		for n, p := range group {
			shape[n] = p.Column + ":" + p.Operator + ":" + p.Context
		}
		sort.Strings(shape)
		parts = append(parts, strings.Join(shape, ","))
	}
	for _, branch := range d.RoleBranches {
		parts = append(parts, "role="+branch.Role)
		for _, group := range [][]TypedPredicate{branch.RowPredicates, branch.Check} {
			shape := make([]string, len(group))
			for i, predicate := range group {
				shape[i] = predicate.Column + ":" + predicate.Operator + ":" + predicate.Context
			}
			sort.Strings(shape)
			parts = append(parts, strings.Join(shape, ","))
		}
	}
	return digest(strings.Join(parts, "\x00"))
}

func digest(value string) string { return fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(value))) }

type ProtocolPolicyProvider struct {
	permissions map[string]map[string]*protocol.Permission
	err         error
}

func NewProtocolPolicyProvider(permissions map[string]map[string]*protocol.Permission) *ProtocolPolicyProvider {
	out := make(map[string]map[string]*protocol.Permission, len(permissions))
	var invalid error
	for resource, byRole := range permissions {
		out[resource] = make(map[string]*protocol.Permission, len(byRole))
		for role, permission := range byRole {
			if permission != nil {
				copy := *permission
				copy.Select = append([]string(nil), permission.Select...)
				copy.Insert = append([]string(nil), permission.Insert...)
				copy.Update = append([]string(nil), permission.Update...)
				copy.Filter, invalid = cloneProtocolPredicates(permission.Filter)
				if invalid == nil {
					copy.Check, invalid = cloneProtocolPredicates(permission.Check)
				}
				out[resource][role] = &copy
			}
		}
	}
	return &ProtocolPolicyProvider{permissions: out, err: invalid}
}

func cloneProtocolPredicates(in []protocol.Predicate) ([]protocol.Predicate, error) {
	out := make([]protocol.Predicate, len(in))
	for n, predicate := range in {
		out[n] = predicate
		var err error
		out[n].Value, err = cloneClaim(predicate.Value)
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (p *ProtocolPolicyProvider) Decide(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
	if p == nil {
		return PolicyDecision{}, fmt.Errorf("policy provider is nil")
	}
	if p.err != nil {
		return PolicyDecision{}, fmt.Errorf("invalid protocol policy: %w", p.err)
	}
	if err := validatePolicyRequest(request); err != nil {
		return PolicyDecision{}, err
	}
	byRole := p.permissions[request.Resource]
	roles := request.Identity.Roles()
	if len(roles) == 0 {
		d, err := NormalizeDecision(request, PolicyDecision{})
		return d, err
	}
	d := PolicyDecision{Allow: true, PolicyRevision: request.Identity.PolicyRevision(), Fields: make(map[FieldUse][]string)}
	var selectFields []string
	for _, action := range request.Actions {
		allowed := false
		for _, role := range roles {
			permission := byRole[role]
			if permission == nil {
				continue
			}
			switch action {
			case "select":
				allowed = len(permission.Select) > 0
			case "insert":
				allowed = len(permission.Insert) > 0
			case "update":
				allowed = len(permission.Update) > 0
			case "delete":
				allowed = permission.Delete
			case "restore":
				allowed = permission.Restore
			case "aggregate":
				allowed = permission.Aggregate
			default:
				return PolicyDecision{}, fmt.Errorf("unsupported policy action %q", action)
			}
			if allowed {
				break
			}
		}
		if !allowed {
			d, err := NormalizeDecision(request, PolicyDecision{})
			return d, err
		}
	}
	for _, role := range roles {
		permission := byRole[role]
		if permission == nil {
			continue
		}
		selectFields = append(selectFields, permission.Select...)
		branch := RolePolicyBranch{Role: role}
		for _, predicate := range permission.Filter {
			value, _ := cloneClaim(predicate.Value)
			branch.RowPredicates = append(branch.RowPredicates, TypedPredicate{Column: predicate.Column, Operator: predicate.Operator, Context: predicate.Context, Value: value})
		}
		for _, predicate := range permission.Check {
			value, _ := cloneClaim(predicate.Value)
			branch.Check = append(branch.Check, TypedPredicate{Column: predicate.Column, Operator: predicate.Operator, Context: predicate.Context, Value: value})
		}
		if len(branch.RowPredicates) > 0 || len(branch.Check) > 0 {
			d.RoleBranches = append(d.RoleBranches, branch)
		}
	}
	if len(selectFields) > 0 {
		for _, use := range []FieldUse{FieldUseSelect, FieldUseFilter, FieldUseOrder, FieldUseGroup, FieldUseAggregate, FieldUseReturn} {
			d.Fields[use] = append([]string(nil), selectFields...)
		}
	}
	for _, role := range roles {
		if permission := byRole[role]; permission != nil {
			d.Fields[FieldUseWrite] = append(d.Fields[FieldUseWrite], permission.Insert...)
			d.Fields[FieldUseWrite] = append(d.Fields[FieldUseWrite], permission.Update...)
		}
	}
	for _, use := range request.FieldUses {
		if _, ok := d.Fields[use]; !ok {
			d.Fields[use] = append([]string(nil), selectFields...)
		}
	}
	return NormalizeDecision(request, d)
}

type OperationPolicyProvider func(context.Context, PolicyRequest) (PolicyDecision, error)

func (p OperationPolicyProvider) Decide(ctx context.Context, request PolicyRequest) (PolicyDecision, error) {
	d, err := p(ctx, request)
	if err != nil {
		return PolicyDecision{}, err
	}
	return NormalizeDecision(request, d)
}
