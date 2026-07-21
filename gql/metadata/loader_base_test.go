package metadata

import (
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
