package gql

import (
	"context"
	stdjson "encoding/json"
	"testing"

	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

func TestExactNumberInputValidation(t *testing.T) {
	for _, scalar := range []string{"BigInt", "Decimal"} {
		for _, value := range []any{true, map[string]any{"value": 1}, "NaN", "Infinity", "01", "1/3", " 1", float64(9007199254740992)} {
			_, err := coerceScalar(scalar, value, nil)
			require.NotNil(t, err, "%s accepted %v", scalar, value)
		}
		for _, value := range []any{"9007199254740993", stdjson.Number("9007199254740993"), int64(9007199254740993)} {
			got, err := coerceScalar(scalar, value, nil)
			require.Nil(t, err)
			require.Equal(t, "9007199254740993", got)
		}
	}
	_, err := coerceScalar("BigInt", "1.5", nil)
	require.NotNil(t, err)
	value, err := coerceScalar("Decimal", stdjson.Number("1.2300"), nil)
	require.Nil(t, err)
	require.Equal(t, "1.2300", value)
}

type exactResolverNumbers struct {
	Signed   int64  `json:"signed"`
	Unsigned uint64 `json:"unsigned"`
}

func TestExactResolverNumbers(t *testing.T) {
	engine, cleanup := setupTestExecutor(t)
	defer cleanup()
	require.NoError(t, engine.register(NewResolver("Query", "exactEcho", "精确整数往返",
		func(_ context.Context, _ Root, input exactResolverNumbers) (exactResolverNumbers, error) {
			return input, nil
		})))
	for _, query := range []string{
		`{exactEcho(signed:"-9223372036854775808",unsigned:"18446744073709551615"){signed unsigned}}`,
		`query($signed:BigInt!,$unsigned:BigInt!){exactEcho(signed:$signed,unsigned:$unsigned){signed unsigned}}`,
	} {
		body, err := engine.Execute(context.Background(), query, map[string]any{"signed": "-9223372036854775808", "unsigned": "18446744073709551615"})
		require.NoError(t, err)
		require.JSONEq(t, `{"data":{"exactEcho":{"signed":"-9223372036854775808","unsigned":"18446744073709551615"}}}`, string(body))
	}
}

// 不注册任何 Codec，验证默认构建即可完成精确数值往返。
func TestDefaultExactNumbers(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	testExec(t, db, `CREATE DOMAIN exact_amount AS numeric(40,20);
		CREATE TABLE exact_values (
		id bigint PRIMARY KEY, total bigint, amount numeric(40,20),
		amount_domain exact_amount, totals bigint[], amounts numeric[], small integer, ratio double precision
	)`)
	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	meta, err := NewMetadata(k, db)
	require.NoError(t, err)
	compiler, err := NewCompiler(meta, nil)
	require.NoError(t, err)
	engine, err := newEngineForTest(db, NewRenderer(meta), meta, compiler)
	require.NoError(t, err)
	defer engine.Close()

	query := `mutation { createExactValue(input:{
		id:"9007199254740993", total:"9007199254740993",
		amount:"12345678901234567890.12345678901234567890",
		amountDomain:"12345678901234567890.12345678901234567890",
		totals:["9007199254740993"], amounts:["0.1234567890123456789"], small:7, ratio:1.5
	}) { id total amount amountDomain totals amounts small ratio } }`
	body, err := engine.Execute(context.Background(), query, nil)
	require.NoError(t, err)
	require.JSONEq(t, `{"data":{"createExactValue":{
		"id":"9007199254740993","total":"9007199254740993",
		"amount":"12345678901234567890.12345678901234567890",
		"amountDomain":"12345678901234567890.12345678901234567890",
		"totals":["9007199254740993"],"amounts":["0.1234567890123456789"],"small":7,"ratio":1.5
	}}}`, string(body))

	query = `query($minimum:BigInt!){exactValues(where:{total:{gt:$minimum},amount:{gt:"9"}}){items{id total}}
		exactValueStats{total{sum avg min max} amount{sum avg}}}`
	for i := 0; i < 2; i++ { // 同时覆盖首次编译与计划缓存命中。
		body, err = engine.Execute(context.Background(), query, map[string]any{"minimum": "9007199254740992"})
		require.NoError(t, err)
		require.NotContains(t, string(body), `"errors"`)
		require.Contains(t, string(body), `"sum": "9007199254740993"`)
	}
	for _, query := range []string{
		`{exactValues(where:{amount:{eq:12345678901234567890.12345678901234567890}}){items{total}}}`,
		`query($amount:Decimal=12345678901234567890.12345678901234567890){exactValues(where:{amount:{eq:$amount}}){items{total}}}`,
	} {
		body, err = engine.Execute(context.Background(), query, nil)
		require.NoError(t, err)
		require.JSONEq(t, `{"data":{"exactValues":{"items":[{"total":"9007199254740993"}]}}}`, string(body))
	}
	for _, query := range []string{
		`{exactValues(where:{total:{eq:"1.5"}}){items{id}}}`,
		`{exactValues(where:{amount:{eq:"NaN"}}){items{id}}}`,
		`{exactValues(where:{total:{eq:true}}){items{id}}}`,
		`{exactValues(where:{total:{eq:[1,2]}}){items{id}}}`,
	} {
		_, err = engine.parse(query, "", nil)
		require.Error(t, err, "invalid exact scalar must fail before SQL: %s", query)
	}

	// 自定义 resolver 的显式精确标量输出也遵循同一协议。
	for _, scalar := range []string{"BigInt", "Decimal"} {
		value, ok := coerceCompletedScalar(engine.schema, ast.NamedType(scalar, nil), int64(9007199254740993))
		require.True(t, ok)
		require.Equal(t, "9007199254740993", value)
	}

	// bigint 求和超过 int64，平均值有小数时仍逐字等于数据库的精确结果。
	testExec(t, db, `INSERT INTO exact_values(id,total) VALUES (2,9223372036854775807),(3,9223372036854775807)`)
	var sum, average string
	require.NoError(t, testQueryRow(db, `SELECT sum(total)::text,avg(total)::text FROM exact_values`).Scan(&sum, &average))
	body, err = engine.Execute(context.Background(), `{exactValueStats(having:{total:{sum:{gt:"9223372036854775807"}}}){total{sum avg}}}`, nil)
	require.NoError(t, err)
	var aggregate struct {
		Data struct {
			Rows []struct{ Total struct{ Sum, Avg string } } `json:"exactValueStats"`
		}
	}
	require.NoError(t, stdjson.Unmarshal(body, &aggregate))
	require.Len(t, aggregate.Data.Rows, 1, "%s", body)
	require.Equal(t, sum, aggregate.Data.Rows[0].Total.Sum)
	require.Equal(t, average, aggregate.Data.Rows[0].Total.Avg)
}
