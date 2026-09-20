package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

	validation "github.com/go-playground/validator/v10"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

type mutationCode string

type mutationText string

func (my *mutationText) UnmarshalText(data []byte) error {
	*my = mutationText(strings.ToUpper(string(data)))
	return nil
}
func (my mutationText) MarshalText() ([]byte, error) { return []byte(strings.ToLower(string(my))), nil }

type mutationAmount int

func (my mutationAmount) MarshalJSON() ([]byte, error) { return []byte(fmt.Sprint(int(my) + 1)), nil }

type mutationInt int64

func (my mutationInt) String() string { return "display only" }

func (my *mutationCode) UnmarshalJSON(data []byte) error {
	var value string
	if err := stdjson.Unmarshal(data, &value); err != nil {
		return err
	}
	*my = mutationCode(strings.ToUpper(value))
	return nil
}

func (my mutationCode) MarshalJSON() ([]byte, error) {
	return stdjson.Marshal("code:" + strings.ToLower(string(my)))
}

func TestTypedMutationPreservesCustomScalar(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct {
		Name mutationCode `validate:"required"`
	}
	mutation := NewMutation[Input]("User", func(_ context.Context, change *Change[Input]) error {
		require.Equal(t, mutationCode("MIXED"), change.Input.Name)
		return nil
	})
	require.NoError(t, e.installMutations([]Mutation{mutation}))
	r := e.run(context.Background(), `mutation{createUser(input:{name:"Mixed",email:"one"}){name}}`, nil, "")
	require.Empty(t, r.Errors)
	var name string
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE email='one'`).Scan(&name))
	require.Equal(t, "code:mixed", name)
}

func TestTypedMutationDoesNotWriteRootArgumentCopies(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES(1,'one','one'),(2,'two','two')`)
	type Input struct {
		Name string `validate:"required"`
	}
	mutation := NewMutation[Input]("User", func(_ context.Context, change *Change[Input]) error {
		require.NotContains(t, change.Arguments, "input")
		change.Arguments["id"] = "2"
		change.Arguments["input"] = map[string]any{"name": "", "email": "bypass"}
		return nil
	})
	require.NoError(t, e.installMutations([]Mutation{mutation}))
	r := e.run(context.Background(), `mutation{updateUser(id:1,input:{name:"changed"}){name}}`, nil, "")
	require.Empty(t, r.Errors)
	var name string
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE id=1`).Scan(&name))
	require.Equal(t, "changed", name)
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE id=2`).Scan(&name))
	require.Equal(t, "two", name)
}

func TestMutationScalarBindingWidthsAndAliases(t *testing.T) {
	type text string
	type flag bool
	for _, tc := range []struct {
		target  any
		raw     any
		want    any
		invalid bool
	}{
		{int8(0), int64(127), int64(127), false},
		{int8(0), int64(128), nil, true},
		{uint8(0), "255", uint64(255), false},
		{uint8(0), "256", nil, true},
		{uint64(0), "18446744073709551615", uint64(math.MaxUint64), false},
		{uint64(0), "-1", nil, true},
		{int64(0), "-9223372036854775808", int64(math.MinInt64), false},
		{float32(0), 1.25, float32(1.25), false},
		{float32(0), math.MaxFloat64, nil, true},
		{text(""), "value", "value", false},
		{flag(false), true, true, false},
		{mutationText(""), "Mixed", "mixed", false},
		{mutationAmount(0), "41", stdjson.Number("42"), false},
		{mutationInt(0), "42", int64(42), false},
	} {
		t.Run(fmt.Sprintf("%T/%v", tc.target, tc.raw), func(t *testing.T) {
			field := newMutationInputField(reflect.StructField{Name: "Value", Type: reflect.TypeOf(tc.target), Index: []int{0}}, "value", false)
			value := reflect.New(reflect.TypeOf(tc.target)).Elem()
			err := field.decode(value, tc.raw)
			if tc.invalid {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			actual, err := field.encode(value)
			require.NoError(t, err)
			require.Equal(t, tc.want, actual)
		})
	}
	field := newMutationInputField(reflect.StructField{Name: "Value", Type: reflect.TypeFor[float64](), Index: []int{0}}, "value", false)
	for _, value := range []float64{math.NaN(), math.Inf(1)} {
		_, err := field.encode(reflect.ValueOf(value))
		require.Error(t, err)
	}
}

func TestTypedMutationPreservesPatchAndNull(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	type Input struct {
		Name  string  `validate:"required,max=10" label:"姓名"`
		Email *string `validate:"omitempty,email"`
	}
	mutation := NewMutation[Input]("User", func(_ context.Context, change *Change[Input]) error {
		if change.Has("name") {
			change.Input.Name = strings.TrimSpace(change.Input.Name)
		}
		return nil
	})
	require.NoError(t, e.installMutations([]Mutation{mutation}))
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES (1,'old','old@example.com')`)
	r := e.run(context.Background(), `mutation { updateUser(id:1,input:{name:" next "}){name email} }`, nil, "")
	require.Empty(t, r.Errors, "%v", r.Errors)
	var name string
	require.NoError(t, testQueryRow(db, `SELECT name FROM users WHERE id=1`).Scan(&name))
	require.Equal(t, "next", name)
	for _, input := range []string{`{name:null}`, `{email:null}`, `{name:""}`, `{email:"bad"}`} {
		r := e.run(context.Background(), `mutation { updateUser(id:1,input:`+input+`){id} }`, nil, "")
		require.NotEmpty(t, r.Errors)
	}
	r = e.run(context.Background(), `mutation { createUser(input:{email:"new@example.com"}){id} }`, nil, "")
	require.NotEmpty(t, r.Errors)
	post := NewMutation[struct{ Content *string }]("Post", nil)
	require.NoError(t, e.installMutations([]Mutation{mutation, post}))
	testExec(t, db, `INSERT INTO posts(id,title,content,user_id) VALUES (1,'post','old',1)`)
	r = e.run(context.Background(), `mutation { updatePost(id:1,input:{content:null}){content} }`, nil, "")
	require.Empty(t, r.Errors)
	require.Nil(t, r.Data["updatePost"].(map[string]any)["content"])
}

func TestTypedMutationSharedValidatorAndBulkRollback(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	v, err := std.NewValidator()
	require.NoError(t, err)
	require.NoError(t, v.RegisterValidation("allowed_name", func(fl validation.FieldLevel) bool { return fl.Field().String() != "blocked" }, "{field}不可使用"))
	e.validator = v
	type Input struct {
		Name string `validate:"required,allowed_name" label:"姓名"`
	}
	mutation := NewMutation[Input]("User", nil)
	type requestKey struct{}
	ctx := context.WithValue(context.Background(), requestKey{}, "request")
	v.RegisterStructValidationCtx(func(callCtx context.Context, sl validation.StructLevel) {
		if callCtx.Value(requestKey{}) != "request" {
			sl.ReportError(sl.Current().FieldByName("Name").String(), "Name", "Name", "allowed_name", "")
		}
	}, Input{})
	mutation.Returning = []string{"id"}
	calls := 0
	mutation.After = func(ctx context.Context, _ MutationRequest, rows []map[string]any) error {
		calls++
		x, ok := ExecutionFrom(ctx)
		require.True(t, ok)
		require.NotNil(t, x.Transaction())
		require.Len(t, rows, 2)
		return fmt.Errorf("audit failed")
	}
	require.NoError(t, e.installMutations([]Mutation{mutation}))
	for _, query := range []string{
		`mutation { createUsers(input:[{name:"ok",email:"one"},{name:"blocked",email:"two"}]){id} }`,
		`mutation { createUsers(input:[{name:"ok",email:"one"},{name:"good",email:"two"}]){name} }`,
	} {
		r := e.run(ctx, query, nil, "")
		require.NotEmpty(t, r.Errors)
	}
	require.Equal(t, 1, calls)
	var count int
	require.NoError(t, testQueryRow(db, `SELECT count(*) FROM users`).Scan(&count))
	require.Zero(t, count)
	resolver := NewResolver("Mutation", "validateName", "", func(_ context.Context, _ Root, input Input) (string, error) { return input.Name, nil })
	_, err = resolver.Resolve(e.executionContextFor(ctx, nil, "Mutation.validateName"), Root{}, map[string]any{"name": "blocked"})
	require.Error(t, err)
	_, err = resolver.Resolve(e.executionContextFor(ctx, nil, "Mutation.validateName"), Root{}, map[string]any{"name": "allowed"})
	require.NoError(t, err)
}

func TestTypedMutationRejectsInvalidBinding(t *testing.T) {
	e, _, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	for _, mutation := range []Mutation{
		NewMutation[string]("User", nil),
		NewMutation[struct{ Missing string }]("User", nil),
		NewMutation[struct{ Name int }]("User", nil),
	} {
		require.Error(t, e.installMutations([]Mutation{mutation}))
	}
}

func TestTypedMutationIncludeRechecksPermissions(t *testing.T) {
	e, db, cleanup := newTestExecutor(t, nil)
	defer cleanup()
	testExec(t, db, `INSERT INTO users(id,name,email) VALUES (1,'old','original')`)
	type Input struct{ Name, Email string }
	called := false
	mutation := NewMutation[Input]("User", func(_ context.Context, change *Change[Input]) error {
		called = true
		change.Input.Email = "forbidden"
		change.Include("email")
		return nil
	})
	require.NoError(t, e.installMutations([]Mutation{mutation}))
	installTestPolicy(t, e, OperationPolicyProvider(func(_ context.Context, r PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: r.Resource == "User", Fields: map[FieldUse][]string{FieldUseWrite: {"name"}, FieldUseSelect: {"*"}, FieldUseReturn: {"*"}, FieldUseFilter: {"id"}}}, nil
	}))
	id, err := NewIdentity(IdentityInput{Tenant: "test", Subject: "user"})
	require.NoError(t, err)
	variables := map[string]any{"input": map[string]any{"name": "next"}}
	r := e.run(WithIdentity(context.Background(), id), `mutation($input:UserUpdateInput!){updateUser(id:1,input:$input){name}}`, variables, "")
	require.True(t, called, "%v", r.Errors)
	require.NotEmpty(t, r.Errors)
	require.Equal(t, map[string]any{"input": map[string]any{"name": "next"}}, variables)
	var email string
	require.NoError(t, testQueryRow(db, `SELECT email FROM users WHERE id=1`).Scan(&email))
	require.Equal(t, "original", email)
}

func TestTypedMutationExactNumbersDefaultsAndZeroValues(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, `CREATE TABLE meters(id serial PRIMARY KEY, amount bigint NOT NULL DEFAULT 17, enabled boolean NOT NULL DEFAULT true, name text NOT NULL DEFAULT 'database')`)
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
		Amount      int64 `validate:"min=0"`
		Enabled     bool
		DisplayName string `json:"name"`
	}
	mutation := NewMutation[Input]("Meter", func(_ context.Context, change *Change[Input]) error {
		// 改了未提交字段但没有 Include，不得覆盖数据库默认值或已有值。
		change.Input.Enabled = false
		return nil
	})
	require.NoError(t, e.installMutations([]Mutation{mutation}))
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
	require.NoError(t, testQueryRow(db, `SELECT amount,name FROM meters WHERE id=2`).Scan(&amount, &name))
	require.EqualValues(t, 9007199254740993, amount)
	require.Equal(t, "database", name)
	for _, input := range []string{`{amount:"9223372036854775808"}`, `{amount:"-1"}`, `{enabled:null}`} {
		r := e.run(context.Background(), `mutation{updateMeter(id:2,input:`+input+`){id}}`, nil, "")
		require.NotEmpty(t, r.Errors)
	}
	r := e.run(context.Background(), `mutation{updateMeters(where:{id:{eq:2}},input:{amount:"0",enabled:false}){affectedCount}}`, nil, "")
	require.Empty(t, r.Errors)
	require.NoError(t, testQueryRow(db, `SELECT amount,enabled FROM meters WHERE id=2`).Scan(&amount, &enabled))
	require.Zero(t, amount)
	require.False(t, enabled)
}
