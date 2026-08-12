package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestGovernanceDefaultPredicateUsesContextSlot(t *testing.T) {
	ctx := compiler.NewContext(nil, `"`, nil)
	defer ctx.Release()
	d := &Dialect{}
	c := &protocol.Class{Table: "users", Fields: map[string]*protocol.Field{"tenant": {Name: "tenant", Column: "tenant"}}, Where: []protocol.Predicate{{Column: "tenant", Operator: "eq", Context: "tenant"}}}
	require.NoError(t, d.buildWhere(ctx, scope{class: c, qualifier: "users"}, ast.ArgumentList{}, d.fixedWhereConjuncts(ctx, "users", c, protocol.OP_SELECT, "")...))
	require.Equal(t, `WHERE "users"."tenant" = $1`, ctx.String())
	require.Equal(t, "tenant", ctx.Slots()[0].Context)
}

func TestGovernancePermissionsAreStableRoleDisjunction(t *testing.T) {
	ctx := compiler.NewContext(nil, `"`, nil)
	defer ctx.Release()
	d := &Dialect{}
	c := &protocol.Class{Table: "users", Permissions: map[string]*protocol.Permission{
		"zeta":  {Select: []string{"id"}, Filter: []protocol.Predicate{{Column: "tenant", Operator: "eq", Context: "tenant"}}},
		"admin": {Select: []string{"id"}},
	}}
	require.NoError(t, d.buildWhere(ctx, scope{class: c, qualifier: "users"}, ast.ArgumentList{}, d.fixedWhereConjuncts(ctx, "users", c, protocol.OP_SELECT, "")...))
	require.Equal(t, `WHERE (($1 = ANY($2)) OR ($3 = ANY($2) AND "users"."tenant" = $4))`, ctx.String())
	require.Equal(t, protocol.CONTEXT_ROLES, ctx.Slots()[1].Context)
	require.True(t, ctx.Slots()[1].List)
	args, err := compiler.ResolveSlots(ctx.Slots(), nil, map[string]any{protocol.CONTEXT_ROLES: []string{"admin", "zeta"}, "tenant": "t1"})
	require.NoError(t, err)
	require.Equal(t, []string{"admin", "zeta"}, args[1])
}

func TestGovernanceUpsertConflictUsesUpdatePolicy(t *testing.T) {
	c := &protocol.Class{Name: "User", Table: "users", PrimaryKeys: []string{"id"}, Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id"}, "name": {Name: "name", Column: "name"},
	}, Where: []protocol.Predicate{{Column: "active", Operator: "eq", Value: true}}, Permissions: map[string]*protocol.Permission{
		"writer": {Insert: []string{"name"}, Update: []string{"name"}, Filter: []protocol.Predicate{{Column: "owner_id", Operator: "eq", Context: "userId"}}},
	}}
	ctx := compiler.NewContext(testHoster{class: c}, `"`, nil)
	defer ctx.Release()
	m := &mutation{field: &ast.Field{Name: "upsertUsers", Arguments: ast.ArgumentList{{
		Name: protocol.INPUT, Value: &ast.Value{Kind: ast.ListValue, Children: []*ast.ChildValue{{Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "name", Value: &ast.Value{Kind: ast.StringValue, Raw: "x"}}}}}}},
	}}}, class: c, op: protocol.UPSERT, shape: compiler.ShapeBulk}
	require.NoError(t, (&Dialect{}).buildUpsert(ctx, m))
	require.Contains(t, ctx.String(), `DO UPDATE SET`)
	require.Contains(t, ctx.String(), `WHERE`)
	require.Contains(t, ctx.String(), `"users"."active" =`)
	require.Contains(t, ctx.String(), `"users"."owner_id" =`)
}

func TestGovernanceEmptyPermissionsDeny(t *testing.T) {
	ctx := compiler.NewContext(nil, `"`, nil)
	defer ctx.Release()
	d := &Dialect{}
	c := &protocol.Class{Table: "users", Permissions: map[string]*protocol.Permission{}}
	require.NoError(t, d.buildWhere(ctx, scope{class: c, qualifier: "users"}, ast.ArgumentList{}, d.fixedWhereConjuncts(ctx, "users", c, protocol.OP_SELECT, "")...))
	require.Equal(t, `WHERE FALSE`, ctx.String())
}

func TestGovernanceMutationFormsAndSoftDelete(t *testing.T) {
	c := &protocol.Class{Name: "User", Table: "users", PrimaryKeys: []string{"id"}, Fields: map[string]*protocol.Field{"id": {Name: "id", Column: "id"}, "name": {Name: "name", Column: "name"}}, SoftDelete: &protocol.SoftDelete{Column: "deleted_at"}}
	ctx := compiler.NewContext(testHoster{class: c}, `"`, nil)
	defer ctx.Release()
	for _, tc := range []struct {
		name string
		kind compiler.OperationShape
	}{{"updateUserByPk", compiler.ShapeSingleByPK}, {"updateUsersMany", compiler.ShapeBulkResult}, {"deleteUserByPk", compiler.ShapeSingleByPK}, {"deleteUsersMany", compiler.ShapeBulkResult}, {"restoreUserByPk", compiler.ShapeSingleByPK}, {"restoreUsersMany", compiler.ShapeBulkResult}} {
		m, err := parseMutation(ctx, &ast.Field{Name: tc.name})
		require.NoError(t, err)
		require.Equal(t, tc.kind, m.shape, tc.name)
	}
	field := &ast.Field{Name: "deleteUser", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}
	m, err := parseMutation(ctx, field)
	require.NoError(t, err)
	require.NoError(t, (&Dialect{}).buildDelete(ctx, m))
	require.Contains(t, ctx.String(), `UPDATE "users" SET "deleted_at" = CURRENT_TIMESTAMP`)
	require.Contains(t, ctx.String(), `"users"."deleted_at" IS NULL`)
}

func TestGovernancePresetAndRestoreSQL(t *testing.T) {
	c := &protocol.Class{Name: "User", Table: "users", Fields: map[string]*protocol.Field{
		"id":      {Name: "id", Column: "id"},
		"created": {Name: "created", Column: "created_at", InsertPreset: &protocol.ValueSource{Context: "now"}},
		"updated": {Name: "updated", Column: "updated_at", UpdatePreset: &protocol.ValueSource{Value: "fixed"}},
	}}
	ctx := compiler.NewContext(testHoster{class: c}, `"`, nil)
	defer ctx.Release()
	d := &Dialect{}
	row, err := d.literalRow(ctx, c, &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "id", Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}, protocol.OP_INSERT)
	require.NoError(t, err)
	rows := []inputRow{row}
	d.applyPreset(c, rows, protocol.OP_INSERT)
	_, err = d.writeInsertValues(ctx, c, rows)
	require.NoError(t, err)
	require.Contains(t, ctx.String(), `"created_at") VALUES ($1, $2)`)

	soft := *c
	soft.SoftDelete = &protocol.SoftDelete{Column: "deleted_at"}
	m := &mutation{field: &ast.Field{Name: "restoreUser", Arguments: ast.ArgumentList{{Name: protocol.WHERE, Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "id", Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: protocol.EQ, Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}}}}}}}, class: &soft, op: protocol.OP_RESTORE}
	ctx2 := compiler.NewContext(testHoster{class: &soft}, `"`, nil)
	defer ctx2.Release()
	require.NoError(t, d.buildRestore(ctx2, m))
	require.Contains(t, ctx2.String(), `SET "deleted_at" = NULL`)
	require.Contains(t, ctx2.String(), `"deleted_at" IS NOT NULL`)
}

func TestGovernanceMutationResultSelectionDriven(t *testing.T) {
	c := &protocol.Class{Name: "User", Table: "users"}
	ctx := compiler.NewContext(testHoster{class: c}, `"`, nil)
	defer ctx.Release()
	d := &Dialect{}
	field := &ast.Field{Name: "updateUsers", SelectionSet: ast.SelectionSet{
		&ast.Field{Name: "affectedCount", Alias: "affectedCount"},
		&ast.Field{Name: "returning", Alias: "rows"},
	}}
	m := &mutation{field: field, class: c, shape: compiler.ShapeBulkResult}
	d.buildMutationResultJSON(ctx, m)
	require.Equal(t, `JSONB_BUILD_OBJECT('affectedCount', (SELECT COUNT(*) FROM "users"), 'rows', '[]')`, ctx.String())
}

func (my *_DialectSuite) TestGovernanceRelationUpdateInjectsTargetPolicy() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{
		"metadata.classes": map[string]*internal.ClassConfig{
			"User": {
				Table: "sys_user", Operations: []string{protocol.OP_INSERT},
				Fields: map[string]*internal.FieldConfig{
					"id": {Type: "ID", Column: "id", IsPrimary: true}, "name": {Type: "String", Column: "name"},
				},
			},
			"Post": {
				Table: "sys_post", Operations: []string{protocol.OP_UPDATE},
				Where: []internal.PredicateConfig{{Column: "active", Operator: "eq", Value: true}},
				Permissions: map[string]*internal.PermissionConfig{"writer": {
					Update: []string{"userId"}, Filter: []internal.PredicateConfig{{Column: "ownerId", Operator: "eq", Context: "userId"}},
				}},
				Fields: map[string]*internal.FieldConfig{
					"id": {Type: "ID", Column: "id", IsPrimary: true}, "ownerId": {Type: "ID", Column: "owner_id"}, "active": {Type: "Boolean", Column: "active"},
					"userId":    {Type: "ID", Column: "user_id", Relation: &internal.RelationConfig{TargetClass: "User", TargetField: "id", Type: "ManyToOne"}},
					"updatedAt": {Type: "DateTime", Column: "updated_at", UpdatePreset: &internal.ValueConfig{Context: "now"}},
				},
			},
		},
	})
	doc, gqlErr := gqlparser.LoadQuery(schema, `mutation { createUser(input: { name: "x", posts: { connect: [1] } }) { id } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)
	my.Contains(sql, `UPDATE "public"."sys_post" SET "user_id"`)
	my.Contains(sql, `"updated_at" = CURRENT_TIMESTAMP`)
	my.Contains(sql, `"sys_post"."active" = $`)
	my.Contains(sql, `"sys_post"."owner_id" = $`)
}

func (my *_DialectSuite) TestGovernanceManyToManyDisconnectInjectsTargetPolicy() {
	meta, schema, dialect := my.newSuite(map[string]interface{}{
		"metadata.classes": map[string]*internal.ClassConfig{
			"Post": {
				Table: "sys_post", Operations: []string{protocol.OP_UPDATE},
				Fields: map[string]*internal.FieldConfig{
					"id": {Type: "ID", Column: "id", IsPrimary: true, Relation: &internal.RelationConfig{
						TargetClass: "Tag", TargetField: "id", Type: "ManyToMany",
						Through: &internal.ThroughConfig{TableName: "sys_post_tag", SourceKey: "post_id", TargetKey: "tag_id"},
					}},
					"title": {Type: "String", Column: "title"},
				},
			},
			"Tag": {
				Table: "sys_tag", Operations: []string{protocol.OP_UPDATE},
				Permissions: map[string]*internal.PermissionConfig{"writer": {
					Update: []string{"id"}, Filter: []internal.PredicateConfig{{Column: "ownerId", Operator: "eq", Context: "userId"}},
				}},
				Fields: map[string]*internal.FieldConfig{
					"id": {Type: "ID", Column: "id", IsPrimary: true}, "ownerId": {Type: "ID", Column: "owner_id"},
				},
			},
		},
	})
	doc, gqlErr := gqlparser.LoadQuery(schema, `mutation { updatePost(input: { tags: { disconnect: [1] } }, id: 2) { id } }`)
	my.Require().Empty(gqlErr)
	compile, err := gql.NewCompiler(meta, []compiler.Dialect{dialect})
	my.Require().NoError(err)
	sql, _, err := compile.Build(doc.Operations[0], nil)
	my.Require().NoError(err)
	my.Contains(sql, `DELETE FROM "public"."sys_post_tag"`)
	my.Contains(sql, `"sys_tag"."owner_id" = $`)
}

type testHoster struct{ class *protocol.Class }

func (h testHoster) PutNode(string, *protocol.Class) error { return nil }
func (h testHoster) GetNode(name string) (*protocol.Class, bool) {
	if name == h.class.Name || name == h.class.Table {
		return h.class, true
	}
	return nil, false
}
func (h testHoster) SetVersion(string) {}
func (h testHoster) LookupOperation(coordinate string) (compiler.OperationSpec, bool) {
	variants := map[string]compiler.OperationSpec{
		"Mutation.updateUserByPk":   {Operation: protocol.OP_UPDATE, Shape: compiler.ShapeSingleByPK},
		"Mutation.updateUsersMany":  {Operation: protocol.OP_UPDATE, Shape: compiler.ShapeBulkResult},
		"Mutation.deleteUser":       {Operation: protocol.OP_DELETE},
		"Mutation.deleteUserByPk":   {Operation: protocol.OP_DELETE, Shape: compiler.ShapeSingleByPK},
		"Mutation.deleteUsersMany":  {Operation: protocol.OP_DELETE, Shape: compiler.ShapeBulkResult},
		"Mutation.purgeUserByPk":    {Operation: protocol.OP_PURGE, Shape: compiler.ShapeSingleByPK},
		"Mutation.restoreUserByPk":  {Operation: protocol.OP_RESTORE, Shape: compiler.ShapeSingleByPK},
		"Mutation.restoreUsersMany": {Operation: protocol.OP_RESTORE, Shape: compiler.ShapeBulkResult},
	}
	spec, ok := variants[coordinate]
	spec.Class = h.class
	return spec, ok
}
