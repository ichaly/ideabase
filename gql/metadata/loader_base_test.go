package metadata

import (
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type routineHoster struct {
	*fakeHoster
	routines map[string]*protocol.Routine
}

func (h *routineHoster) PutRoutine(key string, routine *protocol.Routine) error {
	if h.routines == nil {
		h.routines = make(map[string]*protocol.Routine)
	}
	h.routines[key] = routine
	return nil
}

func TestCatalogBuildsFieldsConstraintsRelationsIndexesAndRoutines(t *testing.T) {
	h := &routineHoster{fakeHoster: newFakeHoster()}
	err := (&baseLoader{}).loadCatalog(h, catalogData{
		Tables: []tableInfo{{Schema: "public", TableName: "users", TableKind: string(protocol.TableBase)}, {TableName: "posts", TableKind: string(protocol.TableBase)}},
		Columns: []columnInfo{
			{TableName: "users", ColumnName: "id", DataType: "int8", TypeKind: string(protocol.TypeBase), TypeName: "int8", DefaultExpression: "nextval"},
			{TableName: "users", ColumnName: "name", DataType: "text", TypeKind: string(protocol.TypeBase), TypeName: "text"},
			{TableName: "posts", ColumnName: "id", DataType: "int8", TypeKind: string(protocol.TypeBase), TypeName: "int8", GenerationKind: "stored", GenerationExpression: "identity"},
			{TableName: "posts", ColumnName: "user_id", DataType: "int8", TypeKind: string(protocol.TypeBase), TypeName: "int8"},
		},
		Constraints: []constraintInfo{
			{TableName: "users", Name: "users_pk", Kind: string(protocol.ConstraintPrimary), Columns: []string{"id"}},
			{TableName: "posts", Name: "posts_pk", Kind: string(protocol.ConstraintPrimary), Columns: []string{"id"}},
			{TableName: "posts", Name: "posts_user_fk", Kind: string(protocol.ConstraintForeign), Columns: []string{"user_id"}, ReferencedTable: "users", ReferencedColumns: []string{"id"}},
			{TableName: "users", Name: "users_name_unique", Kind: string(protocol.ConstraintUnique), Columns: []string{"name"}},
		},
		Indexes:  []indexInfo{{TableName: "users", Name: "users_name_idx", Keys: []indexKeyInfo{{Column: "name"}}, Unique: true}},
		Routines: []routineInfo{{Schema: "public", Name: "count_users", IdentityArguments: "", Kind: "function", ReturnType: "int8", Volatility: "stable", Arguments: []routineArgumentInfo{{Name: "tenant", Type: "int8"}}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	users, ok := h.GetNode("users")
	if !ok || !users.Fields["id"].IsPrimary || !users.Fields["name"].IsUnique || len(users.Relations) != 1 {
		t.Fatalf("users catalog incomplete: %#v", users)
	}
	posts, ok := h.GetNode("posts")
	if !ok || !posts.Fields["id"].ReadOnly || len(posts.Relations) != 1 {
		t.Fatalf("posts catalog incomplete: %#v", posts)
	}
	if _, ok := h.routines["public.count_users()"]; !ok || h.version == "" {
		t.Fatal("routine or catalog version was not injected")
	}
}

// 自引用多对多(user_friends两外键均指向users):两方向关系必须并存——
// 单指针模型下r2覆盖r1只剩一个方向
func TestManyToManySelfReference(t *testing.T) {
	users := &protocol.Class{
		Name: "users", Table: "users", PrimaryKeys: []string{"id"},
		Fields: map[string]*protocol.Field{
			"id": {Name: "id", Column: "id", IsPrimary: true},
		},
	}
	friends := &protocol.Class{
		Name: "user_friends", Table: "user_friends",
		Fields: map[string]*protocol.Field{
			"user_id":   {Name: "user_id", Column: "user_id"},
			"friend_id": {Name: "friend_id", Column: "friend_id"},
		},
	}
	classes := map[string]*protocol.Class{"users": users, "user_friends": friends}
	groups := [][]foreignKeyInfo{
		{{SourceTable: "user_friends", SourceColumn: "user_id", TargetTable: "users", TargetColumn: "id", ConstraintName: "fk_user"}},
		{{SourceTable: "user_friends", SourceColumn: "friend_id", TargetTable: "users", TargetColumn: "id", ConstraintName: "fk_friend"}},
	}
	pks := []primaryKeyInfo{
		{TableName: "user_friends", ColumnName: "user_id"},
		{TableName: "user_friends", ColumnName: "friend_id"},
	}

	detectManyToManyRelations(classes, groups, pks)

	require.True(t, friends.IsThrough, "应识别为中间表")
	require.Len(t, users.Relations, 2, "自引用多对多两方向必须并存")
	keys := map[string]*protocol.Through{}
	for _, rel := range users.Relations {
		assert.Equal(t, protocol.MANY_TO_MANY, rel.Type)
		keys[rel.Through.SourceKey+">"+rel.Through.TargetKey] = rel.Through
	}
	assert.Contains(t, keys, "user_id>friend_id", "user_id方向")
	assert.Contains(t, keys, "friend_id>user_id", "friend_id方向")
}

// 复合外键按约束聚合为一条关系(多行同约束名收敛,列组按序对齐)
func TestGroupByConstraintComposite(t *testing.T) {
	rows := []foreignKeyInfo{
		{SourceTable: "order_items", SourceColumn: "order_id", TargetTable: "orders", TargetColumn: "id", ConstraintName: "fk_order", Position: 1},
		{SourceTable: "order_items", SourceColumn: "tenant_id", TargetTable: "orders", TargetColumn: "tenant_id", ConstraintName: "fk_order", Position: 2},
		{SourceTable: "order_items", SourceColumn: "sku_id", TargetTable: "skus", TargetColumn: "id", ConstraintName: "fk_sku", Position: 1},
	}
	groups := groupByConstraint(rows)
	require.Len(t, groups, 2, "两条约束聚合为两组")
	assert.Len(t, groups[0], 2, "复合外键两行收敛为一组")
	assert.Equal(t, "order_id", groups[0][0].SourceColumn)
	assert.Equal(t, "tenant_id", groups[0][1].SourceColumn, "列序保持约束内位置")
	assert.Len(t, groups[1], 1)
}

func TestCatalogReferencesFailClosed(t *testing.T) {
	validTable := tableInfo{TableName: "accounts", TableKind: string(protocol.TableBase)}
	validColumn := columnInfo{TableName: "accounts", ColumnName: "id", DataType: "int8", TypeName: "int8", TypeKind: string(protocol.TypeBase)}
	tests := []struct {
		name    string
		catalog catalogData
		want    string
	}{
		{"column table", catalogData{Columns: []columnInfo{validColumn}}, "missing table"},
		{"constraint column", catalogData{Tables: []tableInfo{validTable}, Constraints: []constraintInfo{{TableName: "accounts", Name: "account_pk", Kind: string(protocol.ConstraintPrimary), Columns: []string{"missing"}}}}, "missing column"},
		{"index include", catalogData{Tables: []tableInfo{validTable}, Columns: []columnInfo{validColumn}, Indexes: []indexInfo{{TableName: "accounts", Name: "account_idx", Include: []string{"missing"}}}}, "includes missing column"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := (&baseLoader{}).loadCatalog(newFakeHoster(), tt.catalog)
			require.ErrorContains(t, err, tt.want)
		})
	}
}

func TestCatalogDetectsConventionalSoftDelete(t *testing.T) {
	h := newFakeHoster()
	err := (&baseLoader{}).loadCatalog(h, catalogData{
		Tables: []tableInfo{{TableName: "articles", TableKind: string(protocol.TableBase)}},
		Columns: []columnInfo{
			{TableName: "articles", ColumnName: "id", DataType: "int8"},
			{TableName: "articles", ColumnName: "deleted_at", DataType: "timestamp", IsNullable: true},
		},
	})
	require.NoError(t, err)
	class, ok := h.GetNode("articles")
	require.True(t, ok)
	require.Equal(t, "deleted_at", class.SoftDelete.Column)
	require.Contains(t, class.Where, protocol.Predicate{Column: "deleted_at", Operator: "is-null"})
}

func TestCatalogMarksAuditOutboxHidden(t *testing.T) {
	h := newFakeHoster()
	err := (&baseLoader{}).loadCatalog(h, catalogData{
		Tables:  []tableInfo{{TableName: "gql_audit_outbox", TableKind: string(protocol.TableBase)}},
		Columns: []columnInfo{{TableName: "gql_audit_outbox", ColumnName: "event_key", DataType: "text"}},
	})
	require.NoError(t, err)
	class, ok := h.GetNode("gql_audit_outbox")
	require.True(t, ok)
	require.True(t, class.Hidden)
}
