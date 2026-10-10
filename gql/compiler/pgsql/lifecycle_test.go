package pgsql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type recordingAuditWriter struct{ event PurgeAudit }

type multiClassHoster struct{ classes map[string]*protocol.Class }

func (h multiClassHoster) PutNode(string, *protocol.Class) error { return nil }
func (h multiClassHoster) GetNode(name string) (*protocol.Class, bool) {
	c, ok := h.classes[name]
	return c, ok
}
func (h multiClassHoster) SetVersion(string) {}
func (h multiClassHoster) LookupOperation(string) (compiler.OperationSpec, bool) {
	return compiler.OperationSpec{}, false
}

func (writer *recordingAuditWriter) WritePurge(_ *compiler.Context, _ *Dialect, event PurgeAudit) {
	writer.event = event
}

func auditClass() *protocol.Class {
	return &protocol.Class{
		Name: "User", Table: "users", PrimaryKeys: []string{"id"},
		Fields: map[string]*protocol.Field{
			"id":        {Name: "id", Column: "id"},
			"name":      {Name: "name", Column: "name"},
			"createdAt": {Name: "createdAt", Column: "created_at"},
			"createdBy": {Name: "createdBy", Column: "created_by"},
			"updatedAt": {Name: "updatedAt", Column: "updated_at"},
			"updatedBy": {Name: "updatedBy", Column: "updated_by"},
			"deletedAt": {Name: "deletedAt", Column: "deleted_at"},
			"deletedBy": {Name: "deletedBy", Column: "deleted_by"},
		},
		SoftDelete: &protocol.SoftDelete{Column: "deleted_at"},
	}
}

func TestAuditCreateUsesDatabaseClockAndActor(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()

	row, err := (&Dialect{}).literalRow(ctx, class, &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "name", Value: &ast.Value{Kind: ast.StringValue, Raw: "alice"}}}}, protocol.OP_INSERT)
	require.NoError(t, err)
	rows := []inputRow{row}
	d := &Dialect{}
	d.applyAudit(class, rows, protocol.OP_INSERT)
	_, err = d.writeInsertValues(ctx, class, rows)
	require.NoError(t, err)
	require.Contains(t, ctx.String(), `"created_at", "created_by", "updated_at", "updated_by"`)
	require.Contains(t, ctx.String(), `CURRENT_TIMESTAMP`)
	require.Equal(t, "actor", ctx.Slots()[1].Context)
	_, err = compiler.ResolveSlots(ctx.Slots(), nil, nil)
	require.ErrorContains(t, err, "actor")
}

func TestAuditClientValuesAreRejected(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()

	_, err := (&Dialect{}).literalRow(ctx, class, &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "createdAt", Value: &ast.Value{Kind: ast.StringValue, Raw: "spoof"}}}}, protocol.OP_INSERT)
	require.ErrorContains(t, err, "createdAt")
}

func TestAuditLifecycleUpdatesDeletedPair(t *testing.T) {
	class := auditClass()
	d := &Dialect{}
	field := &ast.Field{Name: "deleteUser", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	m := &mutation{field: field, class: class, op: protocol.DELETE}
	require.NoError(t, d.buildDelete(ctx, m))
	require.Contains(t, ctx.String(), `"deleted_at" = CURRENT_TIMESTAMP`)
	require.Contains(t, ctx.String(), `"deleted_by" = $`)
	require.Contains(t, ctx.String(), `"updated_at" = CURRENT_TIMESTAMP`)
	require.Contains(t, ctx.String(), `"updated_by" = $`)

	ctx2 := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx2.Release()
	m.op = protocol.OP_RESTORE
	require.NoError(t, d.buildRestore(ctx2, m))
	require.Contains(t, ctx2.String(), `"deleted_at" = NULL`)
	require.Contains(t, ctx2.String(), `"deleted_by" = NULL`)
	require.Contains(t, ctx2.String(), `"updated_at" = CURRENT_TIMESTAMP`)
	require.Contains(t, ctx2.String(), `"updated_by" = $`)
}

func TestAuditUpdateUsesDatabaseClockAndActor(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	field := &ast.Field{
		Name: "updateUser",
		Arguments: ast.ArgumentList{
			{Name: protocol.INPUT, Value: &ast.Value{Kind: ast.ObjectValue, Children: []*ast.ChildValue{{Name: "name", Value: &ast.Value{Kind: ast.StringValue, Raw: "new"}}}}},
			{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "1"}},
		},
	}
	_, err := (&Dialect{}).buildUpdate(ctx, &mutation{field: field, class: class, op: protocol.OP_UPDATE})
	require.NoError(t, err)
	require.Contains(t, ctx.String(), `"updated_at" = CURRENT_TIMESTAMP`)
	require.Contains(t, ctx.String(), `"updated_by" = $`)
}

func TestNestedCreateAndRelationUpdateUseAuditLifecycle(t *testing.T) {
	parent := &protocol.Class{Name: "User", Table: "users", PrimaryKeys: []string{"id"}, Fields: map[string]*protocol.Field{
		"id": {Name: "id", Column: "id"},
	}}
	target := auditClass()
	target.Name, target.Table = "Post", "posts"
	target.Fields["userId"] = &protocol.Field{Name: "userId", Column: "user_id"}
	rel := &protocol.Relation{SourceClass: "User", SourceField: "id", TargetClass: "Post", TargetField: "userId", Type: protocol.ONE_TO_MANY}
	parent.Fields["posts"] = &protocol.Field{Name: "posts", IsList: true, Relation: rel}
	ctx := compiler.NewContext(multiClassHoster{classes: map[string]*protocol.Class{"User": parent, "users": parent, "Post": target, "posts": target}}, `"`, nil)
	defer ctx.Release()
	row := inputRow{columns: []string{"title"}, values: map[string]paramWriter{"title": func(c *compiler.Context) error { c.Write(`$1`); return nil }}}
	ops := []relationOp{{rel: rel, create: []inputRow{row}, connect: []paramWriter{func(c *compiler.Context) error { c.Write(`$2`); return nil }}}}
	require.NoError(t, (&Dialect{}).buildRelationOps(ctx, parent, ops))
	require.Contains(t, ctx.String(), `"created_at", "created_by", "updated_at", "updated_by"`)
	require.Contains(t, ctx.String(), `SET "user_id" =`)
	require.Contains(t, ctx.String(), `"updated_at" = CURRENT_TIMESTAMP`)
	require.Contains(t, ctx.String(), `"updated_by" = $`)
}

func TestDeletionModeCompilesAsPlanReusablePredicate(t *testing.T) {
	class := auditClass()
	class.Where = []protocol.Predicate{{Column: "deleted_at", Operator: "is-null"}}
	ctx := compiler.NewContext(testHoster{class: class}, `"`, map[string]any{"mode": "ONLY_DELETED"})
	defer ctx.Release()
	args := ast.ArgumentList{{Name: protocol.DELETION, Value: &ast.Value{Kind: ast.Variable, Raw: "mode"}}}
	filters := (&Dialect{}).queryWhereConjuncts(ctx, class.Table, class, protocol.OP_SELECT, args)
	require.Len(t, filters, 1)
	require.NoError(t, filters[0]())
	require.Contains(t, ctx.String(), `= 'WITH_DELETED'`)
	require.Contains(t, ctx.String(), `"users"."deleted_at" IS NOT NULL`)
	require.Len(t, ctx.Slots(), 1)
	require.Equal(t, "mode", ctx.Slots()[0].Variable)
}

func TestPurgeOnlyPhysicallyDeletesSoftDeletedRows(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	field := &ast.Field{Name: "purgeUserByPk", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}
	require.NoError(t, (&Dialect{}).buildPurge(ctx, &mutation{field: field, class: class, op: protocol.OP_PURGE, shape: compiler.ShapeSingleByPK}))
	require.Contains(t, ctx.String(), `DELETE FROM "users"`)
	require.Contains(t, ctx.String(), `"users"."deleted_at" IS NOT NULL`)
	require.NotContains(t, ctx.String(), `UPDATE "users"`)
}

func TestPurgeAuditIsAtomicAndContainsNoRequestVariables(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	field := &ast.Field{Name: "purgeUserByPk", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}
	m := &mutation{field: field, class: class, op: protocol.OP_PURGE, shape: compiler.ShapeSingleByPK}
	require.NoError(t, (&Dialect{}).buildPurge(ctx, m))
	ctx.Write(`)`)
	(&Dialect{}).buildPurgeAudit(ctx, m)
	require.Contains(t, ctx.String(), `INSERT INTO "gql_audit_outbox"`)
	require.Contains(t, ctx.String(), `COUNT(*) FROM "users"`)
	require.NotContains(t, ctx.String(), `input`)
	require.True(t, ctx.UsesAudit())
	_, err := compiler.ResolveSlots(ctx.Slots(), nil, map[string]any{"tenant": "t", "actor": "a", "snapshot": "s", "policyRevision": uint64(1)})
	require.ErrorContains(t, err, "requestId")
}

func TestPurgeAuditEventKeyIsTenantScopedAndRootOccurrenceScoped(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	m := &mutation{class: class, op: protocol.OP_PURGE, occurrence: 2}
	(&Dialect{}).buildPurgeAudit(ctx, m)
	require.Contains(t, ctx.String(), `:purge:User:2`)
	var contexts []string
	for _, slot := range ctx.Slots() {
		contexts = append(contexts, slot.Context)
	}
	require.Contains(t, contexts, "requestId")
	require.Contains(t, contexts, "tenant")
}

func TestMultiplePurgeOccurrencesRetainSeparateAuditCTEs(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	set := ast.SelectionSet{
		&ast.Field{Name: "purgeUserByPk", Alias: "first", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "1"}}}},
		&ast.Field{Name: "purgeUserByPk", Alias: "second", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "2"}}}},
	}
	require.NoError(t, (&Dialect{}).BuildMutation(ctx, set))
	require.Equal(t, 2, strings.Count(ctx.String(), `INSERT INTO "gql_audit_outbox"`))
}

func TestPurgeAuditDoesNotSilentlyIgnoreEventKeyConflicts(t *testing.T) {
	ctx := compiler.NewContext(testHoster{class: auditClass()}, `"`, nil)
	defer ctx.Release()
	(&Dialect{}).buildPurgeAudit(ctx, &mutation{class: auditClass(), occurrence: 0})
	if strings.Contains(ctx.String(), `ON CONFLICT ("event_key") DO NOTHING`) {
		t.Fatalf("purge audit must not silently drop a reused request event: %s", ctx.String())
	}
}

func TestOnlyPurgeMayRepeatOneTable(t *testing.T) {
	class := auditClass()
	for name, set := range map[string]ast.SelectionSet{
		"update_then_purge": {
			&ast.Field{Name: "updateUser", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "1"}}, {Name: protocol.INPUT, Value: &ast.Value{Kind: ast.ObjectValue}}}},
			&ast.Field{Name: "purgeUserByPk", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "1"}}}},
		},
		"purge_then_update": {
			&ast.Field{Name: "purgeUserByPk", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "1"}}}},
			&ast.Field{Name: "updateUser", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Kind: ast.IntValue, Raw: "1"}}, {Name: protocol.INPUT, Value: &ast.Value{Kind: ast.ObjectValue}}}},
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
			defer ctx.Release()
			require.ErrorContains(t, (&Dialect{}).BuildMutation(ctx, set), "不能多次变更表")
		})
	}
}

func TestPurgeSupportsTransactionAwareAuditWriter(t *testing.T) {
	writer := &recordingAuditWriter{}
	dialect := &Dialect{Audit: writer}
	ctx := compiler.NewContext(testHoster{class: auditClass()}, `"`, nil)
	defer ctx.Release()
	dialect.buildPurgeAudit(ctx, &mutation{class: auditClass()})
	require.Equal(t, PurgeAudit{Resource: "User", Source: "users"}, writer.event)
	require.True(t, ctx.UsesAudit())
}
