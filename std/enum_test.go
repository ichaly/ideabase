package std

import (
	"testing"

	"github.com/stretchr/testify/require"
)

type (
	status        string
	pointerStatus string
)

const (
	statusA status = "a"
	statusB status = "b"

	pointerOk   pointerStatus = "ok"
	pointerFail pointerStatus = "fail"
)

func (s status) Valid() bool { return s == statusA || s == statusB }

func (s *pointerStatus) Valid() bool {
	if s == nil {
		return false
	}
	return *s == pointerOk
}

func TestEnumValidation(t *testing.T) {
	v, err := NewValidator()
	require.NoError(t, err)

	type payload struct {
		Status  status         `validate:"enum"`
		Pointer *pointerStatus `validate:"enum"`
	}

	require.NoError(t, v.Struct(payload{Status: statusA, Pointer: ptr(pointerOk)}))
	require.NoError(t, v.Struct(payload{Status: statusB, Pointer: ptr(pointerOk)}))

	require.Error(t, v.Struct(payload{Status: "c", Pointer: ptr(pointerOk)}))
	require.Error(t, v.Struct(payload{Status: statusA, Pointer: ptr(pointerFail)}))
	require.Error(t, v.Struct(payload{Status: statusB}))
}

func ptr[T any](v T) *T { return &v }
