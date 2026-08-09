package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
)

type SchemaArtifact struct {
	Schema                string
	Fingerprint           string
	StructuralFingerprint string
	PermissionSignature   string
	PolicyRevision        uint64
	RouteRevision         uint64
}

type SchemaProfile struct {
	Name string
	SchemaArtifact
}

// GenerateSchemaProfile materializes a named, versioned codegen artifact for
// the supplied trusted identity.
func (my *Engine) GenerateSchemaProfile(ctx context.Context, name string) (SchemaProfile, error) {
	if strings.TrimSpace(name) == "" {
		return SchemaProfile{}, fmt.Errorf("schema profile name is required")
	}
	artifact, err := my.EffectiveSchema(ctx)
	return SchemaProfile{Name: name, SchemaArtifact: artifact}, err
}

// ValidateOperation validates a persisted operation against this profile. It
// is a publishing check only; runtime policy authorization is never skipped.
func (profile SchemaProfile) ValidateOperation(query string) error {
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: profile.Name + ".graphql", Input: profile.Schema})
	if err != nil {
		return err
	}
	_, errors := gqlparser.LoadQueryWithRules(schema, query, validatorrules.NewDefaultRules())
	if len(errors) > 0 {
		return errors
	}
	return nil
}

// CheckProfileCompatibility rejects schema changes that break clients
// generated from the previous profile.
func CheckProfileCompatibility(previous, next SchemaProfile) error {
	before, err := gqlparser.LoadSchema(&ast.Source{Name: previous.Name + ".graphql", Input: previous.Schema})
	if err != nil {
		return fmt.Errorf("previous schema profile: %w", err)
	}
	after, err := gqlparser.LoadSchema(&ast.Source{Name: next.Name + ".graphql", Input: next.Schema})
	if err != nil {
		return fmt.Errorf("next schema profile: %w", err)
	}
	var breaking []string
	for name, oldType := range before.Types {
		if strings.HasPrefix(name, "__") {
			continue
		}
		newType := after.Types[name]
		if newType == nil {
			breaking = append(breaking, "removed type "+name)
			continue
		}
		for _, oldField := range oldType.Fields {
			newField := newType.Fields.ForName(oldField.Name)
			if newField == nil {
				breaking = append(breaking, "removed field "+name+"."+oldField.Name)
			} else if newField.Type.String() != oldField.Type.String() {
				breaking = append(breaking, "changed field "+name+"."+oldField.Name)
			} else if oldType.Kind != ast.InputObject {
				breaking = append(breaking, incompatibleArguments(name+"."+oldField.Name, oldField.Arguments, newField.Arguments)...)
			}
		}
		if oldType.Kind == ast.InputObject {
			for _, field := range newType.Fields {
				if oldType.Fields.ForName(field.Name) == nil && field.Type.NonNull && field.DefaultValue == nil {
					breaking = append(breaking, "added required input field "+name+"."+field.Name)
				}
			}
		}
		if oldType.Kind == ast.Enum {
			for _, value := range oldType.EnumValues {
				if newType.EnumValues.ForName(value.Name) == nil {
					breaking = append(breaking, "removed enum value "+name+"."+value.Name)
				}
			}
		}
	}
	if len(breaking) > 0 {
		sort.Strings(breaking)
		return fmt.Errorf("incompatible schema profile: %s", strings.Join(breaking, ", "))
	}
	return nil
}

func incompatibleArguments(coordinate string, before, after ast.ArgumentDefinitionList) []string {
	var breaking []string
	for _, argument := range before {
		next := after.ForName(argument.Name)
		if next == nil {
			breaking = append(breaking, "removed argument "+coordinate+"("+argument.Name+")")
		} else if next.Type.String() != argument.Type.String() {
			breaking = append(breaking, "changed argument "+coordinate+"("+argument.Name+")")
		}
	}
	for _, argument := range after {
		if before.ForName(argument.Name) == nil && argument.Type.NonNull && argument.DefaultValue == nil {
			breaking = append(breaking, "added required argument "+coordinate+"("+argument.Name+")")
		}
	}
	return breaking
}

type effectiveView struct {
	artifact SchemaArtifact
	schema   *ast.Schema
	intro    *intro.Handler
}

var allFieldUses = []FieldUse{
	FieldUseSelect, FieldUseFilter, FieldUseOrder, FieldUseGroup,
	FieldUseAggregate, FieldUseJoin, FieldUseWrite, FieldUseReturn,
	FieldUseInternalDependency,
}

// EffectiveSchema returns the exact identity-specific schema artifact used by
// introspection. Runtime authorization remains mandatory on every handler.
func (my *Engine) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
	if my.tenantRouter != nil {
		target, routed, err := my.tenantDispatch(ctx)
		if err != nil {
			return SchemaArtifact{}, err
		}
		return target.EffectiveSchema(routed)
	}
	if _, err := my.Snapshot(); err != nil {
		return SchemaArtifact{}, err
	}
	view, err := my.effectiveSchema(ctx)
	if err != nil {
		return SchemaArtifact{}, err
	}
	return view.artifact, nil
}

func (my *Engine) effectiveIntrospection(ctx context.Context) (*intro.Handler, error) {
	view, err := my.effectiveSchema(ctx)
	if err != nil {
		return nil, err
	}
	return view.intro, nil
}

func (my *Engine) effectiveSchema(ctx context.Context) (*effectiveView, error) {
	_, err := my.Snapshot()
	if err != nil {
		return nil, err
	}
	provider, identity, builtin, err := my.effectivePolicy(ctx)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return my.baseView, nil
	}
	key := my.effectiveSchemaKey(ctx, identity)
	cacheable := builtin || identity.PolicyRevision() != 0
	if cacheable {
		my.effectiveMu.Lock()
		cached := my.effective[key]
		my.effectiveMu.Unlock()
		if cached != nil {
			return cached, nil
		}
	}
	route, _ := PinnedRoute(ctx)
	if route.Tenant == "" {
		route.Tenant = identity.Tenant()
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "effective.graphql", Input: my.snapshot.Schema()})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]map[FieldUse][]string)
	writes := make(map[string]map[string][]string)
	denied := make(map[string]bool)
	var signatures []string
	for _, capability := range my.snapshot.Capabilities() {
		if capability.Resource == "" || len(capability.Actions) == 0 || capability.Parent != "Query" && capability.Parent != "Mutation" && capability.Parent != "Subscription" {
			continue
		}
		if builtin {
			if class, ok := my.metadata.GetNode(capability.Resource); !ok || class.Permissions == nil {
				continue
			}
		}
		request := PolicyRequest{Identity: identity, Route: route, Coordinate: capability.Coordinate, Resource: capability.Resource, Actions: capability.Actions, FieldUses: allFieldUses, Values: scopeValues(ctx)}
		decision, err := provider.Decide(ctx, request)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", capability.Coordinate, err)
		}
		decision, err = NormalizeDecision(request, decision)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", capability.Coordinate, err)
		}
		if revision := identity.PolicyRevision(); revision != 0 && decision.PolicyRevision != revision {
			return nil, fmt.Errorf("policy %s revision mismatch", capability.Coordinate)
		}
		class, governed := my.metadata.GetNode(capability.Resource)
		// nil means the provider does not shape fields; a non-nil map is an
		// explicit shape, including an empty deny-all shape.
		hasFieldShape := decision.Fields != nil
		if governed && hasFieldShape && allowed[class.Name] == nil {
			allowed[class.Name] = make(map[FieldUse][]string)
		}
		signatures = append(signatures, decision.PermissionSignature+":"+decision.ShapeSignature)
		if !decision.Allow {
			denied[capability.Coordinate] = true
			continue
		}
		if capability.Handler != HandlerSQL && hasRowPolicy(decision) && !capability.PolicyAware {
			return nil, fmt.Errorf("policy %s requires a policy-aware handler", capability.Coordinate)
		}
		if governed && hasFieldShape {
			if err := ValidateDecisionCatalog(class, decision); err != nil {
				return nil, err
			}
			if class.SoftDelete != nil && !allowedColumn(decision.Fields[FieldUseFilter], class.Fields[class.SoftDelete.Column]) {
				parent, name, _ := strings.Cut(capability.Coordinate, ".")
				if definition := schema.Types[parent]; definition != nil {
					if field := definition.Fields.ForName(name); field != nil {
						field.Arguments = removeSchemaArgument(field.Arguments, protocol.DELETION)
					}
				}
			}
			uses := allowed[class.Name]
			if uses == nil {
				uses = make(map[FieldUse][]string)
				allowed[class.Name] = uses
			}
			for use, fields := range decision.Fields {
				if use != FieldUseWrite {
					uses[use] = append(uses[use], fields...)
				}
			}
			if len(decision.Fields[FieldUseWrite]) > 0 {
				if writes[class.Name] == nil {
					writes[class.Name] = make(map[string][]string)
				}
				for _, action := range capability.Actions {
					if action == protocol.OP_INSERT || action == protocol.OP_UPDATE {
						writes[class.Name][action] = append(writes[class.Name][action], decision.Fields[FieldUseWrite]...)
					}
				}
			}
		}
	}
	for coordinate := range denied {
		parent, name, _ := strings.Cut(coordinate, ".")
		if definition := schema.Types[parent]; definition != nil {
			definition.Fields = removeSchemaField(definition.Fields, name)
		}
	}
	for resource, uses := range allowed {
		class, _ := my.metadata.GetNode(resource)
		filterEffectiveTypes(schema, class, uses, writes[resource])
	}
	if err := pruneEffectiveSchema(schema); err != nil {
		return nil, err
	}
	disableUnsupportedDirectives(schema)
	source := formatSchema(schema)
	sort.Strings(signatures)
	permission := digest(strings.Join(signatures, "\x00"))
	view := &effectiveView{artifact: SchemaArtifact{
		Schema: source, Fingerprint: digest(source), StructuralFingerprint: my.snapshot.Fingerprint(),
		PermissionSignature: permission, PolicyRevision: identity.PolicyRevision(), RouteRevision: route.Revision,
	}, schema: schema, intro: intro.New(schema)}
	if cacheable {
		my.effectiveMu.Lock()
		if my.effective == nil || len(my.effective) >= 128 {
			my.effective = make(map[string]*effectiveView)
		}
		my.effective[key] = view
		my.effectiveMu.Unlock()
	}
	return view, nil
}

func removeSchemaArgument(arguments ast.ArgumentDefinitionList, name string) ast.ArgumentDefinitionList {
	out := arguments[:0]
	for _, argument := range arguments {
		if argument.Name != name {
			out = append(out, argument)
		}
	}
	return out
}

func (my *Engine) effectivePolicy(ctx context.Context) (PolicyProvider, Identity, bool, error) {
	if my.policy != nil {
		identity, ok := IdentityFromContext(ctx)
		if !ok {
			return nil, Identity{}, false, fmt.Errorf("verified identity is required for effective schema")
		}
		return my.policy, identity, my.policyBuiltin, nil
	}
	return nil, Identity{}, false, nil
}

func hasMetadataPermissions(metadata *Metadata) bool {
	if metadata == nil {
		return false
	}
	for name, class := range metadata.Nodes {
		if class != nil && name == class.Name && len(class.Permissions) > 0 {
			return true
		}
	}
	return false
}

func metadataPolicy(metadata *Metadata) PolicyProvider {
	permissions := make(map[string]map[string]*protocol.Permission)
	for _, name := range sortedNodeNames(metadata) {
		class := metadata.Nodes[name]
		if class != nil && name == class.Name && len(class.Permissions) > 0 {
			permissions[class.Name] = class.Permissions
		}
	}
	if len(permissions) == 0 {
		return nil
	}
	return NewProtocolPolicyProvider(permissions)
}

func (my *Engine) effectiveSchemaKey(ctx context.Context, identity Identity) string {
	claims, _ := stdjson.Marshal(identity.Claims())
	route, _ := PinnedRoute(ctx)
	return digest(strings.Join([]string{
		my.snapshot.Fingerprint(), identity.Tenant(), identity.Subject(), strings.Join(identity.Roles(), ","),
		string(claims), fmt.Sprint(identity.PolicyRevision()), route.CacheKey,
	}, "\x00"))
}

func filterEffectiveTypes(schema *ast.Schema, class *protocol.Class, uses map[FieldUse][]string, writes map[string][]string) {
	if class == nil {
		return
	}
	output := append(append([]string(nil), uses[FieldUseSelect]...), uses[FieldUseReturn]...)
	for _, name := range uses[FieldUseJoin] {
		field := class.Fields[name]
		dependencies := []string(nil)
		if field != nil {
			switch {
			case field.Relation != nil:
				dependencies = field.Relation.SourceColumns()
			case field.Resolver != "":
				dependencies = field.Requires
			case field.Remote != nil:
				dependencies = []string{field.Remote.Key}
			}
		}
		allowed := true
		for _, dependency := range dependencies {
			allowed = allowed && allowedColumn(uses[FieldUseInternalDependency], class.Fields[dependency])
		}
		if allowed {
			output = append(output, name)
		}
	}
	filterEffectiveFields(schema.Types[class.Name], class, output, false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_CREATE_INPUT], class, writes[protocol.OP_INSERT], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_CREATE_ROW_INPUT], class, writes[protocol.OP_INSERT], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_UPSERT_ROW_INPUT], class, policyWriteIntersection(writes), false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_UPDATE_INPUT], class, writes[protocol.OP_UPDATE], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_UPDATE_ROW_INPUT], class, writes[protocol.OP_UPDATE], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_WHERE_INPUT], class, uses[FieldUseFilter], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_SORT_INPUT], class, uses[FieldUseOrder], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_KEY_INPUT], class, uses[FieldUseFilter], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_HAVING_INPUT], class, uses[FieldUseGroup], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_STATS], class, uses[FieldUseAggregate], true)
	if !filterEffectiveEnum(schema.Types[class.Name+protocol.SUFFIX_DISTINCT_FIELD], class, uses[FieldUseGroup]) {
		removeFieldArgument(schema.Types["Query"], queryField(class.Name), protocol.DISTINCT)
		removeFieldArgument(schema.Types["Subscription"], queryField(class.Name), protocol.DISTINCT)
	}
	if !filterEffectiveEnum(schema.Types[class.Name+protocol.SUFFIX_GROUP_FIELD], class, uses[FieldUseGroup]) {
		removeFieldArgument(schema.Types["Query"], strcase.ToLowerCamel(class.Name)+protocol.SUFFIX_STATS, protocol.GROUP_BY)
	}
}

func policyWriteIntersection(writes map[string][]string) []string {
	allowed := make(map[string]struct{}, len(writes[protocol.OP_UPDATE]))
	for _, field := range writes[protocol.OP_UPDATE] {
		allowed[field] = struct{}{}
	}
	var fields []string
	for _, field := range uniqueStrings(writes[protocol.OP_INSERT]) {
		if _, ok := allowed[field]; ok {
			fields = append(fields, field)
		}
	}
	return fields
}

func filterEffectiveEnum(definition *ast.Definition, class *protocol.Class, allowed []string) bool {
	if definition == nil {
		return false
	}
	values := definition.EnumValues[:0]
	for _, value := range definition.EnumValues {
		if allowedColumn(allowed, class.Fields[value.Name]) {
			values = append(values, value)
		}
	}
	definition.EnumValues = values
	return len(values) > 0
}

func removeFieldArgument(definition *ast.Definition, fieldName, argumentName string) {
	if definition == nil {
		return
	}
	if field := definition.Fields.ForName(fieldName); field != nil {
		field.Arguments = removeSchemaArgument(field.Arguments, argumentName)
	}
}

func filterEffectiveFields(definition *ast.Definition, class *protocol.Class, allowed []string, generated bool) {
	if definition == nil {
		return
	}
	fields := definition.Fields[:0]
	for _, definition := range definition.Fields {
		field := class.Fields[definition.Name]
		if (field == nil && generated) || (field != nil && allowedColumn(allowed, field)) {
			fields = append(fields, definition)
		}
	}
	definition.Fields = fields
}

func removeSchemaField(fields ast.FieldList, name string) ast.FieldList {
	out := fields[:0]
	for _, field := range fields {
		if field.Name != name {
			out = append(out, field)
		}
	}
	return out
}

// pruneEffectiveSchema removes capability types that are no longer reachable
// from an executable root. Keeping them would make __type reveal operations
// that the identity cannot invoke and can leave invalid empty input objects.
func pruneEffectiveSchema(schema *ast.Schema) error {
	// Shape denial can empty a type. Remove that type and every field whose
	// contract references it, repeating until the remaining graph is closed.
	// This keeps introspection executable without inventing placeholder fields.
	for changed := true; changed; {
		changed = false
		for name, definition := range schema.Types {
			if definition.BuiltIn || definition == schema.Query || definition == schema.Mutation || definition == schema.Subscription {
				continue
			}
			empty := (definition.Kind == ast.Object || definition.Kind == ast.Interface || definition.Kind == ast.InputObject) && len(definition.Fields) == 0 ||
				definition.Kind == ast.Enum && len(definition.EnumValues) == 0 || definition.Kind == ast.Union && len(definition.Types) == 0
			if empty {
				delete(schema.Types, name)
				changed = true
			}
		}
		for _, definition := range schema.Types {
			fields := definition.Fields[:0]
			for _, field := range definition.Fields {
				valid := schema.Types[field.Type.Name()] != nil
				arguments := field.Arguments[:0]
				for _, argument := range field.Arguments {
					if schema.Types[argument.Type.Name()] != nil {
						arguments = append(arguments, argument)
					} else if argument.Type.NonNull {
						valid = false
					}
				}
				if valid {
					field.Arguments = arguments
					fields = append(fields, field)
				} else {
					changed = true
				}
			}
			definition.Fields = fields
		}
	}
	if schema.Query == nil || len(schema.Query.Fields) == 0 {
		return fmt.Errorf("effective schema has no query capability")
	}
	if schema.Mutation != nil && len(schema.Mutation.Fields) == 0 {
		schema.Mutation = nil
	}
	if schema.Subscription != nil && len(schema.Subscription.Fields) == 0 {
		schema.Subscription = nil
	}
	reachable := make(map[string]bool)
	var visit func(string)
	visit = func(name string) {
		if name == "" || reachable[name] {
			return
		}
		definition := schema.Types[name]
		if definition == nil {
			return
		}
		reachable[name] = true
		for _, name := range append(append([]string(nil), definition.Interfaces...), definition.Types...) {
			visit(name)
		}
		for _, possible := range schema.PossibleTypes[name] {
			visit(possible.Name)
		}
		for _, field := range definition.Fields {
			visit(field.Type.Name())
			for _, argument := range field.Arguments {
				visit(argument.Type.Name())
			}
		}
	}
	visit(schema.Query.Name)
	if schema.Mutation != nil {
		visit(schema.Mutation.Name)
	}
	if schema.Subscription != nil {
		visit(schema.Subscription.Name)
	}
	for _, directive := range schema.Directives {
		for _, argument := range directive.Arguments {
			visit(argument.Type.Name())
		}
	}
	for name, definition := range schema.Types {
		if !reachable[name] && !definition.BuiltIn {
			delete(schema.Types, name)
			continue
		}
	}
	for name, definitions := range schema.PossibleTypes {
		schema.PossibleTypes[name] = visibleDefinitions(definitions, reachable)
	}
	for name, definitions := range schema.Implements {
		schema.Implements[name] = visibleDefinitions(definitions, reachable)
	}
	return nil
}

func visibleDefinitions(definitions []*ast.Definition, reachable map[string]bool) []*ast.Definition {
	out := definitions[:0]
	for _, definition := range definitions {
		if reachable[definition.Name] {
			out = append(out, definition)
		}
	}
	return out
}
