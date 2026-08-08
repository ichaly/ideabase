package gql

import (
	"testing"

	"github.com/ichaly/ideabase/gql/metadata"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/require"
)

func TestPostgresCatalogFactsFromRealDatabase(t *testing.T) {
	db, cleanup := setupTestDatabase(t)
	defer cleanup()
	const fixture = `
CREATE SCHEMA "Catalog Case";
CREATE TYPE "Catalog Case".account_mood AS ENUM ('new', 'in-progress');
CREATE DOMAIN "Catalog Case".positive_int AS integer CHECK (VALUE > 0);
CREATE TABLE "Catalog Case"."Account" (
  tenant_id integer NOT NULL,
  code text NOT NULL,
  id bigint GENERATED ALWAYS AS IDENTITY,
  mood "Catalog Case".account_mood NOT NULL DEFAULT 'new',
  score "Catalog Case".positive_int NOT NULL DEFAULT 1,
  tags text[] NOT NULL DEFAULT '{}',
  slug text GENERATED ALWAYS AS (lower(code)) STORED,
  CONSTRAINT account_pk PRIMARY KEY (tenant_id, code),
  CONSTRAINT account_tenant_id_key UNIQUE (tenant_id, id),
  CONSTRAINT account_deferred_code UNIQUE (code) DEFERRABLE INITIALLY DEFERRED,
  CONSTRAINT account_score_check CHECK (score > 0)
);
CREATE TABLE "Catalog Case".child (
  tenant_id integer NOT NULL,
  account_code text NOT NULL,
  note text,
  CONSTRAINT child_account_fk FOREIGN KEY (tenant_id, account_code)
    REFERENCES "Catalog Case"."Account" (tenant_id, code)
    ON UPDATE CASCADE ON DELETE RESTRICT
);
CREATE UNIQUE INDEX account_active_code ON "Catalog Case"."Account" (code) WHERE score > 0;
CREATE UNIQUE INDEX account_lower_code ON "Catalog Case"."Account" (lower(code));
CREATE INDEX account_mood_cover ON "Catalog Case"."Account" USING btree (mood) INCLUDE (score);
CREATE VIEW "Catalog Case".account_view AS SELECT tenant_id, code FROM "Catalog Case"."Account";
CREATE MATERIALIZED VIEW "Catalog Case".account_rollup AS SELECT tenant_id, count(*) AS total FROM "Catalog Case"."Account" GROUP BY tenant_id;
CREATE TABLE "Catalog Case".events (id bigint, happened_at date) PARTITION BY RANGE (happened_at);
CREATE EXTENSION postgres_fdw;
CREATE SERVER catalog_dummy FOREIGN DATA WRAPPER postgres_fdw OPTIONS (host '127.0.0.1', dbname 'test');
CREATE FOREIGN TABLE "Catalog Case".external_account (id bigint) SERVER catalog_dummy OPTIONS (table_name 'external_account');
CREATE TABLE "Catalog Case"."Order Items" ("Order ID" bigint PRIMARY KEY);
CREATE FUNCTION "Catalog Case".catalog_sum(a integer, INOUT b integer DEFAULT 2, c text DEFAULT 'x,y')
RETURNS integer LANGUAGE SQL IMMUTABLE SECURITY DEFINER AS $$ SELECT a + b $$;
CREATE FUNCTION "Catalog Case".catalog_sum(a bigint)
RETURNS SETOF bigint LANGUAGE SQL STABLE AS $$ SELECT a $$;
CREATE PROCEDURE "Catalog Case".catalog_refresh(IN account_id bigint, INOUT status text)
LANGUAGE plpgsql AS $$ BEGIN status := 'done'; END $$;
`
	require.NoError(t, db.Exec(fixture).Error)

	k, err := std.NewKonfig()
	require.NoError(t, err)
	k.Set("mode", "dev")
	k.Set("app.root", t.TempDir())
	k.Set("schema.schema", "Catalog Case")
	k.Set("metadata.use-camel", false)
	k.Set("metadata.use-singular", false)
	meta, err := NewMetadata(k, db, WithoutLoader(metadata.LoaderMysql, metadata.LoaderFile))
	require.NoError(t, err)

	account := meta.Nodes["Account"]
	require.NotNil(t, account)
	require.Equal(t, "Catalog Case", account.Schema)
	require.Equal(t, protocol.TableBase, account.TableKind)
	require.Equal(t, []string{"tenant_id", "code"}, account.PrimaryKeys)
	require.Equal(t, "int4", account.Fields["tenant_id"].Type)
	require.Equal(t, "text", account.Fields["code"].Type)

	id := account.Fields["id"]
	require.NotNil(t, id.Catalog.Generation)
	require.Equal(t, protocol.GeneratedIdentityAlways, id.Catalog.Generation.Kind)
	require.True(t, id.ReadOnly)
	slug := account.Fields["slug"]
	require.Equal(t, protocol.GeneratedStored, slug.Catalog.Generation.Kind)
	require.Contains(t, slug.Catalog.Generation.Expression, "lower(code)")
	require.True(t, slug.ReadOnly)
	require.Equal(t, "1", account.Fields["score"].Catalog.Default.Expression)
	require.Equal(t, protocol.TypeDomain, account.Fields["score"].Catalog.Type.Kind)
	require.Equal(t, "int4", account.Fields["score"].Catalog.Type.Base)
	require.Equal(t, protocol.TypeArray, account.Fields["tags"].Catalog.Type.Kind)
	require.Equal(t, "text", account.Fields["tags"].Catalog.Type.Element.Name)
	require.Equal(t, protocol.TypeEnum, account.Fields["mood"].Catalog.Type.Kind)
	require.Equal(t, []string{"new", "in-progress"}, account.Fields["mood"].Catalog.Type.EnumLabels)

	fk := findConstraint(t, meta.Nodes["child"], "child_account_fk")
	require.Equal(t, protocol.ConstraintForeign, fk.Kind)
	require.Equal(t, []string{"tenant_id", "account_code"}, fk.Columns)
	require.Equal(t, []string{"tenant_id", "code"}, fk.ReferencedColumns)
	require.Equal(t, "cascade", fk.OnUpdate)
	require.Equal(t, "restrict", fk.OnDelete)
	require.Equal(t, "simple", fk.Match)

	partial := findIndex(t, account, "account_active_code")
	require.NotEmpty(t, partial.Predicate)
	require.False(t, partial.IsUsableConflictTarget())
	expression := findIndex(t, account, "account_lower_code")
	require.Contains(t, expression.Keys[0].Expression, "lower(code)")
	require.False(t, expression.IsUsableConflictTarget())
	deferred := findIndex(t, account, "account_deferred_code")
	require.True(t, deferred.Deferred)
	require.False(t, deferred.IsUsableConflictTarget())
	cover := findIndex(t, account, "account_mood_cover")
	require.Equal(t, []string{"score"}, cover.Include)
	require.Equal(t, "btree", cover.Method)

	require.True(t, meta.Nodes["account_view"].ReadOnly)
	require.Equal(t, protocol.TableView, meta.Nodes["account_view"].TableKind)
	require.True(t, meta.Nodes["account_rollup"].ReadOnly)
	require.Equal(t, protocol.TableMaterialized, meta.Nodes["account_rollup"].TableKind)
	require.Equal(t, protocol.TablePartitioned, meta.Nodes["events"].TableKind)
	require.True(t, meta.Nodes["external_account"].ReadOnly)
	require.Equal(t, protocol.TableForeign, meta.Nodes["external_account"].TableKind)
	require.NotNil(t, meta.Nodes["Order Items"].Fields["Order ID"], "quoted identifier must round-trip")

	sum := meta.Routines[`Catalog Case.catalog_sum(a integer, INOUT b integer, c text)`]
	require.NotNil(t, sum)
	require.Equal(t, protocol.RoutineFunction, sum.Kind)
	require.Equal(t, "a integer, INOUT b integer, c text", sum.Identity)
	require.Equal(t, []protocol.RoutineArgument{
		{Name: "a", Mode: protocol.RoutineArgIn, Type: "integer"},
		{Name: "b", Mode: protocol.RoutineArgInOut, Type: "integer", Default: "2"},
		{Name: "c", Mode: protocol.RoutineArgIn, Type: "text", Default: "'x,y'::text"},
	}, sum.Arguments)
	require.Equal(t, "integer", sum.ReturnType)
	require.False(t, sum.ReturnsSet)
	require.Equal(t, protocol.RoutineVolatilityImmutable, sum.Volatility)
	require.True(t, sum.SecurityDefiner)

	set := meta.Routines[`Catalog Case.catalog_sum(a bigint)`]
	require.NotNil(t, set)
	require.True(t, set.ReturnsSet)
	require.Equal(t, "bigint", set.ReturnType)
	require.Equal(t, protocol.RoutineVolatilityStable, set.Volatility)

	proc := meta.Routines[`Catalog Case.catalog_refresh(IN account_id bigint, INOUT status text)`]
	require.NotNil(t, proc)
	require.Equal(t, protocol.RoutineProcedure, proc.Kind)
	require.Equal(t, protocol.RoutineArgInOut, proc.Arguments[1].Mode)
	require.Empty(t, proc.Arguments[1].Default)
}

func findConstraint(t *testing.T, class *protocol.Class, name string) *protocol.Constraint {
	t.Helper()
	for _, constraint := range class.Constraints {
		if constraint.Name == name {
			return constraint
		}
	}
	t.Fatalf("constraint %s not found", name)
	return nil
}

func findIndex(t *testing.T, class *protocol.Class, name string) *protocol.Index {
	t.Helper()
	for _, index := range class.Indexes {
		if index.Name == name {
			return index
		}
	}
	t.Fatalf("index %s not found", name)
	return nil
}
