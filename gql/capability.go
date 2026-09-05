package gql

import (
	"bytes"
	"cmp"
	"crypto/sha256"
	stdjson "encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/compiler"
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
	HandlerProjection    HandlerKind = "projection"
	HandlerIntrospection HandlerKind = "introspection"
)

// Capability is the immutable execution contract of one schema coordinate.
type Capability struct {
	Coordinate      string        `json:"coordinate"`
	Parent          string        `json:"parent"`
	Name            string        `json:"name"`
	Type            string        `json:"type"`
	Arguments       []Argument    `json:"arguments"`
	Handler         HandlerKind   `json:"handler"`
	Planner         string        `json:"planner"`
	Actions         []string      `json:"actions"`
	Resource        string        `json:"resource"`
	Cost            uint32        `json:"cost"`
	PolicyAware     bool          `json:"policyAware"`
	Requires        []string      `json:"requires"`
	Timeout         time.Duration `json:"timeout"`
	RetrySafe       bool          `json:"retrySafe"`
	IdempotencyKey  string        `json:"idempotencyKey"`
	MaxBatch        int           `json:"maxBatch"`
	SemanticKind    SemanticKind  `json:"semanticKind,omitempty"`
	RefreshRequired bool          `json:"refreshRequired,omitempty"`
}

// Argument is the introspectable invocation contract of one field argument.
type Argument struct {
	Name    string `json:"name"`
	Type    string `json:"type"`
	Default string `json:"default"`
}

type operationCapability struct {
	class     *protocol.Class
	actions   []string
	operation string
	shape     compiler.OperationShape
}

func buildOperationCatalog(metadata *Metadata) map[string]operationCapability {
	catalog := make(map[string]operationCapability)
	if metadata == nil {
		return catalog
	}
	add := func(parent, name string, class *protocol.Class, shape compiler.OperationShape, operation string, actions ...string) {
		catalog[parent+"."+name] = operationCapability{class: class, actions: actions, operation: operation, shape: shape}
	}
	for _, name := range slices.Sorted(maps.Keys(metadata.Nodes)) {
		class := metadata.Nodes[name]
		if class == nil || name != class.Name || class.Table == "" || class.Hidden {
			continue
		}
		if catalogAllows(class, protocol.OP_SELECT) {
			add("Query", queryField(name), class, compiler.ShapeSingle, protocol.OP_SELECT, protocol.OP_SELECT)
			add("Subscription", queryField(name), class, compiler.ShapeSingle, protocol.OP_SELECT, protocol.OP_SELECT)
			if len(class.PrimaryKeyFields()) > 0 {
				connection := strcase.ToLowerCamel(name) + protocol.SUFFIX_CONNECTION
				add("Query", connection, class, compiler.ShapeSingle, protocol.OP_SELECT, protocol.OP_SELECT)
				add("Subscription", connection, class, compiler.ShapeSingle, protocol.OP_SELECT, protocol.OP_SELECT)
			}
		}
		if catalogAllows(class, protocol.OP_AGGREGATE) {
			add("Query", strcase.ToLowerCamel(name)+protocol.SUFFIX_STATS, class, compiler.ShapeSingle, protocol.OP_AGGREGATE, protocol.OP_AGGREGATE)
		}
		insertFields := writableFields(metadata, class, protocol.OP_INSERT)
		updateFields := writableFields(metadata, class, protocol.OP_UPDATE)
		create := catalogAllows(class, protocol.OP_INSERT) && (len(insertFields) > 0 || class.CanInsertWithoutInput())
		update := catalogAllows(class, protocol.OP_UPDATE) && len(updateFields) > 0
		plural := inflection.Plural(name)
		batch := mutationBatchName(name)
		if create {
			add("Mutation", protocol.CREATE+name, class, compiler.ShapeSingle, protocol.CREATE, protocol.OP_INSERT)
			if plural != name && len(insertFields) > 0 {
				add("Mutation", protocol.CREATE+plural, class, compiler.ShapeBulk, protocol.CREATE, protocol.OP_INSERT)
			}
			upsertFields := slices.DeleteFunc(append([]string(nil), insertFields...), func(name string) bool {
				return !slices.Contains(updateFields, name)
			})
			if update && len(upsertFields) > 0 && len(class.UpsertConflictTargets()) > 0 {
				add("Mutation", protocol.UPSERT+plural, class, compiler.ShapeBulk, protocol.UPSERT, protocol.OP_INSERT, protocol.OP_UPDATE)
			}
		}
		if update {
			if len(class.PrimaryKeyFields()) > 0 {
				add("Mutation", protocol.UPDATE+name, class, compiler.ShapeSingleByPK, protocol.OP_UPDATE, protocol.OP_UPDATE)
			}
			add("Mutation", protocol.UPDATE+batch, class, compiler.ShapeBulkResult, protocol.OP_UPDATE, protocol.OP_UPDATE)
		}
		if catalogAllows(class, protocol.OP_DELETE) {
			add("Mutation", protocol.DELETE+name, class, compiler.ShapeSingle, protocol.OP_DELETE, protocol.OP_DELETE)
			if len(class.PrimaryKeyFields()) > 0 {
				add("Mutation", protocol.DELETE+name+protocol.SUFFIX_BY_PK, class, compiler.ShapeSingleByPK, protocol.OP_DELETE, protocol.OP_DELETE)
			}
			add("Mutation", protocol.DELETE+batch, class, compiler.ShapeBulkResult, protocol.OP_DELETE, protocol.OP_DELETE)
		}
		if catalogAllows(class, protocol.OP_RESTORE) && class.SoftDelete != nil {
			if len(class.PrimaryKeyFields()) > 0 {
				add("Mutation", "restore"+name+protocol.SUFFIX_BY_PK, class, compiler.ShapeSingleByPK, protocol.OP_RESTORE, protocol.OP_RESTORE)
			}
			add("Mutation", "restore"+batch, class, compiler.ShapeBulkResult, protocol.OP_RESTORE, protocol.OP_RESTORE)
		}
		if operationAllowedPurge(class) {
			if len(class.PrimaryKeyFields()) > 0 {
				add("Mutation", protocol.PURGE+name+protocol.SUFFIX_BY_PK, class, compiler.ShapeSingleByPK, protocol.OP_PURGE, protocol.OP_PURGE)
			}
			add("Mutation", protocol.PURGE+batch, class, compiler.ShapeBulkResult, protocol.OP_PURGE, protocol.OP_PURGE)
		}
	}
	return catalog
}

func catalogAllows(class *protocol.Class, operation string) bool {
	if class == nil || class.ReadOnly && operation != protocol.OP_SELECT && operation != protocol.OP_AGGREGATE {
		return false
	}
	return len(class.Operations) == 0 || operationAllowedInList(class.Operations, operation)
}

func operationAllowedPurge(class *protocol.Class) bool {
	return class != nil && !class.ReadOnly && class.SoftDelete != nil && operationAllowedInList(class.Operations, protocol.OP_PURGE)
}

func operationAllowedInList(operations []string, operation string) bool {
	for _, candidate := range operations {
		if strings.EqualFold(candidate, operation) {
			return true
		}
	}
	return false
}

func fieldHidden(metadata *Metadata, field *protocol.Field) bool {
	if metadata == nil || metadata.cfg == nil || metadata.cfg.Metadata.ShowThrough || field == nil {
		return false
	}
	if field.IsThrough {
		return true
	}
	refType := field.Type
	if field.Relation != nil && field.Relation.TargetClass != "" {
		refType = field.Relation.TargetClass
	}
	refClass := metadata.Nodes[refType]
	return refClass != nil && refClass.IsThrough
}

func writableFields(metadata *Metadata, class *protocol.Class, operation string) []string {
	var scoped map[string]bool
	if len(class.Scope) > 0 {
		scoped = make(map[string]bool, len(class.Scope))
		for _, rule := range class.Scope {
			scoped[rule.Column] = true
		}
	}
	names := make([]string, 0, len(class.Fields))
	for _, fieldName := range slices.Sorted(maps.Keys(class.Fields)) {
		field := class.Fields[fieldName]
		if field == nil || fieldName != field.Name {
			continue
		}
		catalogBacked := field.Catalog != nil && field.Catalog.Type.Kind != ""
		generatedPrimary := field.IsPrimary && (!catalogBacked || class.IDGenerator != "" && class.IDGenerator != "database")
		if field.Virtual || field.Column == "" || generatedPrimary || scoped[field.Column] || field.ReadOnly ||
			(field.Catalog != nil && field.Catalog.Generation != nil) ||
			(operation == protocol.OP_INSERT && field.InsertPreset != nil) ||
			(operation == protocol.OP_UPDATE && field.UpdatePreset != nil) || auditColumn(field.Column) || fieldHidden(metadata, field) {
			continue
		}
		names = append(names, fieldName)
	}
	return names
}

// Snapshot is the immutable schema and capability view used by an Engine.
type Snapshot struct {
	schema       string
	fingerprint  string
	capabilities []Capability
	index        map[string]Capability
}

// NewSnapshotArtifact validates and freezes a remotely supplied endpoint
// manifest without exposing Snapshot's mutable representation.
func NewSnapshotArtifact(schema, fingerprint string, capabilities []Capability) (*Snapshot, error) {
	if schema == "" || fingerprint == "" {
		return nil, fmt.Errorf("snapshot schema and fingerprint are required")
	}
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "endpoint.graphql", Input: schema})
	if err != nil {
		return nil, fmt.Errorf("endpoint schema: %w", err)
	}
	computed, fingerprintErr := artifactFingerprint(schema, capabilities, parsed)
	snapshot, err := newSnapshotArtifact(schema, computed, capabilities, parsed)
	if err != nil {
		return nil, err
	}
	if fingerprintErr != nil {
		return nil, fmt.Errorf("endpoint fingerprint: %w", fingerprintErr)
	}
	if fingerprint != computed {
		return nil, fmt.Errorf("endpoint fingerprint mismatch: advertised %s, computed %s", fingerprint, computed)
	}
	return snapshot, nil
}

func newSnapshotArtifact(schema, fingerprint string, capabilities []Capability, parsed *ast.Schema) (*Snapshot, error) {
	disableUnsupportedDirectives(parsed)
	if err := validateExecutableDirectives(parsed); err != nil {
		return nil, err
	}
	capabilities = append([]Capability(nil), capabilities...)
	if err := validateRootCapabilities(parsed, capabilities); err != nil {
		return nil, err
	}
	capabilities = completeCapabilities(parsed, capabilities, HandlerProjection, "endpoint")
	index := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		if capability.Coordinate == "" || index[capability.Coordinate].Coordinate != "" {
			return nil, fmt.Errorf("endpoint capability coordinate invalid or duplicated: %q", capability.Coordinate)
		}
		parent := parsed.Types[capability.Parent]
		var field *ast.FieldDefinition
		if parent != nil {
			field = parent.Fields.ForName(capability.Name)
		}
		if field == nil || capability.Coordinate != capability.Parent+"."+capability.Name {
			return nil, fmt.Errorf("endpoint capability %q is not present in endpoint schema", capability.Coordinate)
		}
		if capability.Handler == "" || capability.Type != field.Type.String() || !slices.Equal(capability.Arguments, fieldArguments(field)) {
			return nil, fmt.Errorf("endpoint capability %q does not match endpoint schema", capability.Coordinate)
		}
		index[capability.Coordinate] = cloneCapability(capability)
	}
	copy := make([]Capability, len(capabilities))
	for i, capability := range capabilities {
		copy[i] = cloneCapability(capability)
	}
	return &Snapshot{schema: schema, fingerprint: fingerprint, capabilities: copy, index: index}, nil
}

func (my *Snapshot) Schema() string      { return my.schema }
func (my *Snapshot) Fingerprint() string { return my.fingerprint }

func (my *Snapshot) Capabilities() []Capability {
	return cloneCapabilities(my.capabilities)
}

func cloneCapabilities(capabilities []Capability) []Capability {
	out := make([]Capability, len(capabilities))
	for i, capability := range capabilities {
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
	capability.Requires = append([]string(nil), capability.Requires...)
	return capability
}

func nestedRelationWrite(class *protocol.Class, operation string) bool {
	if class == nil || (operation != protocol.CREATE && operation != protocol.OP_UPDATE) {
		return false
	}
	for _, field := range class.Fields {
		if field != nil && field.Relation != nil {
			return true
		}
	}
	return false
}

func completeCapabilities(schema *ast.Schema, capabilities []Capability, fallback HandlerKind, planner string) []Capability {
	seen := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		seen[capability.Coordinate] = struct{}{}
	}
	for _, parent := range sortedDefinitions(schema) {
		if parent.Kind != ast.Object && parent.Kind != ast.Interface {
			continue
		}
		for _, field := range parent.Fields {
			coordinate := parent.Name + "." + field.Name
			if _, exists := seen[coordinate]; exists {
				continue
			}
			handler := fallback
			if strings.HasPrefix(parent.Name, "__") || strings.HasPrefix(field.Name, "__") {
				handler = HandlerIntrospection
			}
			capabilities = append(capabilities, Capability{
				Coordinate: coordinate, Parent: parent.Name, Name: field.Name, Type: field.Type.String(),
				Arguments: fieldArguments(field), Handler: handler, Planner: planner, Cost: 1,
				SemanticKind: defaultSemanticKind(parent.Name, handler),
			})
		}
	}
	slices.SortFunc(capabilities, func(a, b Capability) int { return cmp.Compare(a.Coordinate, b.Coordinate) })
	return capabilities
}

func validateRootCapabilities(schema *ast.Schema, capabilities []Capability) error {
	provided := make(map[string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		provided[capability.Coordinate] = struct{}{}
	}
	for _, root := range []*ast.Definition{schema.Query, schema.Mutation, schema.Subscription} {
		if root == nil {
			continue
		}
		for _, field := range root.Fields {
			coordinate := root.Name + "." + field.Name
			if strings.HasPrefix(field.Name, "__") {
				continue
			}
			if _, ok := provided[coordinate]; !ok {
				return fmt.Errorf("endpoint executable root capability missing: %s", coordinate)
			}
		}
	}
	return nil
}

// ArtifactFingerprint computes the stable identity of a schema artifact.
// Metadata and transport details are deliberately excluded: producers and
// consumers must derive the same value from the public artifact contract.
func ArtifactFingerprint(schema string, capabilities []Capability) (string, error) {
	parsed, err := gqlparser.LoadSchema(&ast.Source{Name: "artifact.graphql", Input: schema})
	if err != nil {
		return "", err
	}
	return artifactFingerprint(schema, capabilities, parsed)
}

func artifactFingerprint(schema string, capabilities []Capability, parsed *ast.Schema) (string, error) {
	canonical := schemaWithoutComments(formatSchema(parsed))
	capabilities = completeCapabilities(parsed, append([]Capability(nil), capabilities...), HandlerProjection, "endpoint")
	ordered := make([]Capability, len(capabilities))
	for i, capability := range capabilities {
		ordered[i] = cloneCapability(capability)
		if ordered[i].Arguments == nil {
			ordered[i].Arguments = []Argument{}
		}
		if ordered[i].Actions == nil {
			ordered[i].Actions = []string{}
		}
		if ordered[i].Requires == nil {
			ordered[i].Requires = []string{}
		}
	}
	slices.SortStableFunc(ordered, func(a, b Capability) int { return cmp.Compare(a.Coordinate, b.Coordinate) })
	contract, err := stdjson.Marshal(struct {
		Schema       string
		Capabilities []Capability
	}{canonical, ordered})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(contract)
	return fmt.Sprintf("%x", digest), nil
}

func buildSnapshot(engine *Engine) (*Snapshot, error) {
	if err := validateExecutableDirectives(engine.schema); err != nil {
		return nil, err
	}
	schema := formatSchema(engine.schema)
	capabilities := make([]Capability, 0, len(engine.schema.Types)*4)
	index := make(map[string]Capability)

	for _, parent := range sortedDefinitions(engine.schema) {
		if parent.Kind != ast.Object && parent.Kind != ast.Interface {
			continue
		}
		for _, field := range parent.Fields {
			capability, err := engine.fieldCapability(parent, field)
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
	for name := range engine.resolvers {
		if capability, ok := index[name]; !ok || capability.Handler != HandlerResolver {
			return nil, fmt.Errorf("resolver没有可执行schema coordinate: %s", name)
		}
	}

	slices.SortFunc(capabilities, func(a, b Capability) int { return cmp.Compare(a.Coordinate, b.Coordinate) })
	fingerprint, err := ArtifactFingerprint(schema, capabilities)
	if err != nil {
		return nil, err
	}
	return &Snapshot{
		schema: schema, fingerprint: fingerprint,
		capabilities: capabilities, index: index,
	}, nil
}

func validateExecutableDirectives(schema *ast.Schema) error {
	for name, directive := range schema.Directives {
		if name == "skip" || name == "include" {
			continue
		}
		for _, location := range directive.Locations {
			switch location {
			case ast.LocationQuery, ast.LocationMutation, ast.LocationSubscription, ast.LocationField,
				ast.LocationFragmentDefinition, ast.LocationFragmentSpread, ast.LocationInlineFragment, ast.LocationVariableDefinition:
				return fmt.Errorf("unsupported executable directive @%s", name)
			}
		}
	}
	return nil
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

func sortedDefinitions(schema *ast.Schema) []*ast.Definition {
	names := slices.Sorted(maps.Keys(schema.Types))
	definitions := make([]*ast.Definition, 0, len(names))
	for _, name := range names {
		definitions = append(definitions, schema.Types[name])
	}
	return definitions
}

func (my *Engine) fieldCapability(parent *ast.Definition, field *ast.FieldDefinition) (Capability, error) {
	coordinate := parent.Name + "." + field.Name
	capability := Capability{
		Coordinate: coordinate, Parent: parent.Name, Name: field.Name,
		Type: field.Type.String(), Arguments: fieldArguments(field), Handler: HandlerProjection, Cost: 1,
		SemanticKind: defaultSemanticKind(parent.Name, HandlerProjection),
	}
	if strings.HasPrefix(parent.Name, "__") || strings.HasPrefix(field.Name, "__") {
		capability.Handler = HandlerIntrospection
		return capability, nil
	}
	if resolver, ok := my.resolvers[coordinate]; ok {
		capability.Handler = HandlerResolver
		define := resolver.Define()
		capability.Resource = define.Resource
		capability.Actions = append([]string(nil), define.Actions...)
		capability.PolicyAware = define.PolicyAware
		capability.SemanticKind = deriveSemanticKind(define)
		if !validSemanticKind(capability.SemanticKind) {
			return Capability{}, fmt.Errorf("resolver %s has invalid semantic kind %q", coordinate, capability.SemanticKind)
		}
		capability.Requires = append([]string(nil), define.Requires...)
		capability.Timeout = define.Timeout
		capability.RetrySafe, capability.IdempotencyKey = define.RetrySafe, define.IdempotencyKey
		if _, ok := resolver.(BatchResolver); ok {
			capability.MaxBatch = handlerBatchLimit(resolver)
		}
		if cost := define.Cost; cost > 0 {
			capability.Cost = cost
		}
		if capability.Resource == "" {
			if class, exists := my.metadata.GetNode(parent.Name); exists {
				capability.Resource, capability.Actions = class.Name, []string{protocol.OP_SELECT}
			}
		}
		if my.policy != nil && (capability.Resource == "" || len(capability.Actions) == 0) {
			return Capability{}, fmt.Errorf("resolver %s requires explicit policy resource/action", coordinate)
		}
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

		resource := class.Name
		if declared.Relation != nil {
			resource = declared.Relation.TargetClass
		}
		capability.Handler, capability.Resource = HandlerSQL, resource
		capability.Actions = []string{protocol.OP_SELECT}
		capability.Planner = "sql"
		capability.SemanticKind = SemanticProjection
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
		if nestedRelationWrite(operation.class, operation.operation) {
			capability.RefreshRequired = true
		}
	}
	return capability, nil
}

func defaultSemanticKind(parent string, handler HandlerKind) SemanticKind {
	if handler == HandlerIntrospection {
		return SemanticProjection
	}
	if parent == "Mutation" {
		return SemanticCommand
	}
	if parent == "Query" || parent == "Subscription" {
		return SemanticPrimitive
	}
	if handler == HandlerProjection {
		return SemanticProjection
	}
	return SemanticPrimitive
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
