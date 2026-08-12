package compiler

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2/ast"
)

type registryDialect string

func (d registryDialect) Name() string                              { return string(d) }
func (registryDialect) Quotation() string                           { return `"` }
func (registryDialect) Placeholder(int) string                      { return "$1" }
func (registryDialect) BuildQuery(*Context, ast.SelectionSet) error { return nil }
func (registryDialect) BuildMutation(*Context, ast.SelectionSet) error {
	return nil
}

func TestDialectsReturnsImmutableSnapshot(t *testing.T) {
	original := registry
	registry = []Dialect{registryDialect("first")}
	t.Cleanup(func() { registry = original })

	got := Dialects()
	got[0] = registryDialect("mutated")

	require.Equal(t, "first", Dialects()[0].Name())
}

func TestRegisterRejectsInvalidDialect(t *testing.T) {
	original := registry
	registry = nil
	t.Cleanup(func() { registry = original })

	require.Panics(t, func() { Register(nil) })
	require.NotPanics(t, func() { Register(registryDialect("postgresql")) })
	require.Panics(t, func() { Register(registryDialect("postgresql")) })
	require.Panics(t, func() { Register(registryDialect("")) })
}
