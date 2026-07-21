package gql

import (
	"context"
	"errors"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type mutationCountDialect struct {
	compiler.Dialect
	mutations int
}

func (my *mutationCountDialect) BuildMutation(ctx *compiler.Context, set ast.SelectionSet) error {
	my.mutations++
	return my.Dialect.BuildMutation(ctx, set)
}

func TestMutationRootFieldsExecuteSerially(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	dialect := &mutationCountDialect{Dialect: executor.compiler.dialect}
	executor.compiler.dialect = dialect

	operation := `mutation SerialMutation($email: String!) {
		created: createUser(input: { name: "Alice", email: $email }) { id }
		updated: updateUser(input: { name: "Bob" }, where: { email: { eq: $email } }) { name }
	}`
	for _, email := range []string{"serial-1@x.com", "serial-2@x.com"} {
		reply := executor.run(context.Background(), operation, map[string]any{"email": email}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		require.Equal(t, "Bob", reply.Data["updated"].(map[string]any)["name"])
	}
	require.Equal(t, 2, dialect.mutations, "非volatile子计划在热请求中不得重复编译")
}

func TestMutationRootPlansOnlyCacheNonVolatileFields(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	dialect := &mutationCountDialect{Dialect: executor.compiler.dialect}
	executor.compiler.dialect = dialect

	operation := `mutation VolatileMutation($input: UserCreateInput!, $email: String!) {
		createUser(input: $input) { id }
		updateUser(input: { name: "Updated" }, where: { email: { eq: $email } }) { name }
	}`
	for _, email := range []string{"volatile-1@x.com", "volatile-2@x.com"} {
		reply := executor.run(context.Background(), operation, map[string]any{
			"input": map[string]any{"name": "Volatile", "email": email},
			"email": email,
		}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
	}
	require.Equal(t, 3, dialect.mutations, "volatile字段必须重编译，普通字段应复用缓存")
}

func TestRootExecutionKeepsDefaultFastPaths(t *testing.T) {
	executor := &Executor{resolvers: map[string]Resolver{}}
	field := func(name string) *ast.Field { return &ast.Field{Name: name} }

	require.False(t, executor.needsRootExecution(&ast.OperationDefinition{
		Operation: ast.Query, SelectionSet: ast.SelectionSet{field("users"), field("posts")},
	}))
	require.False(t, executor.needsRootExecution(&ast.OperationDefinition{
		Operation: ast.Mutation, SelectionSet: ast.SelectionSet{field("createUser")},
	}))
	require.True(t, executor.needsRootExecution(&ast.OperationDefinition{
		Operation: ast.Mutation, SelectionSet: ast.SelectionSet{field("createUser"), field("createPost")},
	}))
}

func TestMutationFailureRollsBackEarlierRootFields(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	fail := NewResolver("Mutation", "failMutation", "测试事务回滚",
		func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
			if err := Tx(ctx).Exec(
				"INSERT INTO users(name, email) VALUES(?, ?)", "Resolver", "resolver-rollback@x.com",
			).Error; err != nil {
				return false, err
			}
			return false, errors.New("stop mutation")
		})
	require.NoError(t, executor.Register(fail))

	reply := executor.run(context.Background(), `mutation {
		createUser(input: { name: "Rollback", email: "rollback@x.com" }) { id }
		failMutation
	}`, nil, "")

	require.NotEmpty(t, reply.Errors)
	var count int64
	require.NoError(t, db.Table("users").Where("email IN ?", []string{
		"rollback@x.com", "resolver-rollback@x.com",
	}).Count(&count).Error)
	require.Zero(t, count, "失败必须同时回滚默认CRUD和Resolver写入")
}

func TestMutationResolverSharesTransactionWithDefaultFields(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	seed := NewResolver("Mutation", "seedUser", "测试Resolver事务连接",
		func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
			tx := Tx(ctx)
			if tx == nil {
				return false, errors.New("mutation transaction missing")
			}
			return true, tx.Exec(
				"INSERT INTO users(name, email) VALUES(?, ?)", "Resolver", "resolver-tx@x.com",
			).Error
		})
	require.NoError(t, executor.Register(seed))

	reply := executor.run(context.Background(), `mutation {
		seedUser
		updated: updateUser(input: { name: "Shared" }, where: { email: { eq: "resolver-tx@x.com" } }) { name }
	}`, nil, "")

	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "Shared", reply.Data["updated"].(map[string]any)["name"])
}

func TestMutationResolverWithoutDatabase(t *testing.T) {
	resolver := NewResolver("Mutation", "pingMutation", "纯计算mutation",
		func(context.Context, Root, struct{}) (string, error) { return "pong", nil })
	executor := &Executor{resolvers: map[string]Resolver{resolver.Name(): resolver}}
	operation := &ast.OperationDefinition{
		Operation: ast.Mutation,
		SelectionSet: ast.SelectionSet{&ast.Field{
			Name: "pingMutation", Alias: "pingMutation",
		}},
	}

	reply := executor.executeRootResolvers(context.Background(), &planEntry{operation: operation}, nil)
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "pong", reply.Data["pingMutation"])
}

func TestPersistedMutationKeepsSerialExecutionAfterPrewarm(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	ctx := context.Background()

	created := executor.run(ctx, `mutation {
		createUser(input: { name: "Before", email: "persisted@x.com" }) { id }
	}`, nil, "")
	require.Empty(t, created.Errors, "%v", created.Errors)
	id := created.Data["createUser"].(map[string]any)["id"]

	require.NoError(t, executor.loadDocument(`mutation OrderedMutation($id: ID!) {
		updateUser(input: { name: "After" }, id: $id) { id }
		createPost(input: { title: "Persisted", userId: $id }) { user { name } }
	}`))
	reply := executor.runOperation(ctx, "OrderedMutation", map[string]any{"id": id})

	require.Empty(t, reply.Errors, "%v", reply.Errors)
	post := reply.Data["createPost"].(map[string]any)
	require.Equal(t, "After", post["user"].(map[string]any)["name"])
}

func TestNestedMutationReusesOuterTransaction(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	nested := NewResolver("Mutation", "nestedMutation", "嵌套执行mutation",
		func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
			reply := executor.execute(ctx, `mutation {
				createUser(input: { name: "Nested", email: "nested@x.com" }) { id }
				updateUser(input: { name: "Inner" }, where: { email: { eq: "nested@x.com" } }) { id }
			}`, nil, "")
			if len(reply.Errors) > 0 {
				return false, reply.Errors
			}
			return true, nil
		})
	fail := NewResolver("Mutation", "failOuterMutation", "触发外层回滚",
		func(context.Context, Root, struct{}) (bool, error) {
			return false, errors.New("rollback outer mutation")
		})
	require.NoError(t, executor.Register(nested, fail))

	reply := executor.run(context.Background(), `mutation {
		nestedMutation
		failOuterMutation
	}`, nil, "")
	require.NotEmpty(t, reply.Errors)
	var count int64
	require.NoError(t, db.Table("users").Where("email = ?", "nested@x.com").Count(&count).Error)
	require.Zero(t, count, "嵌套mutation必须随外层事务一起回滚")
}
