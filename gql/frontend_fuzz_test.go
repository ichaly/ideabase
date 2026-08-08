package gql

import (
	"testing"

	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
)

func FuzzExecutableFrontend(f *testing.F) {
	schema := gqlparser.MustLoadSchema(&ast.Source{Input: `
		type Query { value: String, items(limit: Int): [Item!]! }
		type Item { value: String }
	`})
	for _, seed := range []string{
		`{ value }`,
		`query($show: Boolean!){ value @include(if: $show) }`,
		`query { items(limit: 2) { value } }`,
		`fragment F on Query { value } query { ...F }`,
		`query { unknown }`,
		`query { items { value `,
	} {
		f.Add(seed, true)
	}
	rules := validatorrules.NewDefaultRules()
	f.Fuzz(func(t *testing.T, source string, show bool) {
		if len(source) > 4096 {
			t.Skip()
		}
		doc, errs := loadQuery(schema, source, 256, rules)
		if len(errs) > 0 || doc == nil {
			return
		}
		for _, operation := range doc.Operations {
			variables := map[string]any{"show": show}
			coerced, err := coerceVariables(schema, operation, variables)
			if err != nil {
				continue
			}
			selection, err := normalizeSelectionWithSchema(operation.SelectionSet, doc.Fragments, coerced, schema)
			if err != nil {
				continue
			}
			operation.SelectionSet = selection
			_ = operationCost(operation, coerced, 10, nil)
		}
	})
}
