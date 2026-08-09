package gql

import (
	"fmt"
	"sync"
	"testing"

	"github.com/huandu/go-clone"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func TestPolicyOverlayIsAliasStableAndDoesNotMutateCatalog(t *testing.T) {
	field := &protocol.Field{Name: "ownerId", Column: "owner_id", Type: "ID"}
	baseClass := &protocol.Class{
		Name: "User", Table: "users",
		Fields: map[string]*protocol.Field{"ownerId": field, "owner_id": field},
		Permissions: map[string]*protocol.Permission{
			"reader": {Select: []string{"ownerId"}},
		},
	}
	base := &Metadata{Nodes: map[string]*protocol.Class{
		"User": baseClass, "users": baseClass,
	}}

	overlay := newPolicyOverlay(base)
	got := overlay.class(baseClass)

	require.Same(t, got, overlay.metadata.Nodes["User"])
	require.Same(t, got, overlay.metadata.Nodes["users"])
	require.Nil(t, got.Permissions)
	require.NotNil(t, baseClass.Permissions)
	require.Same(t, field, got.Fields["ownerId"])
}

func TestPolicyOverlayClearsPermissionsFromEveryClass(t *testing.T) {
	first := &protocol.Class{Name: "User", Permissions: map[string]*protocol.Permission{"reader": {Select: []string{"id"}}}}
	second := &protocol.Class{Name: "Post", Permissions: map[string]*protocol.Permission{"reader": {Select: []string{"id"}}}}
	base := &Metadata{Nodes: map[string]*protocol.Class{
		"User": first, "users": first, "Post": second,
	}}
	overlay := newPolicyOverlay(base)
	require.Nil(t, overlay.metadata.Nodes["User"].Permissions)
	require.Nil(t, overlay.metadata.Nodes["users"].Permissions)
	require.Nil(t, overlay.metadata.Nodes["Post"].Permissions)
	require.NotNil(t, first.Permissions)
	require.NotNil(t, second.Permissions)
}

func TestPolicyOverlayCopiesOnlyPresetField(t *testing.T) {
	name := &protocol.Field{Name: "name", Column: "name", Type: "String"}
	owner := &protocol.Field{Name: "ownerId", Column: "owner_id", Type: "ID"}
	baseClass := &protocol.Class{
		Name: "User", Table: "users",
		Fields: map[string]*protocol.Field{
			"name": name, "ownerId": owner, "owner_id": owner,
		},
	}
	overlay := newPolicyOverlay(&Metadata{Nodes: map[string]*protocol.Class{"User": baseClass}})
	got := overlay.class(baseClass)

	preset := &protocol.ValueSource{Context: "tenant"}
	mutable := overlay.mutableField(got, "ownerId")
	mutable.InsertPreset = preset

	require.NotSame(t, owner, got.Fields["ownerId"])
	require.Same(t, got.Fields["ownerId"], got.Fields["owner_id"])
	require.Same(t, name, got.Fields["name"])
	require.Nil(t, owner.InsertPreset)
}

func TestPolicyOverlaysKeepConcurrentDecisionsIsolated(t *testing.T) {
	field := &protocol.Field{Name: "ownerId", Column: "owner_id", Type: "ID"}
	baseClass := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{"ownerId": field}}
	base := &Metadata{Nodes: map[string]*protocol.Class{"User": baseClass}}

	const workers = 16
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			overlay := newPolicyOverlay(base)
			class := overlay.class(baseClass)
			mutable := overlay.mutableField(class, "ownerId")
			mutable.InsertPreset = &protocol.ValueSource{Context: fmt.Sprintf("tenant-%d", i)}
			if mutable.InsertPreset.Context != fmt.Sprintf("tenant-%d", i) {
				t.Errorf("overlay %d was contaminated", i)
			}
		}(i)
	}
	wg.Wait()
	require.Nil(t, baseClass.Fields["ownerId"].InsertPreset)
}

func BenchmarkPolicyOverlayCold(b *testing.B) {
	field := &protocol.Field{Name: "ownerId", Column: "owner_id", Type: "ID"}
	baseClass := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{"ownerId": field}}
	base := &Metadata{Nodes: map[string]*protocol.Class{"User": baseClass}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		overlay := newPolicyOverlay(base)
		class := overlay.class(baseClass)
		overlay.mutableField(class, "ownerId").InsertPreset = &protocol.ValueSource{Context: "tenant"}
	}
}

func BenchmarkPolicyOverlayWarm(b *testing.B) {
	field := &protocol.Field{Name: "ownerId", Column: "owner_id", Type: "ID"}
	baseClass := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{"ownerId": field}}
	base := &Metadata{Nodes: map[string]*protocol.Class{"User": baseClass}}
	overlay := newPolicyOverlay(base)
	class := overlay.class(baseClass)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		mutable := overlay.mutableField(class, "ownerId")
		mutable.InsertPreset = &protocol.ValueSource{Context: "tenant"}
	}
}

func BenchmarkPolicyCloneCold(b *testing.B) {
	field := &protocol.Field{Name: "ownerId", Column: "owner_id", Type: "ID"}
	baseClass := &protocol.Class{Name: "User", Fields: map[string]*protocol.Field{"ownerId": field}}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		class := clone.Slowly(baseClass).(*protocol.Class)
		class.Permissions = nil
		class.Fields["ownerId"].InsertPreset = &protocol.ValueSource{Context: "tenant"}
	}
}
