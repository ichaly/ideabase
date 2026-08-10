package gql

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type costArgs struct {
	Limit int `json:"limit"`
}

type bulkCostArgs struct {
	Input []map[string]any `json:"input" validate:"required"`
}

type costItem struct {
	Value string `json:"value"`
}

func TestOperationCostUsesCapabilityAndSaturates(t *testing.T) {
	op := &ast.OperationDefinition{SelectionSet: ast.SelectionSet{
		&ast.Field{
			Name: "users", Alias: "users",
			Definition: &ast.FieldDefinition{Type: &ast.Type{Elem: &ast.Type{NamedType: "User"}}},
			SelectionSet: ast.SelectionSet{
				&ast.Field{Name: "name", Alias: "name", ObjectDefinition: &ast.Definition{Name: "User"}},
			},
		},
	}}
	caps := map[string]Capability{"Query.users": {Cost: 7}, "User.name": {Cost: 11}}
	require.Equal(t, uint64(40), operationCost(op, nil, 3, caps))
	require.Equal(t, maxUint64, operationCost(op, nil, 0, caps), "无结果上界的列表必须fail closed")
	require.Equal(t, maxUint64, saturatingMul(maxUint64, 2))
	require.Equal(t, maxUint64, saturatingAdd(maxUint64, 1))
}

func TestOperationCostCountsAbstractInlineFragment(t *testing.T) {
	op := &ast.OperationDefinition{SelectionSet: ast.SelectionSet{&ast.Field{
		Name: "node", Definition: &ast.FieldDefinition{Type: &ast.Type{NamedType: "Node"}}, SelectionSet: ast.SelectionSet{
			&ast.InlineFragment{TypeCondition: "User", SelectionSet: ast.SelectionSet{&ast.Field{Name: "name"}}},
		},
	}}}
	require.Equal(t, uint64(2), operationCost(op, nil, 0, nil))
	require.Equal(t, 2, depthOf(op.SelectionSet))
}

func TestOperationCostUsesCoercedBoundsAndBulkInput(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, executor.register(
		NewResolver[Root, costArgs, []costItem]("Query", "costly", "", func(context.Context, Root, costArgs) ([]costItem, error) {
			return nil, nil
		}, Result("[CostItem!]!"), Extra(`type CostItem { value: String! }`), Cost(5)),
		NewResolver[Root, bulkCostArgs, int]("Mutation", "bulkCost", "", func(context.Context, Root, bulkCostArgs) (int, error) {
			return 0, nil
		}, Cost(7)),
	))

	queryCost, err := executor.OperationCost(`query Cost($n: Int = 3) { costly(limit: $n) { value } }`, "Cost", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(8), queryCost, "5 + 3 * CostItem.value")
	queryCost, err = executor.OperationCost(`query Cost($n: Int!) { costly(limit: $n) { value } }`, "Cost", map[string]any{"n": uint(4)})
	require.NoError(t, err)
	require.Equal(t, uint64(9), queryCost)

	variableCost, err := executor.OperationCost(`mutation Bulk($input: [Json!]!) { bulkCost(input: $input) }`, "Bulk", map[string]any{
		"input": []map[string]any{{"id": 1}, {"id": 2}, {"id": 3}},
	})
	require.NoError(t, err)
	require.Equal(t, uint64(21), variableCost)

	literalCost, err := executor.OperationCost(`mutation { bulkCost(input: [{id: 1}, {id: 2}]) }`, "", nil)
	require.NoError(t, err)
	require.Equal(t, uint64(14), literalCost)
}

func TestMaxCostRejectsBeforeResolverAndHonorsConditionalShape(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, func(k *std.Konfig) {
		k.Set("schema.max-cost", 8)
	})
	defer cleanup()
	var calls atomic.Int32
	require.NoError(t, executor.register(
		NewResolver[Root, costArgs, []costItem]("Query", "costly", "", func(context.Context, Root, costArgs) ([]costItem, error) {
			calls.Add(1)
			return []costItem{{Value: "ok"}}, nil
		}, Result("[CostItem!]!"), Extra(`type CostItem { value: String! }`), Cost(5)),
	))

	boundary := executor.run(context.Background(), `{ costly(limit: 3) { value } }`, nil, "")
	require.Empty(t, boundary.Errors)
	require.EqualValues(t, 1, calls.Load())

	tooExpensive := executor.run(context.Background(), `{ costly(limit: 4) { value } }`, nil, "")
	require.NotEmpty(t, tooExpensive.Errors)
	require.Contains(t, tooExpensive.Errors[0].Message, "超过上限")
	require.EqualValues(t, 1, calls.Load(), "超额请求不得进入Resolver")

	const conditional = `query Shape($skip: Boolean!) { costly(limit: 4) @skip(if: $skip) { value } }`
	skipped := executor.run(context.Background(), conditional, map[string]any{"skip": true}, "Shape")
	require.Empty(t, skipped.Errors)
	require.EqualValues(t, 1, calls.Load())
	visible := executor.run(context.Background(), conditional, map[string]any{"skip": false}, "Shape")
	require.NotEmpty(t, visible.Errors, "同一文档的当前变量形状必须重算成本")
	require.EqualValues(t, 1, calls.Load())
}

func TestResolverCostIsPartOfSnapshotContract(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(NewResolver[Root, struct{}, string]("Query", "weighted", "", func(context.Context, Root, struct{}) (string, error) {
		return "ok", nil
	}, Cost(9))))

	snapshot, err := executor.Snapshot()
	require.NoError(t, err)
	capability, ok := snapshot.Capability("Query.weighted")
	require.True(t, ok)
	require.Equal(t, uint32(9), capability.Cost)
}
