package gql

import (
	"context"
	"fmt"
	validation "github.com/go-playground/validator/v10"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
	"testing"
)

type nativeUserInput struct {
	Name  string  `validate:"required,max=10" label:"姓名"`
	Email *string `validate:"omitempty,email"`
}
type nativeUserArgs struct {
	Input nativeUserInput
	ID    string
}
type nativeUserResult struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

func TestNativeResolverExtendsOnlyRegisteredCoordinate(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Args struct{ Input nativeUserInput }
	calls := 0
	require.NoError(t, e.register(NewResolver("Mutation", "createUser", "", func(ctx context.Context, root Root[Args, nativeUserResult], args Args) (nativeUserResult, error) {
		calls++
		args.Input.Name = " prefix "
		return root.Next(args)
	})))
	r := e.run(context.Background(), `mutation{createUser(input:{name:"a",email:"one@example.com"}){alias:name email}}`, nil, "")
	_, snapshotErr := e.Snapshot()
	require.NoError(t, snapshotErr)
	require.Empty(t, r.Errors)
	require.Equal(t, map[string]any{"alias": "prefix", "email": "one@example.com"}, r.Data["createUser"])
	// update 与 bulk create 不自动复用 createUser 的约束和处理器。
	r = e.run(context.Background(), `mutation{updateUser(id:1,input:{name:"long-unvalidated-name"}){name}}`, nil, "")
	require.Empty(t, r.Errors)
	r = e.run(context.Background(), `mutation{createUsers(input:[{name:"bulk",email:"bulk"}]){name}}`, nil, "")
	require.Empty(t, r.Errors)
	require.Equal(t, 1, calls)
	var name string
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE id=1`).Scan(&name))
	require.Equal(t, "long-unvalidated-name", name)
}

func TestNativeResolverPatchAndNextRollback(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES(1,'old','old@example.com')`)
	require.NoError(t, e.register(NewResolver("Mutation", "updateUser", "", func(ctx context.Context, root Root[nativeUserArgs, nativeUserResult], args nativeUserArgs) (nativeUserResult, error) {
		require.True(t, root.Has("input"))
		out, err := root.Next(args)
		if args.Input.Name == "rollback" {
			return out, fmt.Errorf("audit failed")
		}
		return out, err
	})))
	for _, input := range []string{`{name:" next "}`, `{email:"new@example.com"}`} {
		r := e.run(context.Background(), `mutation{updateUser(id:1,input:`+input+`){name email}}`, nil, "")
		require.Empty(t, r.Errors)
	}
	for _, input := range []string{`{name:""}`, `{name:null}`, `{email:null}`, `{email:"bad"}`, `{name:"rollback"}`} {
		r := e.run(context.Background(), `mutation{updateUser(id:1,input:`+input+`){name}}`, nil, "")
		require.NotEmpty(t, r.Errors)
	}
	var name string
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE id=1`).Scan(&name))
	require.Equal(t, "next", name)
	r := e.run(context.Background(), `mutation{updateUser(id:999,input:{name:"valid"}){name}}`, nil, "")
	require.Empty(t, r.Errors)
	require.Nil(t, r.Data["updateUser"])
}

func TestNativeResolverNextMisuseCannotBeSwallowed(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprint(existing), func(t *testing.T) {
			e, db, cleanup := newTestExecutor(t, nil)
			defer cleanup()
			type Args struct{ Input nativeUserInput }
			name := "createUser"
			if !existing {
				name = "customUser"
			}
			require.NoError(t, e.register(NewResolver("Mutation", name, "", func(ctx context.Context, root Root[Args, nativeUserResult], args Args) (nativeUserResult, error) {
				if !existing {
					x, _ := ExecutionFrom(ctx)
					_, err := x.Transaction().ExecContext(ctx, `INSERT INTO users(name,email) VALUES('side','side')`)
					require.NoError(t, err)
				}
				out, _ := root.Next(args)
				if existing {
					_, _ = root.Next(args)
				}
				return out, nil
			})))
			r := e.run(context.Background(), `mutation{`+name+`(input:{name:"one",email:"one@example.com"}){name}}`, nil, "")
			require.NotEmpty(t, r.Errors)
			require.Equal(t, "RESOLVER_NEXT_UNAVAILABLE", r.Errors[0].Extensions["code"])
			var count int
			require.NoError(t, testQueryRow(db, `SELECT count(*) FROM users`).Scan(&count))
			require.Zero(t, count)
		})
	}
}

func TestNativeResolverIncludeReauthorizesAndIsolatesVariables(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES(1,'old','old@example.com')`)
	require.NoError(t, e.register(NewResolver("Mutation", "updateUser", "", func(ctx context.Context, root Root[nativeUserArgs, nativeUserResult], args nativeUserArgs) (nativeUserResult, error) {
		email := "new@example.com"
		args.Input.Email = &email
		root.Include("input.email")
		return root.Next(args)
	})))
	installTestPolicy(t, e, OperationPolicyProvider(func(_ context.Context, r PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: r.Resource == "User", Fields: map[FieldUse][]string{FieldUseWrite: {"name"}, FieldUseSelect: {"*"}, FieldUseReturn: {"*"}, FieldUseFilter: {"id"}}}, nil
	}))
	id, err := NewIdentity(IdentityInput{Tenant: "test", Subject: "user"})
	require.NoError(t, err)
	vars := map[string]any{"_resolver_input": map[string]any{"name": "next"}}
	r := e.run(WithIdentity(context.Background(), id), `mutation($_resolver_input:UserUpdateInput!){updateUser(id:1,input:$_resolver_input){name}}`, vars, "")
	require.NotEmpty(t, r.Errors)
	require.Equal(t, map[string]any{"_resolver_input": map[string]any{"name": "next"}}, vars)
	var email string
	require.NoError(t, testQueryRow(db, `SELECT email FROM users WHERE id=1`).Scan(&email))
	require.Equal(t, "old@example.com", email)
}

func TestNativeResolverBulkAndSharedValidator(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct {
		Name  string `validate:"required,max=5"`
		Email string
	}
	type Args struct {
		Input []Input `validate:"dive"`
	}
	type Out struct {
		Name string `json:"name"`
	}
	require.NoError(t, e.register(NewResolver("Mutation", "createUsers", "", func(ctx context.Context, root Root[Args, []Out], args Args) ([]Out, error) { return root.Next(args) })))
	for _, names := range []string{`[{name:"one",email:"one"},{name:"two",email:"two"}]`, `[{name:"ok",email:"ok"},{name:"too-long",email:"bad"}]`} {
		r := e.run(context.Background(), `mutation{createUsers(input:`+names+`){name}}`, nil, "")
		if names[8:11] == "one" {
			require.Empty(t, r.Errors)
		} else {
			require.NotEmpty(t, r.Errors)
		}
	}
	var count int
	require.NoError(t, testQueryRow(db, `SELECT count(*) FROM users`).Scan(&count))
	require.Equal(t, 2, count)
}

func TestNativeResolverQueryAndReplacement(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(name,email) VALUES('name','one')`)
	type Args struct{ Limit int }
	type User struct {
		Name string `json:"name"`
	}
	type Result struct {
		Items []User `json:"items"`
	}
	require.NoError(t, e.register(NewResolver("Query", "users", "", func(ctx context.Context, root Root[Args, Result], args Args) (Result, error) {
		out, err := root.Next(args)
		if len(out.Items) > 0 {
			out.Items[0].Name = "changed"
		}
		return out, err
	})))
	r := e.run(context.Background(), `{users(limit:1){total items{alias:name email}}}`, nil, "")
	require.Empty(t, r.Errors)
	require.Equal(t, "changed", r.Data["users"].(map[string]any)["items"].([]any)[0].(map[string]any)["alias"])
	require.Equal(t, "one", r.Data["users"].(map[string]any)["items"].([]any)[0].(map[string]any)["email"])
	type Input struct{ Name string }
	type Create struct{ Input Input }
	require.NoError(t, e.register(NewResolver("Mutation", "createUser", "", func(context.Context, Root[Create, User], Create) (User, error) { return User{"replacement"}, nil })))
	r = e.run(context.Background(), `mutation{createUser(input:{name:"ignored",email:"ignored"}){name}}`, nil, "")
	require.Empty(t, r.Errors)
	require.Equal(t, "replacement", r.Data["createUser"].(map[string]any)["name"])
	var count int
	require.NoError(t, testQueryRow(db, `SELECT count(*) FROM users`).Scan(&count))
	require.Equal(t, 1, count)
}

func TestNativeResolverBuildChecks(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Wrong struct{ Input struct{ Name int } }
	r := NewResolver("Mutation", "createUser", "", func(context.Context, Root[Wrong, nativeUserResult], Wrong) (nativeUserResult, error) {
		return nativeUserResult{}, nil
	})
	require.Error(t, e.register(r))
	type Args struct{ Input nativeUserInput }
	r = NewResolver("Mutation", "createUser", "", func(context.Context, Root[Args, nativeUserResult], Args) (nativeUserResult, error) {
		return nativeUserResult{}, nil
	})
	require.NoError(t, e.register(r))
	require.ErrorContains(t, e.register(r), "已注册")
	r = NewResolver("Query", "wrongRoot", "", func(context.Context, Root[string, string], Args) (string, error) { return "", nil })
	require.ErrorContains(t, e.register(r), "Root泛型")
}

func TestNativeResolverPreservesCustomScalar(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct {
		Name mutationCode `validate:"required"`
	}
	type Args struct{ Input Input }
	type Out struct {
		Name string `json:"name"`
	}
	require.NoError(t, e.register(NewResolver("Mutation", "createUser", "", func(ctx context.Context, root Root[Args, Out], args Args) (Out, error) {
		require.Equal(t, mutationCode("MIXED"), args.Input.Name)
		return root.Next(args)
	})))
	r := e.run(context.Background(), `mutation{createUser(input:{name:"Mixed",email:"one"}){name}}`, nil, "")
	require.Empty(t, r.Errors)
	var name string
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE email='one'`).Scan(&name))
	require.Equal(t, "code:mixed", name)
}

func TestNativeResolverSharedValidator(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	v, err := std.NewValidator()
	require.NoError(t, err)
	e.validator = v
	require.NoError(t, v.RegisterValidation("allowed", func(field validation.FieldLevel) bool { return field.Field().String() != "blocked" }, "{field}不可使用"))
	// 同一个Validator既传入Next校验，也可在普通Resolver中使用结构体规则。
	type Args struct {
		Name string `validate:"required,allowed" label:"姓名"`
	}
	resolver := NewResolver("Mutation", "customName", "", func(_ context.Context, _ Root[Args, string], args Args) (string, error) { return args.Name, nil })
	require.NoError(t, e.register(resolver))
	r := e.run(context.Background(), `mutation{customName(name:"")}`, nil, "")
	require.NotEmpty(t, r.Errors)
	require.Equal(t, "VALIDATION_ERROR", r.Errors[0].Extensions["code"])
	type NativeArgs struct{ Input Args }
	require.NoError(t, e.register(NewResolver("Mutation", "createUser", "", func(ctx context.Context, root Root[NativeArgs, nativeUserResult], args NativeArgs) (nativeUserResult, error) {
		return root.Next(args)
	})))
	r = e.run(context.Background(), `mutation{createUser(input:{name:"blocked",email:"one"}){name}}`, nil, "")
	require.NotEmpty(t, r.Errors)
	require.Equal(t, "VALIDATION_ERROR", r.Errors[0].Extensions["code"])
}

func TestNativeResolverExactNumbersDefaultsAndNull(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, `CREATE TABLE meters(id serial PRIMARY KEY, amount bigint NOT NULL DEFAULT 17, enabled boolean NOT NULL DEFAULT true, name text DEFAULT 'database')`)
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	e, err := newEngineForTest(db, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	defer e.Close()
	type Input struct {
		Amount  int64 `validate:"min=0"`
		Enabled bool
		Name    *string
	}
	type Args struct{ Input Input }
	type Out struct {
		ID string `json:"id"`
	}
	save := func(ctx context.Context, root Root[Args, Out], args Args) (Out, error) { return root.Next(args) }
	require.NoError(t, e.register(NewResolver("Mutation", "createMeter", "", save), NewResolver("Mutation", "updateMeter", "", save)))
	for _, input := range []string{`{name:"first"}`, `{amount:"9007199254740993",enabled:false}`} {
		r := e.run(context.Background(), `mutation{createMeter(input:`+input+`){id}}`, nil, "")
		require.Empty(t, r.Errors)
	}
	var amount int64
	var enabled bool
	var name string
	require.NoError(t, testQueryRow(db, `SELECT amount,enabled FROM meters WHERE id=1`).Scan(&amount, &enabled))
	require.EqualValues(t, 17, amount)
	require.True(t, enabled)
	require.NoError(t, testQueryRow(db, `SELECT amount,name,enabled FROM meters WHERE id=2`).Scan(&amount, &name, &enabled))
	require.EqualValues(t, 9007199254740993, amount)
	require.Equal(t, "database", name)
	require.False(t, enabled)
	for _, input := range []string{`{amount:"9223372036854775808"}`, `{amount:"-1"}`, `{enabled:null}`} {
		r := e.run(context.Background(), `mutation{updateMeter(id:2,input:`+input+`){id}}`, nil, "")
		require.NotEmpty(t, r.Errors)
	}
	r := e.run(context.Background(), `mutation{updateMeter(id:2,input:{amount:"0",enabled:false,name:null}){amount enabled name}}`, nil, "")
	require.Empty(t, r.Errors)
	require.Nil(t, r.Data["updateMeter"].(map[string]any)["name"])
	require.NoError(t, testQueryRow(db, `SELECT amount FROM meters WHERE id=2`).Scan(&amount))
	require.Zero(t, amount)
}

func TestNativeResolverRejectsNestedBypass(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct{ Title string }
	type Args struct{ Input Input }
	type Out struct {
		ID string `json:"id"`
	}
	require.NoError(t, e.register(NewResolver("Mutation", "createPost", "", func(ctx context.Context, root Root[Args, Out], args Args) (Out, error) { return root.Next(args) })))
	r := e.run(context.Background(), `mutation{createUser(input:{name:"owner",email:"owner",posts:{create:[{title:"nested"}]}}){id}}`, nil, "")
	require.NotEmpty(t, r.Errors)
	// 无关直接操作仍不执行createPost扩展。
	require.Nil(t, e.native["Mutation.updatePost"])
}

func TestNativeResolverCannotBypassRowPolicy(t *testing.T) {
	for _, next := range []bool{false, true} {
		t.Run(fmt.Sprint(next), func(t *testing.T) {
			e, db, cleanup := newTestExecutor(t, nil)
			defer cleanup()
			type Args struct{ Input nativeUserInput }
			require.NoError(t, e.register(NewResolver("Mutation", "createUser", "", func(ctx context.Context, root Root[Args, nativeUserResult], args Args) (nativeUserResult, error) {
				if next {
					return root.Next(args)
				}
				return nativeUserResult{Name: "fake"}, nil
			})))
			installTestPolicy(t, e, OperationPolicyProvider(func(_ context.Context, r PolicyRequest) (PolicyDecision, error) {
				return PolicyDecision{Allow: r.Resource == "User", Fields: map[FieldUse][]string{FieldUseWrite: {"*"}, FieldUseSelect: {"*"}, FieldUseReturn: {"*"}, FieldUseFilter: {"*"}}, Check: []Predicate{{Column: "name", Operator: "eq", Value: "client"}}}, nil
			}))
			id, err := NewIdentity(IdentityInput{Tenant: "test", Subject: "user"})
			require.NoError(t, err)
			r := e.run(WithIdentity(context.Background(), id), `mutation{createUser(input:{name:"client",email:"one@example.com"}){name}}`, nil, "")
			if next {
				require.Empty(t, r.Errors)
				require.Equal(t, "client", r.Data["createUser"].(map[string]any)["name"])
			} else {
				require.NotEmpty(t, r.Errors)
				require.Equal(t, "POLICY_HANDLER_REQUIRED", r.Errors[0].Extensions["code"])
			}
			var count int
			require.NoError(t, testQueryRow(db, `SELECT count(*) FROM users`).Scan(&count))
			if next {
				require.Equal(t, 1, count)
			} else {
				require.Zero(t, count)
			}
		})
	}
}

func TestNativeResolverIncludeAndRetainedNext(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct {
		Name  string `validate:"required"`
		Email string
	}
	type Args struct {
		Input []Input `validate:"dive"`
	}
	var saved Root[Args, []nativeUserResult]
	require.NoError(t, e.register(NewResolver("Mutation", "createUsers", "", func(ctx context.Context, root Root[Args, []nativeUserResult], args Args) ([]nativeUserResult, error) {
		saved = root
		require.True(t, root.Has("input[0].name"))
		require.False(t, root.Has("input[0].email"))
		args.Input[0].Email = "included"
		root.Include("input[0].email")
		require.True(t, root.Has("input[0].email"))
		return root.Next(args)
	})))
	// 创建输入本身要求email；在SDL允许省略后验证Include的逐行行为。
	field := e.schema.Types["UserCreateRowInput"].Fields.ForName("email")
	field.Type.NonNull = false
	e.source = formatSchema(e.schema)
	require.NoError(t, e.rebuild())
	r := e.run(context.Background(), `mutation{createUsers(input:[{name:"one"}]){name}}`, nil, "")
	require.Empty(t, r.Errors)
	_, err := saved.Next(Args{})
	require.ErrorContains(t, err, "执行已结束")
	var email string
	require.NoError(t, testQueryRow(db, `SELECT email FROM users`).Scan(&email))
	require.Equal(t, "included", email)
}
