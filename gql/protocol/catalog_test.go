package protocol

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCatalogFactsRoundTripAndConflictTargets(t *testing.T) {
	class := &Class{
		Name: "Account", Schema: "tenant_a", Table: "accounts", TableKind: TableBase,
		Fields: map[string]*Field{"id": {
			Name: "id", Column: "id", Type: "int8", Catalog: &Column{
				Ordinal: 1, Generation: &ColumnGeneration{Kind: GeneratedIdentityAlways},
				Type: TypeRef{Schema: "pg_catalog", Name: "int8", Kind: TypeBase},
			},
		}},
		Constraints: []*Constraint{{Name: "accounts_pkey", Kind: ConstraintPrimary, Columns: []string{"id"}, Validated: true}},
		Indexes:     []*Index{{Name: "accounts_pkey", Unique: true, Primary: true, Method: "btree", Keys: []IndexKey{{Column: "id"}}}},
	}
	data, err := json.Marshal(class)
	require.NoError(t, err)
	var decoded Class
	require.NoError(t, json.Unmarshal(data, &decoded))
	require.Equal(t, class.Schema, decoded.Schema)
	require.Equal(t, GeneratedIdentityAlways, decoded.Fields["id"].Catalog.Generation.Kind)
	require.Equal(t, ConstraintPrimary, decoded.Constraints[0].Kind)
	require.True(t, decoded.Indexes[0].IsUsableConflictTarget())

	partial := &Index{Unique: true, Predicate: "deleted_at IS NULL", Keys: []IndexKey{{Column: "id"}}}
	expression := &Index{Unique: true, Keys: []IndexKey{{Expression: "lower(email)"}}}
	deferred := &Index{Unique: true, Deferred: true, Keys: []IndexKey{{Column: "id"}}}
	invalid := &Index{Unique: true, Invalid: true, Keys: []IndexKey{{Column: "id"}}}
	require.False(t, partial.IsUsableConflictTarget())
	require.False(t, expression.IsUsableConflictTarget())
	require.False(t, deferred.IsUsableConflictTarget())
	require.False(t, invalid.IsUsableConflictTarget())
}

func TestConflictTargetsExcludeDeferrableAndUnsafeIndexes(t *testing.T) {
	class := &Class{
		Fields: map[string]*Field{
			"tenant": {Name: "tenant", IsUnique: true},
			"code":   {Name: "code"},
		},
		Constraints: []*Constraint{
			{Name: "deferred_primary", Kind: ConstraintPrimary, Columns: []string{"tenant"}, Deferrable: true},
			{Name: "code_unique", Kind: ConstraintUnique, Columns: []string{"tenant", "code"}},
		},
		Indexes: []*Index{
			{Name: "partial", Unique: true, Predicate: "active", Keys: []IndexKey{{Column: "code"}}},
			{Name: "expression", Unique: true, Keys: []IndexKey{{Expression: "lower(code)"}}},
		},
	}

	require.Equal(t, []Key{{Name: "UNIQUE_1", Columns: []string{"tenant", "code"}}}, class.ConflictTargets())
	_, ok := class.PrimaryConflictTarget()
	require.False(t, ok)
}

func TestRoutineValidationRejectsIncompleteCatalogFacts(t *testing.T) {
	routine := &Routine{Schema: "public", Name: "lookup", Kind: RoutineFunction, Volatility: RoutineVolatilityStable}
	require.ErrorContains(t, routine.Validate(), "return type")
	routine.ReturnType = "text"
	routine.Arguments = []RoutineArgument{{Name: "id", Mode: "wrong", Type: "integer"}}
	require.ErrorContains(t, routine.Validate(), "参数无效")
}
