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
	type Input struct {
		Name string `validate:"required,max=10"`
	}
	m := NewMutation[Input]("User", nil)
	m.Returning = []string{"id"}
	m.After = func(ctx context.Context, request MutationRequest, rows []map[string]any) error {
		x, ok := ExecutionFrom(ctx)
		require.True(t, ok)
		require.NotNil(t, x.Transaction())
		called++
		require.Len(t, rows, 1)
		require.NotNil(t, rows[0]["id"])
		if request.Arguments["input"].(map[string]any)["name"] == "rollback" {
			return fmt.Errorf("audit rejected")
		}
		return nil
	}
	require.NoError(t, e.installMutations([]Mutation{m}))
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

func TestMutationHookBulkAndNested(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	var sizes []int
	type UserInput struct {
		Name string `validate:"required,max=5"`
	}
	type PostInput struct {
		Title string `validate:"required"`
	}
	m := NewMutation[UserInput]("User", nil)
	m.Returning = []string{"id"}
	m.After = func(_ context.Context, _ MutationRequest, rows []map[string]any) error {
		sizes = append(sizes, len(rows))
		return nil
	}
	require.NoError(t, e.installMutations([]Mutation{m, NewMutation[PostInput]("Post", nil)}))
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
	type Input struct{ Name string }
	m := NewMutation[Input]("User", func(_ context.Context, change *Change[Input]) error {
		change.Input.Name = fmt.Sprint(change.Arguments["prefix"]) + change.Input.Name
		return nil
	})
	m.Arguments, m.Returning = map[string]string{"prefix": "String"}, []string{"id"}
	m.After = func(_ context.Context, _ MutationRequest, rows []map[string]any) error {
		require.Len(t, rows, 1)
		require.NotNil(t, rows[0]["id"])
		return nil
	}
	require.NoError(t, e.installMutations([]Mutation{m}))
	variables := map[string]any{"_mutation_input": map[string]any{"name": "name", "email": "private"}}
	r := e.run(context.Background(), `mutation($_mutation_input:UserCreateInput!){createUser(prefix:"prefix-",input:$_mutation_input){name _mutation:name}}`, variables, "")
	require.Empty(t, r.Errors)
	require.Equal(t, "prefix-name", r.Data["createUser"].(map[string]any)["name"])
	require.Equal(t, "prefix-name", r.Data["createUser"].(map[string]any)["_mutation"])
	require.NotContains(t, r.Data["createUser"].(map[string]any), "id")
	require.Equal(t, "name", variables["_mutation_input"].(map[string]any)["name"])
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

func TestMutationHookRelationReachability(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	for _, class := range []string{"Tag", "PostTag"} {
		t.Run(class, func(t *testing.T) {
			require.NoError(t, e.installMutations([]Mutation{{Class: class}}))
			// 用户、文章、标签和评论形成循环；中间表只能通过关系间接到达。
			for _, name := range []string{"User", "Post", "Tag", "Comment"} {
				require.True(t, e.mutationRoots["Mutation.create"+name], name)
				require.Equal(t, name == class, e.mutationRoots["Mutation.delete"+name], name)
			}
			r := e.run(context.Background(), `mutation{createPost(input:{title:"nested",userId:1,tags:{connect:[1]}}){id}}`, nil, "")
			require.NotEmpty(t, r.Errors)
			require.Equal(t, "MUTATION_HOOK_REQUIRED", r.Errors[0].Extensions["code"], "%v", r.Errors)
		})
	}
}

func TestMutationHookRejectsInvalidRules(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct {
		Name string `validate:"no_such_validation"`
	}
	require.NoError(t, e.installMutations([]Mutation{NewMutation[Input]("User", nil)}))
	r := e.run(context.Background(), `mutation{createUser(input:{name:"one",email:"one"}){id}}`, nil, "")
	require.NotEmpty(t, r.Errors)
	var count int
	require.NoError(t, testQueryRow(db, `SELECT count(*) FROM users`).Scan(&count))
	require.Zero(t, count)
}

func TestMutationHookValidatesMappedNumbers(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES (1,'one','one'),(2,'two','two')`)
	type Input struct {
		UserID int64 `validate:"min=1,max=1"`
	}
	require.NoError(t, e.installMutations([]Mutation{NewMutation[Input]("Post", nil)}))
	for _, id := range []int{1, 2} {
		r := e.run(context.Background(), fmt.Sprintf(`mutation{createPost(input:{title:"post",userId:%d}){id}}`, id), nil, "")
		if id == 1 {
			require.Empty(t, r.Errors)
		} else {
			require.NotEmpty(t, r.Errors)
		}
	}
}
