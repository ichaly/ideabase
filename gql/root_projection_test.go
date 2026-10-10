package gql

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

type projectionChild struct {
	ID     int    `json:"id"`
	Name   string `json:"name"`
	Hidden string `json:"hidden"`
}

type projectionRoot struct {
	Child  projectionChild   `json:"child"`
	Items  []projectionChild `json:"items"`
	Hidden string            `json:"hidden"`
}

func TestRootResolverProjectsMapStructListAndTypename(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, executor.register(
		NewResolver[Root[struct{}, map[string]any], struct{}, map[string]any]("Query", "mapRoot", "", func(context.Context, Root[struct{}, map[string]any], struct{}) (map[string]any, error) {
			return map[string]any{
				"child":  map[string]any{"id": 1, "hidden": "secret"},
				"hidden": "root-secret",
			}, nil
		}, Result("ProjectionRoot"), Extra(`scalar JSON
type ProjectionRoot { child: ProjectionChild, items: [ProjectionChild!], hidden: String }
type ProjectionChild { id: Int, name: String, hidden: String }`)),
		NewResolver[Root[struct{}, projectionRoot], struct{}, projectionRoot]("Query", "structRoot", "", func(context.Context, Root[struct{}, projectionRoot], struct{}) (projectionRoot, error) {
			return projectionRoot{Child: projectionChild{ID: 2, Name: "struct", Hidden: "secret"}, Hidden: "root-secret"}, nil
		}, Result("ProjectionRoot")),
		NewResolver[Root[struct{}, []projectionChild], struct{}, []projectionChild]("Query", "listRoot", "", func(context.Context, Root[struct{}, []projectionChild], struct{}) ([]projectionChild, error) {
			return []projectionChild{{ID: 3, Name: "one", Hidden: "secret"}, {ID: 4, Name: "two", Hidden: "secret"}}, nil
		}, Result("[ProjectionChild]")),
		NewResolver[Root[struct{}, map[string]any], struct{}, map[string]any]("Query", "rawRoot", "", func(context.Context, Root[struct{}, map[string]any], struct{}) (map[string]any, error) {
			return map[string]any{"kept": "value", "extra": "preserved"}, nil
		}, Result("JSON")),
	))

	reply := executor.run(context.Background(), `{ mapRoot { alias: child { id name __typename } __typename } structRoot { child { name } } listRoot { id name } rawRoot }`, nil, "")
	require.Empty(t, reply.Errors)
	mapRoot := reply.Data["mapRoot"].(map[string]any)
	require.Equal(t, "ProjectionRoot", mapRoot["__typename"])
	child := mapRoot["alias"].(map[string]any)
	require.Equal(t, int64(1), child["id"])
	require.Equal(t, "ProjectionChild", child["__typename"])
	require.Nil(t, child["name"], "缺失的nullable字段仍须输出null")
	require.NotContains(t, mapRoot, "hidden")

	structRoot := reply.Data["structRoot"].(map[string]any)
	require.Equal(t, map[string]any{"name": "struct"}, structRoot["child"])

	listRoot := reply.Data["listRoot"].([]any)
	require.Equal(t, []any{map[string]any{"id": int64(3), "name": "one"}, map[string]any{"id": int64(4), "name": "two"}}, listRoot)
	require.Equal(t, map[string]any{"kept": "value", "extra": "preserved"}, reply.Data["rawRoot"])
}

func TestRootResolverEntityRequeryPreservesAliases(t *testing.T) {
	executor, db, cleanup := setupRootResolverExecutor(t)
	defer cleanup()
	require.NoError(t, executor.register(newSignUpResolver(db)))
	reply := executor.run(context.Background(), `mutation { created: signUp(name: "Projection", email: "projection@example.com") { alias: name id } }`, nil, "")
	require.Empty(t, reply.Errors)
	created := reply.Data["created"].(map[string]any)
	require.Equal(t, "Projection", created["alias"], "实体回查后的字段alias不得被二次投影丢失")
	require.NotNil(t, created["id"])
}
