package gql

import (
	"crypto/sha256"
	stdjson "encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

// sdlContract deliberately records only public, machine-comparable GraphQL
// shape. The generated schema header contains a timestamp and is excluded.
type sdlContract struct {
	Types      []sdlTypeContract      `json:"types"`
	Directives []sdlDirectiveContract `json:"directives"`
}

type sdlTypeContract struct {
	Kind   string   `json:"kind"`
	Name   string   `json:"name"`
	Fields []string `json:"fields,omitempty"`
	Values []string `json:"values,omitempty"`
}

type sdlDirectiveContract struct {
	Name       string   `json:"name"`
	Arguments  []string `json:"arguments,omitempty"`
	Locations  []string `json:"locations"`
	Repeatable bool     `json:"repeatable,omitempty"`
}

func schemaContract(schema *ast.Schema) sdlContract {
	types := make([]sdlTypeContract, 0, len(schema.Types))
	for name, def := range schema.Types {
		if name == "" || name[0] == '_' {
			continue
		}
		item := sdlTypeContract{Kind: string(def.Kind), Name: name}
		for _, field := range def.Fields {
			item.Fields = append(item.Fields, fieldContract(field))
		}
		sort.Strings(item.Fields)
		for _, value := range def.EnumValues {
			item.Values = append(item.Values, value.Name)
		}
		sort.Strings(item.Values)
		types = append(types, item)
	}
	sort.Slice(types, func(i, j int) bool { return types[i].Name < types[j].Name })

	directives := make([]sdlDirectiveContract, 0, len(schema.Directives))
	for name, directive := range schema.Directives {
		item := sdlDirectiveContract{Name: name, Repeatable: directive.IsRepeatable}
		for _, argument := range directive.Arguments {
			item.Arguments = append(item.Arguments, argumentContract(argument))
		}
		sort.Strings(item.Arguments)
		for _, location := range directive.Locations {
			item.Locations = append(item.Locations, string(location))
		}
		sort.Strings(item.Locations)
		directives = append(directives, item)
	}
	sort.Slice(directives, func(i, j int) bool { return directives[i].Name < directives[j].Name })
	return sdlContract{Types: types, Directives: directives}
}

func fieldContract(field *ast.FieldDefinition) string {
	args := make([]string, 0, len(field.Arguments))
	for _, argument := range field.Arguments {
		args = append(args, argumentContract(argument))
	}
	sort.Strings(args)
	name := field.Name
	if len(args) > 0 {
		name += "(" + strings.Join(args, ",") + ")"
	}
	if field.Type != nil {
		name += ":" + field.Type.String()
	}
	return name
}

func argumentContract(argument *ast.ArgumentDefinition) string {
	value := argument.Name + ":" + argument.Type.String()
	if argument.DefaultValue != nil {
		value += "=" + argument.DefaultValue.String()
	}
	return value
}

func TestCompatGeneratedSDLContract(t *testing.T) {
	raw, err := NewRenderer(createMockMetadata(t)).Render()
	require.NoError(t, err)
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "compat.graphql", Input: raw})
	require.NoError(t, err)

	got, err := stdjson.Marshal(schemaContract(schema))
	require.NoError(t, err)
	want, err := os.ReadFile(filepath.Join("testdata", "compat", "generated_sdl_contract.json"))
	require.NoError(t, err)
	var golden struct {
		SHA256 string `json:"sha256"`
	}
	require.NoError(t, stdjson.Unmarshal(want, &golden))
	actual := fmt.Sprintf("%x", sha256.Sum256(got))
	if actual != golden.SHA256 {
		t.Fatalf("public SDL shape drifted: want sha256=%s got=%s; canonical contract=%s", golden.SHA256, actual, got)
	}
}

func TestCompatRealCorpusParsesAndValidatesOperation(t *testing.T) {
	root := filepath.Join("testdata", "compat")
	sdl, err := os.ReadFile(filepath.Join(root, "fish_ai_schema.graphql"))
	require.NoError(t, err)
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "fish-ai-api.schema.graphql", Input: string(sdl)})
	require.NoError(t, err)
	op, err := os.ReadFile(filepath.Join(root, "fish_ai_operation.graphql"))
	require.NoError(t, err)
	_, errs := gqlparser.LoadQuery(schema, string(op))
	require.Empty(t, errs)
}
