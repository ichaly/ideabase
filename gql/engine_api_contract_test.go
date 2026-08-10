package gql

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"gorm.io/gorm"
)

func TestEngineIsTheOnlyRuntimeAndCannotMutateAfterBuild(t *testing.T) {
	typeOfEngine := reflect.TypeOf(Engine{})
	if _, ok := typeOfEngine.FieldByName("Engine"); ok {
		t.Fatal("Engine must own runtime state directly; embedded Engine facade is forbidden")
	}
	for _, name := range []string{"Register", "Replace", "Wrap", "LoadDocuments"} {
		if _, ok := typeOfEngine.MethodByName(name); ok {
			t.Fatalf("Engine must not expose startup mutation method %s", name)
		}
	}
}

// testAllowAllPolicy keeps package-level lifecycle tests explicit without
// reintroducing a production bypass policy.
func (my *Builder) testAllowAllPolicy() *Builder {
	if err := my.Policy(OperationPolicyProvider(func(context.Context, PolicyRequest) (PolicyDecision, error) {
		return PolicyDecision{Allow: true, Fields: map[FieldUse][]string{
			FieldUseSelect: {"*"}, FieldUseFilter: {"*"}, FieldUseWrite: {"*"}, FieldUseReturn: {"*"},
		}}, nil
	})); err != nil {
		panic(err)
	}
	return my
}

func newEngineForTest(d *gorm.DB, renderer *Renderer, metadata *Metadata, compiler *Compiler) (*Engine, error) {
	if metadata == nil || renderer == nil {
		return nil, fmt.Errorf("缺少Metadata或Renderer")
	}
	if err := configureSearchMode(metadata, d); err != nil {
		return nil, err
	}
	catalog := metadata.freeze()
	if compiler == nil {
		var err error
		compiler, err = NewCompiler(catalog, nil)
		if err != nil {
			return nil, err
		}
	}
	compiled, err := compiler.freeze(catalog)
	if err != nil {
		return nil, err
	}
	data, err := loadSchema(catalog, NewRenderer(catalog))
	if err != nil {
		return nil, err
	}
	return newEngine(d, catalog, compiled, data)
}
