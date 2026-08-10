package gql

import (
	"context"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler/pgsql"
	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestPurgeAndAuditOutboxSharePostgresStatement(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	require.NoError(t, db.Exec(`CREATE TABLE articles (
		id bigserial PRIMARY KEY, tenant_id text NOT NULL, title text NOT NULL,
		deleted_at timestamptz, deleted_by text
	)`).Error)
	require.NoError(t, db.Exec(`INSERT INTO articles (tenant_id,title,deleted_at,deleted_by)
		VALUES ('tenant-a','expired',CURRENT_TIMESTAMP,'old-actor')`).Error)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "public")
	k.Set("metadata.classes", map[string]*internal.ClassConfig{
		"Article": {Table: "articles", Operations: []string{"select", "delete", "restore", "purge"}, Scope: []internal.ScopeConfig{{Column: "tenant_id", Context: "tenant"}}},
	})
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compile, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	executor, err := newEngineForTest(db, NewRenderer(meta), meta, compile)
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user-a", Actor: "actor-a", RequestID: "request-a", PolicyRevision: 7})
	require.NoError(t, err)
	ctx := WithIdentity(context.Background(), identity)
	require.NoError(t, db.Exec(`INSERT INTO articles (tenant_id,title) VALUES ('tenant-a','a'),('tenant-a','b'),('tenant-a','c')`).Error)
	page := executor.run(ctx, `query { articleConnection(first: 1, sort: [{id: ASC}]) {
		links: edges { token: cursor item: node { id title } } info: pageInfo { hasNext end } count: totalCount
	} }`, nil, "")
	require.Empty(t, page.Errors, "%v", page.Errors)
	connection := page.Data["articleConnection"].(map[string]any)
	require.EqualValues(t, 3, connection["count"])
	links := connection["links"].([]any)
	require.Len(t, links, 1)
	cursor := links[0].(map[string]any)["token"].(string)
	page = executor.run(ctx, `query($after: Cursor) { articleConnection(first: 1, after: $after, sort: [{id: ASC}]) {
		edges { node { id } } totalCount
	} }`, map[string]any{"after": cursor}, "")
	require.Empty(t, page.Errors, "%v", page.Errors)
	require.EqualValues(t, 3, page.Data["articleConnection"].(map[string]any)["totalCount"])

	mutation := `mutation { purgeArticles(where: { id: { eq: 1 } }) { affectedCount } }`

	reply := executor.run(ctx, mutation, nil, "")
	require.NotEmpty(t, reply.Errors, "缺失outbox时purge必须失败")
	var rows int64
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM articles WHERE id=1`).Scan(&rows).Error)
	require.EqualValues(t, 1, rows, "outbox失败必须回滚物理删除")

	require.NoError(t, db.Exec(pgsql.AuditOutboxDDL).Error)
	reply = executor.run(ctx, mutation, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM articles WHERE id=1`).Scan(&rows).Error)
	require.Zero(t, rows)
	var tenant, actor, action, resource, request, snapshot string
	var revision, affected int64
	require.NoError(t, db.Raw(`SELECT tenant,actor,action,resource,request_id,snapshot,policy_revision,affected_count
		FROM gql_audit_outbox WHERE event_key='request-a:tenant-a:purge:Article:0'`).Row().Scan(&tenant, &actor, &action, &resource, &request, &snapshot, &revision, &affected))
	require.Equal(t, []string{"tenant-a", "actor-a", "purge", "Article", "request-a"}, []string{tenant, actor, action, resource, request})
	require.NotEmpty(t, snapshot)
	require.EqualValues(t, 7, revision)
	require.EqualValues(t, 1, affected)

	reply = executor.run(ctx, mutation, nil, "")
	require.Empty(t, reply.Errors)
	require.EqualValues(t, 0, reply.Data["purgeArticles"].(map[string]interface{})["affectedCount"])
	require.NoError(t, db.Raw(`SELECT COUNT(*) FROM gql_audit_outbox`).Scan(&rows).Error)
	require.EqualValues(t, 1, rows, "同一request重试不得产生重复审计事件")

	k2, err := std.NewKonfig()
	require.NoError(t, err)
	k2.Set("mode", "dev")
	k2.Set("app.root", t.TempDir())
	k2.Set("schema.schema", "public")
	meta2, err := NewMetadata(k2, db)
	require.NoError(t, err)
	schema, err := NewRenderer(meta2).Render()
	require.NoError(t, err)
	require.NotContains(t, schema, "gqlAuditOutbox", "内部审计表不得进入公共Schema")
}
