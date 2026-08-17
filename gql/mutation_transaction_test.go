package gql

import (
	"context"
	"errors"
	"testing"

	"github.com/ichaly/ideabase/gql/compiler"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
	"gorm.io/gorm"
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
		updated: updateUsers(input: { name: "Bob" }, where: { email: { eq: $email } }) { returning { name } }
	}`
	for _, email := range []string{"serial-1@x.com", "serial-2@x.com"} {
		reply := executor.run(context.Background(), operation, map[string]any{"email": email}, "")
		require.Empty(t, reply.Errors, "%v", reply.Errors)
		require.Equal(t, "Bob", reply.Data["updated"].(map[string]any)["returning"].([]any)[0].(map[string]any)["name"])
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
		updateUsers(input: { name: "Updated" }, where: { email: { eq: $email } }) { affectedCount }
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
	executor := &Engine{resolvers: map[string]Resolver{}}
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
	require.True(t, executor.needsRootExecution(&ast.OperationDefinition{
		Operation: ast.Mutation, SelectionSet: ast.SelectionSet{field("__typename")},
	}))
}

func TestMutationTypenameDoesNotRequireSQLMutation(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	reply := executor.run(context.Background(), `mutation { root: __typename }`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "Mutation", reply.Data["root"])
}

func TestMutationFailureRollsBackEarlierRootFields(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	fail := NewResolver("Mutation", "failMutation", "测试事务回滚",
		func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
			if err := transactionFromContext(ctx).Exec(
				"INSERT INTO users(name, email) VALUES(?, ?)", "Resolver", "resolver-rollback@x.com",
			).Error; err != nil {
				return false, err
			}
			return false, errors.New("stop mutation")
		})
	require.NoError(t, executor.register(fail))

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

func TestRootTransactionTreatsRollbackConnectionLossAsIndeterminate(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	sentinel := errors.New("mutation failed")
	err, rolledBack := runRootTransaction(context.Background(), db, nil, func(tx *gorm.DB) error {
		var pid int
		require.NoError(t, tx.Raw(`SELECT pg_backend_pid()`).Scan(&pid).Error)
		require.NoError(t, db.Exec(`SELECT pg_terminate_backend(?)`, pid).Error)
		return sentinel
	})
	require.Error(t, err)
	require.False(t, rolledBack)
}

func TestMutationResolverSharesTransactionWithDefaultFields(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	seed := NewResolver("Mutation", "seedUser", "测试Resolver事务连接",
		func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
			tx := transactionFromContext(ctx)
			if tx == nil {
				return false, errors.New("mutation transaction missing")
			}
			return true, tx.Exec(
				"INSERT INTO users(name, email) VALUES(?, ?)", "Resolver", "resolver-tx@x.com",
			).Error
		})
	require.NoError(t, executor.register(seed))

	reply := executor.run(context.Background(), `mutation {
		seedUser
		updated: updateUsers(input: { name: "Shared" }, where: { email: { eq: "resolver-tx@x.com" } }) { returning { name } }
	}`, nil, "")

	require.Empty(t, reply.Errors, "%v", reply.Errors)
	require.Equal(t, "Shared", reply.Data["updated"].(map[string]any)["returning"].([]any)[0].(map[string]any)["name"])
}

func TestMutationResolverWithoutDatabase(t *testing.T) {
	resolver := NewResolver("Mutation", "pingMutation", "纯计算mutation",
		func(context.Context, Root, struct{}) (string, error) { return "pong", nil })
	executor := &Engine{resolvers: map[string]Resolver{resolver.Name(): resolver}}
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

func TestPersistedRootPlanPrewarmKeepsGovernanceFlags(t *testing.T) {
	executor, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	// 多根 mutation 会走 rootResolver 预热路径；User 的治理配置必须同步写入
	// 持久化缓存条目，否则执行期会直接跳过 authorize。
	executor.metadata.Nodes["User"].Permissions = map[string]*protocol.Permission{
		"writer": {Insert: []string{"name", "email"}},
	}
	resolver := NewResolver("Mutation", "pingMutation", "测试持久化根计划治理标记",
		func(context.Context, Root, struct{}) (string, error) { return "pong", nil })
	require.NoError(t, executor.register(resolver))

	content := `mutation PersistedGoverned($email: String!) {
		createUser(input: { name: "Alice", email: $email }) { id }
		pingMutation
	}`
	require.NoError(t, executor.loadDocument(content))

	entry, ok := executor.cache.Get(planKeyFor("PersistedGoverned", content, "", ""))
	require.True(t, ok)
	require.True(t, entry.rootResolver)
}

func TestNestedMutationReusesOuterTransaction(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	nested := NewResolver("Mutation", "nestedMutation", "嵌套执行mutation",
		func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
			reply := executor.execute(ctx, `mutation {
				createUser(input: { name: "Nested", email: "nested@x.com" }) { id }
				updateUsers(input: { name: "Inner" }, where: { email: { eq: "nested@x.com" } }) { affectedCount }
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
	require.NoError(t, executor.register(nested, fail))

	reply := executor.run(context.Background(), `mutation {
		nestedMutation
		failOuterMutation
	}`, nil, "")
	require.NotEmpty(t, reply.Errors)
	var count int64
	require.NoError(t, db.Table("users").Where("email = ?", "nested@x.com").Count(&count).Error)
	require.Zero(t, count, "嵌套mutation必须随外层事务一起回滚")
}

func TestCaughtNestedMutationFailureRollsBackOnlyItsSavepoint(t *testing.T) {
	executor, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()

	fail := NewResolver("Mutation", "failNestedMutation", "", func(context.Context, Root, struct{}) (bool, error) {
		return false, errors.New("nested secret failure")
	})
	catch := NewResolver("Mutation", "catchNestedMutation", "", func(ctx context.Context, _ Root, _ struct{}) (bool, error) {
		reply := executor.execute(ctx, `mutation {
			createUser(input: { name: "Inner", email: "inner-savepoint@x.com" }) { id }
			failNestedMutation
		}`, nil, "")
		return len(reply.Errors) > 0, nil
	})
	require.NoError(t, executor.register(fail, catch))

	reply := executor.run(context.Background(), `mutation {
		catchNestedMutation
		createUser(input: { name: "Outer", email: "outer-savepoint@x.com" }) { id }
	}`, nil, "")
	require.Empty(t, reply.Errors, "%v", reply.Errors)
	var inner, outer int64
	require.NoError(t, db.Table("users").Where("email = ?", "inner-savepoint@x.com").Count(&inner).Error)
	require.NoError(t, db.Table("users").Where("email = ?", "outer-savepoint@x.com").Count(&outer).Error)
	require.Zero(t, inner)
	require.EqualValues(t, 1, outer)
}
