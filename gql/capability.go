package gql

import (
	"bytes"
	"crypto/sha256"
	stdjson "encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/jinzhu/inflection"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/formatter"
)

// HandlerKind identifies the only execution owner for a GraphQL field.
type HandlerKind string

const (
	HandlerSQL           HandlerKind = "sql"
	HandlerResolver      HandlerKind = "resolver"
	HandlerRemote        HandlerKind = "remote"
	HandlerProjection    HandlerKind = "projection"
	HandlerIntrospection HandlerKind = "introspection"
)

// Capability is the immutable execution contract of one schema coordinate.
type Capability struct {
	Coordinate string
	Parent     string
	Name       string
	Type       string
	Arguments  []Argument
	Handler    HandlerKind
	Planner    string
	Actions    []string
	Resource   string
	Cost       uint32
}

// Argument is the introspectable invocation contract of one field argument.
type Argument struct {
	Name    string
	Type    string
	Default string
}

type operationCapability struct {
	class   *protocol.Class
	actions []string
}

func buildOperationCatalog(metadata *Metadata) map[string]operationCapability {
	catalog := make(map[string]operationCapability)
	if metadata == nil {
		return catalog
	}
	renderer := NewRenderer(metadata)
	add := func(parent, name string, class *protocol.Class, action string) {
		catalog[parent+"."+name] = operationCapability{class: class, actions: []string{action}}
	}
	addMany := func(parent, name string, class *protocol.Class, actions ...string) {
		catalog[parent+"."+name] = operationCapability{class: class, actions: actions}
	}
	for _, name := range sortedNodeNames(metadata) {
		class := metadata.Nodes[name]
		if class == nil || name != class.Name || class.Table == "" {
			continue
		}
		if renderer.allows(class, protocol.OP_SELECT) {
			add("Query", queryField(name), class, protocol.OP_SELECT)
			add("Subscription", queryField(name), class, protocol.OP_SELECT)
		}
		if renderer.allows(class, protocol.OP_AGGREGATE) {
			add("Query", strcase.ToLowerCamel(name)+protocol.SUFFIX_STATS, class, protocol.OP_AGGREGATE)
		}
		create := renderer.allows(class, protocol.OP_INSERT) && len(renderer.writableFields(class, protocol.OP_INSERT)) > 0
		update := renderer.allows(class, protocol.OP_UPDATE) && len(renderer.writableFields(class, protocol.OP_UPDATE)) > 0
		plural := inflection.Plural(name)
		batch := plural
		if batch == name {
			batch += protocol.SUFFIX_MANY
		}
		if create {
			add("Mutation", protocol.CREATE+name, class, protocol.OP_INSERT)
			if plural != name {
				add("Mutation", protocol.CREATE+plural, class, protocol.OP_INSERT)
			}
			if update {
				addMany("Mutation", protocol.UPSERT+plural, class, protocol.OP_INSERT, protocol.OP_UPDATE)
			}
		}
		if update {
			add("Mutation", protocol.UPDATE+name, class, protocol.OP_UPDATE)
			add("Mutation", protocol.UPDATE+name+protocol.SUFFIX_BY_PK, class, protocol.OP_UPDATE)
			add("Mutation", protocol.UPDATE+batch, class, protocol.OP_UPDATE)
		}
		if renderer.allows(class, protocol.OP_DELETE) {
			add("Mutation", protocol.DELETE+name, class, protocol.OP_DELETE)
			add("Mutation", protocol.DELETE+name+protocol.SUFFIX_BY_PK, class, protocol.OP_DELETE)
			add("Mutation", protocol.DELETE+batch, class, protocol.OP_DELETE)
		}
		if renderer.allows(class, protocol.OP_RESTORE) && class.SoftDelete != nil {
			add("Mutation", "restore"+name+protocol.SUFFIX_BY_PK, class, protocol.OP_RESTORE)
			add("Mutation", "restore"+batch, class, protocol.OP_RESTORE)
		}
	}
	return catalog
}

// Snapshot is the immutable schema and capability view used by an Engine.
type Snapshot struct {
	schema       string
	fingerprint  string
	capabilities []Capability
	index        map[string]Capability
}

func (my *Snapshot) Schema() string      { return my.schema }
func (my *Snapshot) Fingerprint() string { return my.fingerprint }

func (my *Snapshot) Capabilities() []Capability {
	out := make([]Capability, len(my.capabilities))
	for i, capability := range my.capabilities {
		out[i] = cloneCapability(capability)
	}
	return out
}

func (my *Snapshot) Capability(coordinate string) (Capability, bool) {
	capability, ok := my.index[coordinate]
	return cloneCapability(capability), ok
}

func cloneCapability(capability Capability) Capability {
	capability.Arguments = append([]Argument(nil), capability.Arguments...)
	capability.Actions = append([]string(nil), capability.Actions...)
	return capability
}

func buildSnapshot(executor *Executor) (*Snapshot, error) {
	schema := formatSchema(executor.schema)
	capabilities := make([]Capability, 0, len(executor.schema.Types)*4)
	index := make(map[string]Capability)

	for _, parent := range sortedDefinitions(executor.schema) {
		if parent.Kind != ast.Object && parent.Kind != ast.Interface {
			continue
		}
		for _, field := range parent.Fields {
			capability, err := executor.fieldCapability(parent, field)
			if err != nil {
				return nil, err
			}
			if _, exists := index[capability.Coordinate]; exists {
				return nil, fmt.Errorf("schema coordinate重复: %s", capability.Coordinate)
			}
			index[capability.Coordinate] = capability
			capabilities = append(capabilities, capability)
		}
	}
	for name := range executor.resolvers {
		if capability, ok := index[name]; !ok || capability.Handler != HandlerResolver {
			return nil, fmt.Errorf("resolver没有可执行schema coordinate: %s", name)
		}
	}
	for name := range executor.remotes {
		if capability, ok := index[name]; !ok || capability.Handler != HandlerRemote {
			return nil, fmt.Errorf("remote没有可执行schema coordinate: %s", name)
		}
	}
	sort.Slice(capabilities, func(i, j int) bool { return capabilities[i].Coordinate < capabilities[j].Coordinate })
	contract, err := snapshotContract(executor, schema, capabilities)
	if err != nil {
		return nil, err
	}
	fingerprint := sha256.Sum256(contract)
	return &Snapshot{
		schema: schema, fingerprint: fmt.Sprintf("%x", fingerprint),
		capabilities: capabilities, index: index,
	}, nil
}

func validateSchemaArtifact(generated, artifact string) error {
	want, err := canonicalSchema(generated)
	if err != nil {
		return fmt.Errorf("生成schema无效: %w", err)
	}
	got, err := canonicalSchema(artifact)
	if err != nil {
		return fmt.Errorf("schema artifact无效: %w", err)
	}
	if got != want {
		return fmt.Errorf("schema artifact与当前catalog不一致")
	}
	return nil
}

func canonicalSchema(source string) (string, error) {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "schema.graphql", Input: source})
	if err != nil {
		return "", err
	}
	return schemaWithoutComments(formatSchema(schema)), nil
}

func formatSchema(schema *ast.Schema) string {
	var out bytes.Buffer
	formatter.NewFormatter(&out).FormatSchema(schema)
	return out.String()
}

func snapshotContract(executor *Executor, schema string, capabilities []Capability) ([]byte, error) {
	classes := make([]*protocol.Class, 0)
	for _, name := range sortedNodeNames(executor.metadata) {
		class := executor.metadata.Nodes[name]
		if class != nil && name == class.Name {
			classes = append(classes, class)
		}
	}
	runtime := struct {
		Dialect       string
		Schema        string
		DefaultLimit  int
		MaxDepth      int
		Introspection bool
		PersistedOnly bool
		SearchMode    string
		SearchConfig  string
	}{}
	if executor.compiler != nil && executor.compiler.dialect != nil {
		runtime.Dialect = executor.compiler.dialect.Name()
	}
	if executor.metadata != nil && executor.metadata.cfg != nil {
		cfg := executor.metadata.cfg
		runtime.Schema = cfg.Schema.Schema
		runtime.DefaultLimit = cfg.Schema.DefaultLimit
		runtime.MaxDepth = cfg.Schema.MaxDepth
		runtime.Introspection = cfg.Schema.Introspection
		runtime.PersistedOnly = cfg.Schema.PersistedOnly
		runtime.SearchMode, runtime.SearchConfig = executor.metadata.SearchMode()
	}
	return stdjson.Marshal(struct {
		Schema       string
		Capabilities []Capability
		Classes      []*protocol.Class
		Runtime      any
	}{schemaWithoutComments(schema), capabilities, classes, runtime})
}

func sortedNodeNames(metadata *Metadata) []string {
	names := make([]string, 0, len(metadata.Nodes))
	for name := range metadata.Nodes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func sortedDefinitions(schema *ast.Schema) []*ast.Definition {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	definitions := make([]*ast.Definition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, schema.Types[name])
	}
	return definitions
}

func (my *Executor) fieldCapability(parent *ast.Definition, field *ast.FieldDefinition) (Capability, error) {
	coordinate := parent.Name + "." + field.Name
	capability := Capability{
		Coordinate: coordinate, Parent: parent.Name, Name: field.Name,
		Type: field.Type.String(), Arguments: fieldArguments(field), Handler: HandlerProjection, Cost: 1,
	}
	if strings.HasPrefix(parent.Name, "__") || strings.HasPrefix(field.Name, "__") {
		capability.Handler = HandlerIntrospection
		return capability, nil
	}
	if _, ok := my.resolvers[coordinate]; ok {
		capability.Handler = HandlerResolver
		return capability, nil
	}
	if _, ok := my.remotes[coordinate]; ok {
		capability.Handler = HandlerRemote
		return capability, nil
	}
	if class, ok := my.metadata.GetNode(parent.Name); ok {
		declared, exists := class.Fields[field.Name]
		if !exists {
			return Capability{}, fmt.Errorf("schema字段没有handler: %s", coordinate)
		}
		if declared.Resolver != "" {
			if _, exists := my.resolvers[declared.Resolver]; !exists {
				return Capability{}, fmt.Errorf("schema字段%s引用未注册resolver: %s", coordinate, declared.Resolver)
			}
			capability.Handler = HandlerResolver
			return capability, nil
		}
		if declared.Remote != nil {
			if _, exists := my.remotes[declared.Remote.Source]; !exists {
				return Capability{}, fmt.Errorf("schema字段%s引用未注册remote: %s", coordinate, declared.Remote.Source)
			}
			capability.Handler = HandlerRemote
			return capability, nil
		}
		capability.Handler, capability.Resource = HandlerSQL, class.Name
		capability.Actions = []string{protocol.OP_SELECT}
		capability.Planner = "sql"
		return capability, nil
	}
	if parent == my.schema.Query || parent == my.schema.Mutation || parent == my.schema.Subscription {
		if field.Name == "_empty" {
			return capability, nil
		}
		operation, ok := my.metadata.operations[coordinate]
		if !ok {
			return Capability{}, fmt.Errorf("schema字段没有handler: %s", coordinate)
		}
		capability.Handler, capability.Resource = HandlerSQL, operation.class.Name
		capability.Actions = append([]string(nil), operation.actions...)
		capability.Planner = "sql"
	}
	return capability, nil
}

func fieldArguments(field *ast.FieldDefinition) []Argument {
	arguments := make([]Argument, 0, len(field.Arguments))
	for _, argument := range field.Arguments {
		item := Argument{Name: argument.Name, Type: argument.Type.String()}
		if argument.DefaultValue != nil {
			item.Default = argument.DefaultValue.String()
		}
		arguments = append(arguments, item)
	}
	return arguments
}

func schemaWithoutComments(schema string) string {
	lines := strings.Split(schema, "\n")
	out := lines[:0]
	for _, line := range lines {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
