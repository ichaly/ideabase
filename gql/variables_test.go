package gql

import (
	"context"
	stdjson "encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
)

func TestCoerceVariablesRejectsLooseBuiltins(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test.graphql", Input: `
		scalar Date
		enum Role { ADMIN USER }
		input Filter { limit: Int = 3, role: Role! }
		type Query { search(filter: Filter, n: Int, f: Float, b: Boolean, s: String, id: ID, role: Role): String }
		schema { query: Query }
	`})
	require.NoError(t, err)
	doc, errs := gqlparser.LoadQuery(schema, `query Q($n: Int, $f: Float, $b: Boolean, $s: String, $id: ID, $role: Role, $filter: Filter) { search(filter: $filter, n: $n, f: $f, b: $b, s: $s, id: $id, role: $role) }`)
	require.Empty(t, errs)
	op := doc.Operations.ForName("Q")

	for name, value := range map[string]any{
		"n-fraction":  1.5,
		"n-string":    "1",
		"n-wide":      int64(1 << 40),
		"f-string":    "1.5",
		"enum-int":    1,
		"enum-case":   "admin",
		"bool-string": "true",
		"string-int":  1,
		"id-fraction": 1.5,
	} {
		vars := map[string]any{}
		switch name {
		case "n-fraction", "n-string", "n-wide":
			vars["n"] = value
		case "f-string":
			vars["f"] = value
		case "enum-int", "enum-case":
			vars["role"] = value
		case "bool-string":
			vars["b"] = value
		case "string-int":
			vars["s"] = value
		case "id-fraction":
			vars["id"] = value
		}
		_, err := coerceVariables(schema, op, vars)
		require.Error(t, err, name)
	}
	for _, value := range []any{int64(-2147483648), int64(2147483647), stdjson.Number("2147483647")} {
		_, err := coerceVariables(schema, op, map[string]any{"n": value, "f": int64(1), "role": "ADMIN"})
		require.NoError(t, err)
	}
}

func TestHTTPVariablesPreserveJSONNumberKind(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	type args struct {
		N int `json:"n"`
	}
	require.NoError(t, executor.Register(NewResolver[Root, args, int]("Query", "echoHTTP", "", func(_ context.Context, _ Root, in args) (int, error) {
		return in.N, nil
	})))
	app := fiber.New()
	executor.Bind(app.Group(executor.Path()))
	request := httptest.NewRequest("POST", executor.Path(), strings.NewReader(
		`{"query":"query($n: Int!) { echoHTTP(n: $n) }","variables":{"n":1}}`,
	))
	request.Header.Set("Content-Type", "application/json")
	response, err := app.Test(request)
	require.NoError(t, err)
	require.Equal(t, 200, response.StatusCode)
	var reply gqlReply
	require.NoError(t, jsonNumeric.NewDecoder(response.Body).Decode(&reply))
	require.Empty(t, reply.Errors)
	require.Equal(t, stdjson.Number("1"), reply.Data["echoHTTP"])
}

func TestCoerceVariablesEnforcesOneOfAndNullDefaultSemantics(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test.graphql", Input: `
		input Choice @oneOf { id: ID, name: String }
		type Query { find(choice: Choice, limit: Int): String }
		schema { query: Query }
	`})
	require.NoError(t, err)
	doc, errs := gqlparser.LoadQuery(schema, `query Q($choice: Choice, $limit: Int = 7) { find(choice: $choice, limit: $limit) }`)
	require.Empty(t, errs)
	op := doc.Operations.ForName("Q")

	for name, choice := range map[string]any{
		"empty":    map[string]any{},
		"multiple": map[string]any{"id": "1", "name": "one"},
		"null":     map[string]any{"id": nil},
	} {
		_, err := coerceVariables(schema, op, map[string]any{"choice": choice})
		require.Error(t, err, name)
	}

	omitted, err := coerceVariables(schema, op, map[string]any{"choice": map[string]any{"id": "1"}})
	require.NoError(t, err)
	require.Equal(t, int64(7), omitted["limit"])
	explicitNull, err := coerceVariables(schema, op, map[string]any{"choice": map[string]any{"name": "one"}, "limit": nil})
	require.NoError(t, err)
	require.Contains(t, explicitNull, "limit")
	require.Nil(t, explicitNull["limit"])
}

func TestVariableValidationRunsBeforeCachedResolver(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	type args struct {
		N int `json:"n"`
	}
	calls := 0
	require.NoError(t, executor.Register(NewResolver[Root, args, int]("Query", "echo", "", func(_ context.Context, _ Root, in args) (int, error) {
		calls++
		return in.N, nil
	})))
	query := `query Echo($n: Int!) { echo(n: $n) }`

	reply := executor.run(context.Background(), query, map[string]any{"n": 1}, "Echo")
	require.Empty(t, reply.Errors)
	require.Equal(t, 1, calls)
	reply = executor.run(context.Background(), query, map[string]any{"n": "1"}, "Echo")
	require.NotEmpty(t, reply.Errors)
	require.Equal(t, 1, calls, "热计划的无效变量不得调用Resolver")
	require.Equal(t, ast.Path{ast.PathName("variable"), ast.PathName("n")}, reply.Errors[0].Path)
}

func TestColdFlightDoesNotPropagateAnotherRequestsVariableError(t *testing.T) {
	executor, cleanup := setupTestExecutor(t)
	defer cleanup()
	type args struct {
		N int `json:"n"`
	}
	require.NoError(t, executor.Register(NewResolver[Root, args, int]("Query", "echoFlight", "", func(_ context.Context, _ Root, in args) (int, error) {
		return in.N, nil
	})))
	query := `query Echo($n: Int!) { echoFlight(n: $n) }`
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _, _ = executor.flight.Do("Echo\x00"+query, func() (any, error) {
			close(started)
			<-release
			return nil, gqlerror.ErrorPathf(ast.Path{ast.PathName("variable"), ast.PathName("n")}, "foreign request error")
		})
	}()
	<-started
	time.AfterFunc(10*time.Millisecond, func() { close(release) })
	reply := executor.run(context.Background(), query, map[string]any{"n": 7}, "Echo")
	require.Empty(t, reply.Errors)
	require.EqualValues(t, 7, reply.Data["echoFlight"])
}

func TestCoerceVariablesRejectsUnknownInputFieldAndKeepsBigIntID(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test.graphql", Input: `
		input Filter { limit: Int = 3 }
		type Query { search(filter: Filter, id: ID): String }
		schema { query: Query }
	`})
	require.NoError(t, err)
	doc, errs := gqlparser.LoadQuery(schema, `query Q($filter: Filter, $id: ID) { search(filter: $filter, id: $id) }`)
	require.Empty(t, errs)
	op := doc.Operations.ForName("Q")

	_, err = coerceVariables(schema, op, map[string]any{"filter": map[string]any{"wat": 1}})
	require.Error(t, err)

	vars, err := coerceVariables(schema, op, map[string]any{"filter": map[string]any{}, "id": stdjson.Number("9007199254740993")})
	require.NoError(t, err)
	require.Equal(t, int64(3), vars["filter"].(map[string]any)["limit"])
	require.Equal(t, stdjson.Number("9007199254740993"), vars["id"])
	vars, err = coerceVariables(schema, op, map[string]any{"id": uint64(540800000000000001)})
	require.NoError(t, err)
	require.Equal(t, uint64(540800000000000001), vars["id"])
}

func TestCoerceVariablesPromotesSingletonListAndValidatesNestedJSONNumbers(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test.graphql", Input: `
		type Query { values(ids: [Int!]!, ratios: [Float!]!, matrix: [[Int!]!]!): String }
		schema { query: Query }
	`})
	require.NoError(t, err)
	doc, errs := gqlparser.LoadQuery(schema, `query Q($ids: [Int!]!, $ratios: [Float!]!, $matrix: [[Int!]!]!) { values(ids: $ids, ratios: $ratios, matrix: $matrix) }`)
	require.Empty(t, errs)
	vars, err := coerceVariables(schema, doc.Operations.ForName("Q"), map[string]any{
		"ids":    stdjson.Number("7"),
		"ratios": []any{stdjson.Number("1.25")},
		"matrix": []any{stdjson.Number("9")},
	})
	require.NoError(t, err)
	require.Equal(t, []any{int64(7)}, vars["ids"])
	require.Equal(t, []any{1.25}, vars["ratios"])
	require.Equal(t, []any{[]any{int64(9)}}, vars["matrix"])
}

func TestCoerceVariablesWithoutDefinitionsAllocatesNothing(t *testing.T) {
	op := &ast.OperationDefinition{}
	require.Zero(t, testing.AllocsPerRun(1000, func() {
		_, err := coerceVariables(nil, op, nil)
		if err != nil {
			t.Fatal(err)
		}
	}))
}

type suffixCodec struct{}

func (suffixCodec) Name() string         { return "Token" }
func (suffixCodec) Encode([]byte) []byte { return nil }
func (suffixCodec) Decode(value any) any { return value.(string) + "#" }

func TestVariableDefaultCodecRunsOnceOnColdAndWarmPlans(t *testing.T) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "test.graphql", Input: `
		scalar Token
		type Query { echoToken(value: Token): String }
		schema { query: Query }
	`})
	require.NoError(t, err)
	metadata := &Metadata{}
	metadata.setCodecs([]Codec{suffixCodec{}})
	executor := &Executor{schema: schema, metadata: metadata}
	query := `query($value: Token = "raw") { echoToken(value: $value) }`

	cold := map[string]any{}
	operation, err := executor.parse(query, "", cold)
	require.NoError(t, err)
	decodeVariables(schema, operation.VariableDefinitions, cold, metadata)
	require.Equal(t, "raw#", cold["value"])

	warm := map[string]any{}
	_, err = coerceVariables(schema, operation, warm)
	require.NoError(t, err)
	decodeVariables(schema, operation.VariableDefinitions, warm, metadata)
	require.Equal(t, "raw#", warm["value"], "缓存AST中的原始默认值不得被二次codec转换")
}
