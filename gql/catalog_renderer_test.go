package gql

import (
	"strings"
	"testing"

	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/stretchr/testify/require"
)

func TestRendererCatalogReadOnlyRelationHasNoMutationSurface(t *testing.T) {
	view := governanceClass("ActiveUser")
	view.TableKind = protocol.TableView
	view.ReadOnly = true
	schema := governanceSchema(t, view)

	require.Contains(t, schema, "activeUsers(")
	require.NotContains(t, schema, "createActiveUser")
	require.NotContains(t, schema, "updateActiveUser")
	require.NotContains(t, schema, "deleteActiveUser")
	require.NotContains(t, schema, "upsertActiveUsers")
	require.False(t, strings.Contains(schema, "input ActiveUserCreateInput"))
	require.False(t, strings.Contains(schema, "input ActiveUserUpdateInput"))
}

func TestRendererDoesNotAdvertiseCursorPaginationWithoutPrimaryKey(t *testing.T) {
	view := governanceClass("EventView", protocol.OP_SELECT)
	view.PrimaryKeys = nil
	view.Fields["id"].IsPrimary = false
	schema := governanceSchema(t, view)

	require.Contains(t, schema, "eventViews(")
	require.Contains(t, schema, "limit: Int")
	require.NotContains(t, schema, "offset: Int")
	require.NotContains(t, schema, "first: Int")
	require.NotContains(t, schema, "after: Cursor")
	require.NotContains(t, schema, "eventViewConnection")
	require.NotContains(t, schema, "pageInfo: PageInfo  # 游标分页信息")
}

func TestRendererCatalogDefaultAndGenerationControlCreateInput(t *testing.T) {
	class := governanceClass("CatalogInput", protocol.OP_INSERT)
	class.Fields["id"].Catalog = &protocol.Column{Type: protocol.TypeRef{Name: "integer", Kind: protocol.TypeBase}}
	class.AddField(&protocol.Field{Name: "requiredValue", Column: "required_value", Type: "text"})
	class.AddField(&protocol.Field{Name: "defaultedValue", Column: "defaulted_value", Type: "text", Catalog: &protocol.Column{
		Default: &protocol.ColumnDefault{Expression: "'ready'::text"},
	}})
	class.AddField(&protocol.Field{Name: "generatedValue", Column: "generated_value", Type: "text", Catalog: &protocol.Column{
		Generation: &protocol.ColumnGeneration{Kind: protocol.GeneratedStored, Expression: "lower(required_value)"},
	}})
	schema := governanceSchema(t, class)
	start := strings.Index(schema, "input CatalogInputCreateInput {")
	require.NotEqual(t, -1, start)
	input := schema[start : start+strings.Index(schema[start:], "}\n")]
	require.Contains(t, input, "id: Int!", "catalog-backed natural primary keys must be client writable")
	require.Contains(t, input, "requiredValue: String!")
	require.Contains(t, input, "defaultedValue: String")
	require.NotContains(t, input, "defaultedValue: String!")
	require.NotContains(t, input, "generatedValue")
}

func TestRendererAdvertisesDefaultOnlyCreateWithoutEmptyInput(t *testing.T) {
	class := governanceClass("DefaultOnly", protocol.OP_INSERT)
	class.Fields["insertPreset"].ReadOnly = true
	class.Fields["updatePreset"].ReadOnly = true
	class.Fields["readOnly"].Catalog = &protocol.Column{Default: &protocol.ColumnDefault{Expression: "'read only'::text"}}
	class.Fields["updatePreset"].Catalog = &protocol.Column{Default: &protocol.ColumnDefault{Expression: "'update'::text"}}
	class.Fields["id"].Catalog = &protocol.Column{
		Type:       protocol.TypeRef{Name: "integer", Kind: protocol.TypeBase},
		Generation: &protocol.ColumnGeneration{Kind: protocol.GeneratedIdentityByDefault},
	}
	class.Fields["title"].Catalog = &protocol.Column{
		Type:    protocol.TypeRef{Name: "text", Kind: protocol.TypeBase},
		Default: &protocol.ColumnDefault{Expression: "'ready'::text"},
	}
	class.Fields["title"].ReadOnly = true
	schema := governanceSchema(t, class)
	require.Contains(t, schema, "createDefaultOnly: DefaultOnly!")
	require.NotContains(t, schema, "input DefaultOnlyCreateInput")
}

func TestRendererCatalogEnumAndArrayAreHonest(t *testing.T) {
	class := governanceClass("CatalogType", protocol.OP_SELECT, protocol.OP_INSERT)
	class.AddField(&protocol.Field{Name: "state", Column: "state", Type: "account_state", Catalog: &protocol.Column{Type: protocol.TypeRef{
		Name: "account_state", Kind: protocol.TypeEnum, EnumLabels: []string{"ACTIVE", "SUSPENDED"},
	}}})
	class.AddField(&protocol.Field{Name: "tags", Column: "tags", Type: "_text", Catalog: &protocol.Column{Type: protocol.TypeRef{
		Name: "_text", Kind: protocol.TypeArray, Element: &protocol.TypeRef{Name: "text", Kind: protocol.TypeBase},
	}}})
	schema := governanceSchema(t, class)
	require.Contains(t, schema, "enum AccountStateEnum {")
	require.Contains(t, schema, "state: AccountStateEnum!")
	require.Contains(t, schema, "tags: [String]!")
	start := strings.Index(schema, "input CatalogTypeCreateInput {")
	require.NotEqual(t, -1, start)
	input := schema[start : start+strings.Index(schema[start:], "}\n")]
	require.Contains(t, input, "state: AccountStateEnum!")
	require.Contains(t, input, "tags: [String]!")
	require.NotContains(t, schema, "state: AccountStateEnumWhereInput", "enum filter is not advertised before typed predicate support")
}

func TestRendererRejectsLossyDatabaseEnumLabel(t *testing.T) {
	class := governanceClass("BadEnum", protocol.OP_SELECT)
	class.AddField(&protocol.Field{Name: "state", Column: "state", Type: "bad_state", Catalog: &protocol.Column{Type: protocol.TypeRef{
		Name: "bad_state", Kind: protocol.TypeEnum, EnumLabels: []string{"in-progress"},
	}}})
	meta := createMockMetadata(t)
	meta.Nodes = map[string]*protocol.Class{class.Name: class}
	_, err := NewRenderer(meta).Render()
	require.ErrorContains(t, err, "非法GraphQL值")
}

func TestRendererCatalogConflictTargetsAreTypedAndExecutable(t *testing.T) {
	class := governanceClass("CatalogInput", protocol.OP_INSERT, protocol.OP_UPDATE)
	class.Fields["id"].IsUnique = false
	class.Constraints = []*protocol.Constraint{
		{Name: "catalog_primary", Kind: protocol.ConstraintPrimary, Columns: []string{"id"}},
		{Name: "catalog_title_read_only", Kind: protocol.ConstraintUnique, Columns: []string{"title", "readOnly"}},
	}
	class.Indexes = []*protocol.Index{
		{Name: "catalog_partial", Unique: true, Predicate: "deleted_at IS NULL", Keys: []protocol.IndexKey{{Column: "title"}}},
		{Name: "catalog_expression", Unique: true, Keys: []protocol.IndexKey{{Expression: "lower(title)"}}},
	}
	schema := governanceSchema(t, class)

	require.Contains(t, schema, "enum CatalogInputConflictTarget {")
	require.Contains(t, schema, "PRIMARY  # id")
	require.Contains(t, schema, "UNIQUE_1  # title, readOnly")
	require.NotContains(t, schema, "catalog_partial")
	require.NotContains(t, schema, "catalog_expression")
	require.Contains(t, schema, "on: CatalogInputConflictTarget = PRIMARY")
}

func TestRendererRequiresConflictTargetWhenNoPrimaryKeyExists(t *testing.T) {
	class := governanceClass("CatalogInput", protocol.OP_INSERT, protocol.OP_UPDATE)
	class.PrimaryKeys = nil
	class.Fields["id"].IsPrimary = false
	class.Fields["id"].IsUnique = false
	class.Constraints = []*protocol.Constraint{{Name: "catalog_title", Kind: protocol.ConstraintUnique, Columns: []string{"title"}}}

	require.Contains(t, governanceSchema(t, class), "on: CatalogInputConflictTarget!")
}

func TestRendererUsesTypedCompositePrimaryKey(t *testing.T) {
	class := governanceClass("CatalogInput", protocol.OP_SELECT, protocol.OP_UPDATE, protocol.OP_DELETE)
	class.PrimaryKeys = []string{"id", "title"}
	class.Fields["title"].IsPrimary = true
	schema := governanceSchema(t, class)

	require.Contains(t, schema, "input CatalogInputKeyInput {")
	require.Contains(t, schema, "id: Int!")
	require.Contains(t, schema, "title: String!")
	require.Contains(t, schema, "catalogInputs(\n    key: CatalogInputKeyInput")
	require.Contains(t, schema, "updateCatalogInput(key: CatalogInputKeyInput!, input: CatalogInputUpdateInput!)")
	require.Contains(t, schema, "deleteCatalogInputByPk(key: CatalogInputKeyInput!)")
	require.NotContains(t, schema, "updateCatalogInputByPk")
}
