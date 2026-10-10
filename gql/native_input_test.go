package gql

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNativeInputListNullAndEmpty(t *testing.T) {
	plan := &inputBinding{elem: &inputBinding{}, nullable: true}
	value, err := plan.encode(reflect.ValueOf([]string(nil)), []any{}, &inputState{})
	require.NoError(t, err)
	require.Nil(t, value)
	value, err = plan.encode(reflect.ValueOf([]string{}), nil, &inputState{})
	require.NoError(t, err)
	require.Equal(t, []any{}, value)
	plan.nullable = false
	_, err = plan.encode(reflect.ValueOf([]string(nil)), nil, &inputState{})
	require.ErrorContains(t, err, "不能为null")
}
