package pgsql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type recordingAuditWriter struct{ event PurgeAudit }

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
	require.NoError(t, (&Dialect{}).buildPurge(ctx, &mutation{field: field, class: class, op: protocol.OP_PURGE, single: true}))
	require.Contains(t, ctx.String(), `DELETE FROM "users"`)
	require.Contains(t, ctx.String(), `"users"."deleted_at" IS NOT NULL`)
	require.NotContains(t, ctx.String(), `UPDATE "users"`)
}

func TestPurgeAuditIsAtomicAndContainsNoRequestVariables(t *testing.T) {
	class := auditClass()
	ctx := compiler.NewContext(testHoster{class: class}, `"`, nil)
	defer ctx.Release()
	field := &ast.Field{Name: "purgeUserByPk", Arguments: ast.ArgumentList{{Name: protocol.ID, Value: &ast.Value{Raw: "1", Kind: ast.IntValue}}}}
	m := &mutation{field: field, class: class, op: protocol.OP_PURGE, single: true}
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

func TestPurgeSupportsTransactionAwareAuditWriter(t *testing.T) {
	writer := &recordingAuditWriter{}
	dialect := &Dialect{Audit: writer}
	ctx := compiler.NewContext(testHoster{class: auditClass()}, `"`, nil)
	defer ctx.Release()
	dialect.buildPurgeAudit(ctx, &mutation{class: auditClass()})
	require.Equal(t, PurgeAudit{Resource: "User", Source: "users"}, writer.event)
	require.True(t, ctx.UsesAudit())
}
