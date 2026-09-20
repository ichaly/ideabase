package gql

import (
	stdjson "encoding/json"
	"fmt"
	"math"
	"reflect"
	"strings"
	"testing"

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
			field := newScalarBinding(reflect.TypeOf(tc.target), false)
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
			actual, err = field.encode(reflect.ValueOf(value.Interface()))
			require.NoError(t, err)
			require.Equal(t, tc.want, actual)
		})
	}
	field := newScalarBinding(reflect.TypeFor[float64](), false)
	for _, value := range []float64{math.NaN(), math.Inf(1)} {
		_, err := field.encode(reflect.ValueOf(value))
		require.Error(t, err)
	}
}
