package gql

import (
	"context"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestMutationHookTransactionAndProjection(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	called := 0
	require.NoError(t, e.installMutations([]Mutation{{
		Class: "User", Returning: []string{"id"}, Validate: map[string]string{"name": "required,max=10"},
		Before: func(ctx context.Context, request MutationRequest) (MutationAfter, error) {
			x, ok := ExecutionFrom(ctx)
			require.True(t, ok)
			require.NotNil(t, x.Transaction())
			return func(rows []map[string]any) error {
				called++
				require.Len(t, rows, 1)
				require.NotNil(t, rows[0]["id"])
				if request.Arguments["input"].(map[string]any)["name"] == "rollback" {
					return fmt.Errorf("audit rejected")
				}
				return nil
			}, nil
		},
	}}))
	for _, name := range []string{"ok", "rollback", "too-long-name"} {
		r := e.run(context.Background(), `mutation($name:String!){ saved:createUser(input:{name:$name,email:$name}){name} }`, map[string]any{"name": name}, "")
		if name == "ok" {
			require.Empty(t, r.Errors)
			require.Equal(t, map[string]any{"name": "ok"}, r.Data["saved"])
		} else {
			require.NotEmpty(t, r.Errors)
			if name == "too-long-name" {
				require.Equal(t, "VALIDATION_ERROR", r.Errors[0].Extensions["code"])
			}
		}
	}
	require.Equal(t, 2, called)
	var count int
	require.NoError(t, db.Pool().QueryRowContext(context.Background(), `SELECT count(*) FROM users`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestMutationHookReauthorizesChangedInput(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES (1,'name','original')`)
	called := false
	require.NoError(t, e.installMutations([]Mutation{{Class: "User", Before: func(_ context.Context, r MutationRequest) (MutationAfter, error) {
		called = true
		r.Arguments["input"].(map[string]any)["email"] = "forbidden"
		return nil, nil
	}}}))
	installTestPolicy(t, e, OperationPolicyProvider(func(_ context.Context, request PolicyRequest) (PolicyDecision, error) {
		if request.Resource != "User" {
			return PolicyDecision{}, nil
		}
		return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{FieldUseWrite: {"name"}, FieldUseSelect: {"*"}, FieldUseReturn: {"*"}, FieldUseFilter: {"id"}}}, nil
	}))
	id, err := NewIdentity(IdentityInput{Tenant: "t", Subject: "u"})
	require.NoError(t, err)
	r := e.run(WithIdentity(context.Background(), id), `mutation{updateUser(id:"1",input:{name:"changed"}){name}}`, nil, "")
	require.True(t, called, "%v", r.Errors)
	require.NotEmpty(t, r.Errors)
	var email string
	require.NoError(t, testQueryRow(db, `SELECT email FROM users WHERE id=1`).Scan(&email))
	require.Equal(t, "original", email)
}

func TestMutationHookBulkAndNested(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	var sizes []int
	require.NoError(t, e.installMutations([]Mutation{{Class: "User", Returning: []string{"id"}, Validate: map[string]string{"name": "required,max=5"}, Before: func(context.Context, MutationRequest) (MutationAfter, error) {
		return func(rows []map[string]any) error { sizes = append(sizes, len(rows)); return nil }, nil
	}}, {Class: "Post", Validate: map[string]string{"title": "required"}}}))
	for _, query := range []string{
		`mutation{createUsers(input:[{name:"one",email:"one"},{name:"two",email:"two"}]){name}}`,
		`mutation{updateUsers(where:{name:{eq:"one"}},input:{name:"three"}){affectedCount}}`,
		`mutation{upsertUsers(on:UNIQUE_1,input:[{name:"four",email:"two"}]){name}}`,
		`mutation{deleteUsers(where:{name:{eq:"three"}}){affectedCount}}`,
	} {
		r := e.run(context.Background(), query, nil, "")
		require.Empty(t, r.Errors, "%s: %v", query, r.Errors)
		require.NotContains(t, fmt.Sprint(r.Data), "_mutation")
	}
	require.Equal(t, []int{2, 1, 1, 1}, sizes)
	for _, query := range []string{
		`mutation{createUsers(input:[{name:"good",email:"good"},{name:"too-long",email:"bad"}]){id}}`,
		`mutation{createUser(input:{name:"owner",email:"owner",posts:{create:[{title:"nested"}]}}){id}}`,
		`mutation{deleteUser(where:{name:{eq:"four"}})}`,
	} {
		r := e.run(context.Background(), query, nil, "")
		require.NotEmpty(t, r.Errors)
	}
	r := e.run(context.Background(), `{users{total}}`, nil, "")
	require.Empty(t, r.Errors)
	require.EqualValues(t, 1, r.Data["users"].(map[string]any)["total"])
}

func TestMutationHookTransformsPrivateInput(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, e.installMutations([]Mutation{{Class: "User", Returning: []string{"id"}, Arguments: map[string]string{"prefix": "String"}, Before: func(_ context.Context, r MutationRequest) (MutationAfter, error) {
		input := r.Arguments["input"].(map[string]any)
		input["name"] = fmt.Sprint(r.Arguments["prefix"]) + fmt.Sprint(input["name"])
		return nil, nil
	}}}))
	variables := map[string]any{"input": map[string]any{"name": "name", "email": "private"}}
	r := e.run(context.Background(), `mutation($input:UserCreateInput!){createUser(prefix:"prefix-",input:$input){name _mutation:name}}`, variables, "")
	require.Empty(t, r.Errors)
	require.Equal(t, "prefix-name", r.Data["createUser"].(map[string]any)["name"])
	require.Equal(t, "prefix-name", r.Data["createUser"].(map[string]any)["_mutation"])
	require.NotContains(t, r.Data["createUser"].(map[string]any), "id")
	require.Equal(t, "name", variables["input"].(map[string]any)["name"])
}

func TestMutationHookKeepsUnrelatedFastPath(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.NoError(t, e.installMutations([]Mutation{{Class: "Post"}}))
	require.False(t, e.needsRootExecution(&ast.OperationDefinition{Operation: ast.Mutation, SelectionSet: ast.SelectionSet{&ast.Field{Name: "deleteTag"}}}))
	r := e.run(context.Background(), `mutation{createUser(input:{name:"owner",email:"owner",posts:{create:[{title:"nested"}]}}){id}}`, nil, "")
	require.NotEmpty(t, r.Errors)
	require.Contains(t, r.Errors[0].Message, "Post")
}

func TestMutationHookRejectsInvalidRulesAtBuild(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	require.Error(t, e.installMutations([]Mutation{{Class: "User", Validate: map[string]string{"name": "no_such_validation"}}}))
}

func TestMutationHookValidatesMappedNumbers(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES (1,'one','one'),(2,'two','two')`)
	require.NoError(t, e.installMutations([]Mutation{{Class: "Post", Validate: map[string]string{"userId": "min=1,max=1"}}}))
	for _, id := range []int{1, 2} {
		r := e.run(context.Background(), fmt.Sprintf(`mutation{createPost(input:{title:"post",userId:%d}){id}}`, id), nil, "")
		if id == 1 {
			require.Empty(t, r.Errors)
		} else {
			require.NotEmpty(t, r.Errors)
		}
	}
}
