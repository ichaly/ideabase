package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func policyField(name, typeName string, args ast.ArgumentList, selection ast.SelectionSet) *ast.Field {
	return &ast.Field{
		Name:         name,
		Alias:        name,
		Arguments:    args,
		SelectionSet: selection,
		Definition:   &ast.FieldDefinition{Type: ast.NamedType(typeName, nil)},
	}
}

func variableInput(name string) ast.ArgumentList {
	return ast.ArgumentList{{Name: protocol.INPUT, Value: &ast.Value{Kind: ast.Variable, Raw: name}}}
}

func policyMetadata(classes ...*protocol.Class) *Metadata {
	nodes := make(map[string]*protocol.Class, len(classes))
	for _, class := range classes {
		if class.Table == "" {
			class.Table = class.Name
		}
		nodes[class.Name] = class
	}
	meta := &Metadata{Nodes: nodes}
	meta.operations = buildOperationCatalog(meta)
	return meta
}

func TestPolicyBatchCheckEvaluatesEveryRow(t *testing.T) {
	c := &protocol.Class{Name: "Item", Table: "items", Fields: map[string]*protocol.Field{}}
	c.AddField(&protocol.Field{Name: "ownerId", Column: "owner_id"})
	p := &protocol.Permission{
		Insert: []string{"ownerId"},
		Check:  []protocol.Predicate{{Column: "owner_id", Operator: "eq", Context: "userId"}},
	}
	f := policyField("createItems", "Item", variableInput("input"), nil)
	executor := &Executor{}

	pass := map[string]any{"input": []any{
		map[string]any{"ownerId": "u1"},
		map[string]any{"ownerId": "u1"},
	}}
	require.NoError(t, executor.authorizeMutation(c, f, p, protocol.OP_INSERT, pass, map[string]any{"userId": "u1"}))

	fail := map[string]any{"input": []any{
		map[string]any{"ownerId": "u1"},
		map[string]any{"ownerId": "u2"},
	}}
	require.ErrorContains(t, executor.authorizeMutation(c, f, p, protocol.OP_INSERT, fail, map[string]any{"userId": "u1"}), "owner_id")
}

func TestPolicyNestedMutationAuthorizesTarget(t *testing.T) {
	parent := &protocol.Class{Name: "Parent", Table: "parents", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"writer": {Insert: []string{"name"}},
	}}
	parent.AddField(&protocol.Field{Name: "name", Column: "name"})
	child := &protocol.Class{Name: "Child", Table: "children", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"writer": {Insert: []string{"title"}, Update: []string{"id"}},
	}}
	child.AddField(&protocol.Field{Name: "id", Column: "id"})
	child.AddField(&protocol.Field{Name: "title", Column: "title"})
	child.AddField(&protocol.Field{Name: "secret", Column: "secret"})
	parent.AddField(&protocol.Field{Name: "children", Type: "Child", IsList: true, Relation: &protocol.Relation{TargetClass: "Child"}})

	executor := &Executor{metadata: policyMetadata(parent, child)}
	root := policyField("createParent", "Parent", variableInput("input"), nil)
	op := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{root}}
	ctx := WithRole(context.Background(), "writer")

	allowed := map[string]any{"input": map[string]any{
		"name": "p", "children": map[string]any{"create": []any{map[string]any{"title": "ok"}}},
	}}
	require.NoError(t, executor.authorize(ctx, op, allowed))

	denied := map[string]any{"input": map[string]any{
		"name": "p", "children": map[string]any{"create": []any{map[string]any{"secret": "no"}}},
	}}
	err := executor.authorize(ctx, op, denied)
	require.ErrorContains(t, err, "class=Child")
	require.ErrorContains(t, err, "field=secret")
}

func TestPolicyAggregateVariableGroupByUsesSelectWhitelist(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}}
	c.AddField(&protocol.Field{Name: "public", Column: "public"})
	c.AddField(&protocol.Field{Name: "secret", Column: "secret"})
	p := &protocol.Permission{Select: []string{"public"}, Aggregate: true}
	f := policyField("itemStats", "ItemStats", ast.ArgumentList{{
		Name: protocol.GROUP_BY, Value: &ast.Value{Kind: ast.Variable, Raw: "groups"},
	}}, nil)
	require.ErrorContains(t, authorizeArgs(c, f, p, protocol.OP_AGGREGATE, map[string]any{"groups": []any{"secret"}}), "secret")
}

func TestPolicyMutationResultAffectedCountDoesNotRequireSelect(t *testing.T) {
	c := &protocol.Class{Name: "Item", Table: "items", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"writer": {Update: []string{"name"}},
	}}
	c.AddField(&protocol.Field{Name: "name", Column: "name"})
	root := policyField("updateItems", "ItemMutationResult", variableInput("input"), ast.SelectionSet{
		policyField("affectedCount", "Int", nil, nil),
	})
	executor := &Executor{metadata: policyMetadata(c)}
	op := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{root}}
	err := executor.authorize(WithRole(context.Background(), "writer"), op, map[string]any{"input": map[string]any{"name": "x"}})
	require.NoError(t, err)
}

func TestPolicyNestedConnectRequiresTargetUpdate(t *testing.T) {
	parent := &protocol.Class{Name: "Parent", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"writer": {Insert: []string{"name"}},
	}}
	parent.AddField(&protocol.Field{Name: "name", Column: "name"})
	child := &protocol.Class{Name: "Child", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"writer": {Insert: []string{"id"}},
	}}
	child.AddField(&protocol.Field{Name: "id", Column: "id"})
	parent.AddField(&protocol.Field{Name: "children", Relation: &protocol.Relation{TargetClass: "Child"}, IsList: true})
	executor := &Executor{metadata: policyMetadata(parent, child)}
	root := policyField("createParent", "Parent", variableInput("input"), nil)
	op := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{root}}
	vars := map[string]any{"input": map[string]any{"name": "p", "children": map[string]any{"connect": []any{1}}}}
	err := executor.authorize(WithRole(context.Background(), "writer"), op, vars)
	require.ErrorContains(t, err, "class=Child")
	require.ErrorContains(t, err, "action=update")
}

func TestPolicyUpdateCheckMayUseIdenticalRowFilter(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}}
	c.AddField(&protocol.Field{Name: "name", Column: "name"})
	c.AddField(&protocol.Field{Name: "ownerId", Column: "owner_id"})
	predicate := protocol.Predicate{Column: "owner_id", Operator: "eq", Context: "userId"}
	p := &protocol.Permission{Update: []string{"name"}, Filter: []protocol.Predicate{predicate}, Check: []protocol.Predicate{predicate}}
	f := policyField("updateItem", "Item", variableInput("input"), nil)
	err := (&Executor{}).authorizeMutation(c, f, p, protocol.OP_UPDATE,
		map[string]any{"input": map[string]any{"name": "new"}}, map[string]any{"userId": "u1"})
	require.NoError(t, err)
}

func TestPolicyCheckMissingTrustedContextDenied(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}}
	c.AddField(&protocol.Field{Name: "ownerId", Column: "owner_id", InsertPreset: &protocol.ValueSource{Context: "userId"}})
	p := &protocol.Permission{Insert: []string{"*"}, Check: []protocol.Predicate{{Column: "owner_id", Operator: "eq", Context: "userId"}}}
	f := policyField("createItem", "Item", variableInput("input"), nil)
	err := (&Executor{}).authorizeMutation(c, f, p, protocol.OP_INSERT,
		map[string]any{"input": map[string]any{}}, map[string]any{})
	require.ErrorContains(t, err, "owner_id")
}

func TestOperationNeedsPolicyIncludesGovernedRelationTarget(t *testing.T) {
	parent := &protocol.Class{Name: "Parent", Fields: map[string]*protocol.Field{}}
	child := &protocol.Class{Name: "Child", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{"writer": {Insert: []string{"*"}}}}
	parent.AddField(&protocol.Field{Name: "name", Column: "name"})
	parent.AddField(&protocol.Field{Name: "children", Relation: &protocol.Relation{TargetClass: "Child"}, IsList: true})
	meta := policyMetadata(parent, child)
	root := policyField("createParent", "Parent", variableInput("input"), nil)
	op := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{root}}
	require.True(t, operationNeedsPolicy(meta, op))
	require.True(t, operationNeedsRole(meta, op))
	executor := &Executor{metadata: meta}
	err := executor.authorize(context.Background(), op, map[string]any{"input": map[string]any{
		"children": map[string]any{"create": []any{map[string]any{}}},
	}})
	require.ErrorContains(t, err, "class=Child")
}

func TestQueryGovernanceProbeOnlyFollowsSelectedRelations(t *testing.T) {
	parent := &protocol.Class{Name: "Parent", Fields: map[string]*protocol.Field{}}
	parent.AddField(&protocol.Field{Name: "id", Column: "id"})
	child := &protocol.Class{Name: "Child", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{"reader": {Select: []string{"id"}}}}
	child.AddField(&protocol.Field{Name: "id", Column: "id"})
	parent.AddField(&protocol.Field{Name: "children", Relation: &protocol.Relation{TargetClass: "Child"}, IsList: true})
	meta := policyMetadata(parent, child)
	root := policyField("parents", "Parent", nil, ast.SelectionSet{policyField("id", "ID", nil, nil)})
	op := &ast.OperationDefinition{Operation: ast.Query, SelectionSet: ast.SelectionSet{root}}
	require.False(t, operationNeedsPolicy(meta, op))
	require.False(t, operationNeedsRole(meta, op))
}

func TestPolicyUpsertRequiresUpdatePermissionAndColumns(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"creator": {Insert: []string{"name"}},
		"writer":  {Insert: []string{"name"}, Update: []string{"name"}},
	}}
	c.AddField(&protocol.Field{Name: "name", Column: "name"})
	c.Constraints = []*protocol.Constraint{{Name: "item_name", Kind: protocol.ConstraintUnique, Columns: []string{"name"}}}
	executor := &Executor{metadata: policyMetadata(c)}
	root := policyField("upsertItems", "Item", variableInput("input"), nil)
	op := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{root}}
	vars := map[string]any{"input": []any{map[string]any{"name": "x"}}}

	require.ErrorContains(t, executor.authorize(WithRole(context.Background(), "creator"), op, vars), "action=update")
	require.NoError(t, executor.authorize(WithRole(context.Background(), "writer"), op, vars))
}

func TestPolicyUnknownGovernedRootMutationFailsClosed(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}, Permissions: map[string]*protocol.Permission{
		"viewer": {Select: []string{"id"}},
	}}
	c.AddField(&protocol.Field{Name: "id", Column: "id"})
	executor := &Executor{metadata: &Metadata{Nodes: map[string]*protocol.Class{"Item": c}}}
	root := policyField("saveItem", "Item", nil, ast.SelectionSet{policyField("id", "ID", nil, nil)})
	op := &ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{root}}

	require.ErrorContains(t, executor.authorize(WithRole(context.Background(), "viewer"), op, nil), "action=mutation")
}

func TestPolicyMutationFilterAndConflictColumnsUseSelectWhitelist(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}}
	c.AddField(&protocol.Field{Name: "name", Column: "name"})
	c.AddField(&protocol.Field{Name: "secret", Column: "secret"})
	c.Constraints = []*protocol.Constraint{{Name: "secret_key", Kind: protocol.ConstraintUnique, Columns: []string{"secret"}}}
	p := &protocol.Permission{Select: []string{"name"}, Insert: []string{"name", "secret"}, Update: []string{"name", "secret"}}
	where := policyField("updateItems", "ItemMutationResult", ast.ArgumentList{{
		Name: protocol.WHERE, Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "secret", Value: &ast.Value{Kind: ast.ObjectValue}}}},
	}}, nil)
	require.ErrorContains(t, authorizeArgs(c, where, p, protocol.OP_UPDATE, nil), "secret")
	on := policyField("upsertItems", "Item", ast.ArgumentList{{
		Name: protocol.ON, Value: &ast.Value{Kind: ast.EnumValue, Raw: "UNIQUE_1"},
	}}, nil)
	require.ErrorContains(t, authorizeArgs(c, on, p, protocol.OP_INSERT, nil), "secret")
}

func TestPolicyEntityFieldNamedLikeWrapperStillRequiresSelect(t *testing.T) {
	c := &protocol.Class{Name: "Item", Fields: map[string]*protocol.Field{}}
	c.AddField(&protocol.Field{Name: "id", Column: "id"})
	c.AddField(&protocol.Field{Name: "total", Column: "total"})
	p := &protocol.Permission{Select: []string{"id"}}
	f := policyField("items", "Item", nil, ast.SelectionSet{policyField("total", "Int", nil, nil)})
	require.ErrorContains(t, (&Executor{}).authorizeSelection(c, f, p, protocol.OP_SELECT, nil), "total")
}
