package compiler

import (
	"maps"
	"slices"
	"sync"
	"sync/atomic"
)

// Factory creates one engine-owned dialect. Registered factories must not
// return shared mutable instances.
type Factory func() Dialect

var (
	registryMu sync.Mutex
	registry   atomic.Pointer[registrySnapshot]
)

type registrySnapshot struct{ factories map[string]Factory }

func init() { registry.Store(&registrySnapshot{factories: map[string]Factory{}}) }

func Register(factory Factory) {
	if factory == nil {
		panic("compiler: register invalid dialect")
	}
	dialect := factory()
	if dialect == nil || dialect.Name() == "" {
		panic("compiler: register invalid dialect")
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	current := registry.Load()
	if _, exists := current.factories[dialect.Name()]; exists {
		panic("compiler: register duplicate dialect " + dialect.Name())
	}
	factories := maps.Clone(current.factories)
	factories[dialect.Name()] = factory
	registry.Store(&registrySnapshot{factories: factories})
}

// Dialects creates a fresh dialect set for one compiler build.
func Dialects() []Dialect {
	factories := registry.Load().factories
	names := slices.Sorted(maps.Keys(factories))
	result := make([]Dialect, len(names))
	for index, name := range names {
		result[index] = factories[name]()
	}
	return result
}
