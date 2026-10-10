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
	original := registry.Load()
	registry.Store(&registrySnapshot{factories: map[string]Factory{"first": func() Dialect { return registryDialect("first") }}})
	t.Cleanup(func() { registry.Store(original) })

	got := Dialects()
	got[0] = registryDialect("mutated")

	require.Equal(t, "first", Dialects()[0].Name())
}

func TestRegisterRejectsInvalidDialect(t *testing.T) {
	original := registry.Load()
	registry.Store(&registrySnapshot{factories: map[string]Factory{}})
	t.Cleanup(func() { registry.Store(original) })

	require.Panics(t, func() { Register(nil) })
	require.NotPanics(t, func() { Register(func() Dialect { return registryDialect("postgresql") }) })
	require.Panics(t, func() { Register(func() Dialect { return registryDialect("postgresql") }) })
	require.Panics(t, func() { Register(func() Dialect { return registryDialect("") }) })
}

func TestDialectsBuildsIndependentInstances(t *testing.T) {
	original := registry.Load()
	registry.Store(&registrySnapshot{factories: map[string]Factory{}})
	t.Cleanup(func() { registry.Store(original) })
	type mutableDialect struct{ registryDialect }
	Register(func() Dialect { return &mutableDialect{registryDialect("mutable")} })
	first, second := Dialects()[0], Dialects()[0]
	require.NotSame(t, first, second)
}
