package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"slices"
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
	Version               string       `json:"version"`
	Schema                string       `json:"schema"`
	Fingerprint           string       `json:"fingerprint"`
	StructuralFingerprint string       `json:"structuralFingerprint"`
	PermissionSignature   string       `json:"permissionSignature"`
	PolicyRevision        uint64       `json:"policyRevision"`
	RouteRevision         uint64       `json:"routeRevision"`
	CacheIdentity         string       `json:"cacheIdentity,omitempty"`
	ExecutionPin          string       `json:"executionPin"`
	Capabilities          []Capability `json:"capabilities"`
}

const SchemaArtifactVersion = "schema-artifact-v1"

type executionPinContextKey struct{}

// WithExecutionPin binds an effective schema artifact to delegated child
// execution. It is intentionally transport-neutral; transports may project
// it to a request header at a network boundary.
func WithExecutionPin(ctx context.Context, pin string) context.Context {
	if strings.TrimSpace(pin) == "" {
		return ctx
	}
	return context.WithValue(ctx, executionPinContextKey{}, pin)
}

func ExecutionPinFromContext(ctx context.Context) (string, bool) {
	pin, ok := ctx.Value(executionPinContextKey{}).(string)
	return pin, ok && strings.TrimSpace(pin) != ""
}

func schemaArtifactExecutionPin(artifact SchemaArtifact) string {
	parts := []string{SchemaArtifactVersion, artifact.Fingerprint, artifact.StructuralFingerprint,
		artifact.PermissionSignature, fmt.Sprint(artifact.PolicyRevision), fmt.Sprint(artifact.RouteRevision)}
	if artifact.CacheIdentity != "" {
		parts = append(parts, artifact.CacheIdentity)
	}
	capabilities, _ := stdjson.Marshal(artifact.Capabilities)
	parts = append(parts, string(capabilities))
	return digest(strings.Join(parts, "\x00"))
}

func enforceExecutionPin(ctx context.Context, artifact SchemaArtifact) error {
	pin, ok := ExecutionPinFromContext(ctx)
	if !ok || pin == FinalizeSchemaArtifact(artifact).ExecutionPin {
		return nil
	}
	return &publicFailure{"effective schema execution pin changed", "SNAPSHOT_CONFLICT"}
}

// FinalizeSchemaArtifact applies the versioned execution envelope after all
// schema, policy, route, and cache identity fields have been populated.
func FinalizeSchemaArtifact(artifact SchemaArtifact) SchemaArtifact {
	artifact.Version = SchemaArtifactVersion
	artifact.ExecutionPin = schemaArtifactExecutionPin(artifact)
	return artifact
}

// ValidateSchemaArtifact verifies the complete identity-independent artifact
// envelope before it is used for composition or delegated execution.
func ValidateSchemaArtifact(artifact SchemaArtifact) error {
	if artifact.Version != SchemaArtifactVersion || artifact.ExecutionPin == "" || artifact.ExecutionPin != schemaArtifactExecutionPin(artifact) {
		return fmt.Errorf("effective artifact version or execution pin mismatch")
	}
	if artifact.Schema == "" || artifact.Fingerprint != digest(artifact.Schema) {
		return fmt.Errorf("effective artifact fingerprint mismatch")
	}
	return nil
}

type SchemaProfile struct {
	Name string `json:"name"`
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
	var breaking []SchemaCompatibilityViolation
	add := func(category, coordinate, message string) {
		breaking = append(breaking, SchemaCompatibilityViolation{Category: category, Coordinate: coordinate, Message: message})
	}
	for name, oldType := range before.Types {
		if strings.HasPrefix(name, "__") {
			continue
		}
		newType := after.Types[name]
		if newType == nil {
			add("type_removed", name, "removed type "+name)
			continue
		}
		if newType.Kind != oldType.Kind {
			add("type_kind_changed", name, "changed type kind "+name)
			continue
		}
		for _, oldField := range oldType.Fields {
			newField := newType.Fields.ForName(oldField.Name)
			if newField == nil {
				add("field_removed", name+"."+oldField.Name, "removed field "+name+"."+oldField.Name)
			} else if newField.Type.String() != oldField.Type.String() {
				add("field_type_changed", name+"."+oldField.Name, "changed field "+name+"."+oldField.Name)
			} else if oldType.Kind == ast.InputObject && defaultValueChanged(oldField.DefaultValue, newField.DefaultValue) {
				add("input_default_changed", name+"."+oldField.Name, "changed input default "+name+"."+oldField.Name)
			} else if oldType.Kind != ast.InputObject {
				for _, violation := range incompatibleArguments(name+"."+oldField.Name, oldField.Arguments, newField.Arguments) {
					add(violation.Category, violation.Coordinate, violation.Message)
				}
			}
		}
		if oldType.Kind == ast.InputObject {
			for _, field := range newType.Fields {
				if oldType.Fields.ForName(field.Name) == nil && field.Type.NonNull && field.DefaultValue == nil {
					add("input_field_required", name+"."+field.Name, "added required input field "+name+"."+field.Name)
				}
			}
		}
		if oldType.Kind == ast.Enum {
			for _, value := range oldType.EnumValues {
				if newType.EnumValues.ForName(value.Name) == nil {
					add("enum_value_removed", name+"."+value.Name, "removed enum value "+name+"."+value.Name)
				}
			}
		}
		if oldType.Kind == ast.Union {
			for _, member := range oldType.Types {
				if !slices.Contains(newType.Types, member) {
					add("union_member_removed", name+"."+member, "removed union member "+name+"."+member)
				}
			}
		}
		if oldType.Kind == ast.Object {
			for _, iface := range oldType.Interfaces {
				if !slices.Contains(newType.Interfaces, iface) {
					add("interface_implementation_removed", name+" implements "+iface, "removed interface implementation "+name+" implements "+iface)
				}
			}
		}
	}
	for _, root := range []struct {
		name          string
		before, after *ast.Definition
	}{{"query", before.Query, after.Query}, {"mutation", before.Mutation, after.Mutation}, {"subscription", before.Subscription, after.Subscription}} {
		beforeName, afterName := "", ""
		if root.before != nil {
			beforeName = root.before.Name
		}
		if root.after != nil {
			afterName = root.after.Name
		}
		if beforeName != "" && beforeName != afterName {
			add("schema_root_changed", "schema."+root.name, fmt.Sprintf("changed schema %s root from %q to %q", root.name, beforeName, afterName))
		}
	}
	for name, oldDirective := range before.Directives {
		if strings.HasPrefix(name, "__") {
			continue
		}
		newDirective := after.Directives[name]
		if newDirective == nil {
			add("directive_removed", "@"+name, "removed directive @"+name)
			continue
		}
		if oldDirective.IsRepeatable && !newDirective.IsRepeatable {
			add("directive_repeatability_changed", "@"+name, "changed directive repeatability @"+name)
		}
		for _, location := range oldDirective.Locations {
			if !slices.Contains(newDirective.Locations, location) {
				add("directive_location_removed", "@"+name, "removed directive location @"+name+" "+string(location))
			}
		}
		for _, arg := range oldDirective.Arguments {
			nextArg := newDirective.Arguments.ForName(arg.Name)
			if nextArg == nil {
				add("directive_argument_removed", fmt.Sprintf("@%s(%s)", name, arg.Name), "removed directive argument "+name+"("+arg.Name+")")
				continue
			}
			if nextArg.Type.String() != arg.Type.String() {
				add("directive_argument_type_changed", fmt.Sprintf("@%s(%s)", name, arg.Name), "changed directive argument "+name+"("+arg.Name+")")
			}
			if defaultValueChanged(arg.DefaultValue, nextArg.DefaultValue) {
				add("directive_argument_default_changed", fmt.Sprintf("@%s(%s)", name, arg.Name), "changed directive argument default "+name+"("+arg.Name+")")
			}
		}
		for _, arg := range newDirective.Arguments {
			if oldDirective.Arguments.ForName(arg.Name) == nil && arg.Type.NonNull && arg.DefaultValue == nil {
				add("directive_argument_required", fmt.Sprintf("@%s(%s)", name, arg.Name), "added required directive argument "+name+"("+arg.Name+")")
			}
		}
	}
	if len(breaking) > 0 {
		sort.Slice(breaking, func(i, j int) bool {
			if breaking[i].Coordinate == breaking[j].Coordinate {
				return breaking[i].Category < breaking[j].Category
			}
			return breaking[i].Coordinate < breaking[j].Coordinate
		})
		return SchemaCompatibilityError{Violations: breaking}
	}
	return nil
}

// SchemaCompatibilityViolation identifies one client-breaking contract change.
type SchemaCompatibilityViolation struct {
	Category   string `json:"category"`
	Coordinate string `json:"coordinate"`
	Message    string `json:"message"`
}

func (my SchemaCompatibilityViolation) String() string {
	return fmt.Sprintf("category=%s coordinate=%s: %s", my.Category, my.Coordinate, my.Message)
}

type SchemaCompatibilityError struct {
	Violations []SchemaCompatibilityViolation
}

func (my SchemaCompatibilityError) Error() string {
	parts := make([]string, len(my.Violations))
	for i, violation := range my.Violations {
		parts[i] = violation.String()
	}
	return "incompatible schema profile: " + strings.Join(parts, ", ")
}

func incompatibleArguments(coordinate string, before, after ast.ArgumentDefinitionList) []SchemaCompatibilityViolation {
	var breaking []SchemaCompatibilityViolation
	for _, argument := range before {
		next := after.ForName(argument.Name)
		if next == nil {
			breaking = append(breaking, SchemaCompatibilityViolation{"argument_removed", coordinate + "(" + argument.Name + ")", "removed argument " + coordinate + "(" + argument.Name + ")"})
		} else if next.Type.String() != argument.Type.String() {
			breaking = append(breaking, SchemaCompatibilityViolation{"argument_type_changed", coordinate + "(" + argument.Name + ")", "changed argument " + coordinate + "(" + argument.Name + ")"})
		} else if defaultValueChanged(argument.DefaultValue, next.DefaultValue) {
			breaking = append(breaking, SchemaCompatibilityViolation{"argument_default_changed", coordinate + "(" + argument.Name + ")", "changed argument default " + coordinate + "(" + argument.Name + ")"})
		}
	}
	for _, argument := range after {
		if before.ForName(argument.Name) == nil && argument.Type.NonNull && argument.DefaultValue == nil {
			breaking = append(breaking, SchemaCompatibilityViolation{"argument_required", coordinate + "(" + argument.Name + ")", "added required argument " + coordinate + "(" + argument.Name + ")"})
		}
	}
	return breaking
}

func defaultValueChanged(before, after *ast.Value) bool {
	if before == nil && after == nil {
		return false
	}
	if before == nil || after == nil {
		return true
	}
	return before.String() != after.String()
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
	view, err := my.effectiveSchema(ctx)
	if err != nil {
		return SchemaArtifact{}, err
	}
	artifact := view.artifact
	artifact = FinalizeSchemaArtifact(artifact)
	artifact.Capabilities = cloneCapabilities(artifact.Capabilities)
	return artifact, nil
}

// EffectiveSchemaCacheIdentity returns a stable provider-declared key without
// materializing the effective schema. Empty means the schema is uncacheable.
func (my *Engine) EffectiveSchemaCacheIdentity(ctx context.Context) (string, bool) {
	if _, err := my.Snapshot(); err != nil {
		return "", false
	}
	if err := my.checkSecurity(ctx); err != nil {
		return "", false
	}
	provider, identity, builtin, err := my.effectivePolicy(ctx)
	if err != nil || provider == nil {
		return "", false
	}
	if builtin {
		return "builtin", true
	}
	key, ok := effectiveProviderCacheIdentity(ctx, provider, identity)
	return key, ok && strings.TrimSpace(key) != ""
}

func (my *Engine) effectiveIntrospection(ctx context.Context) (*intro.Handler, error) {
	view, err := my.effectiveSchema(ctx)
	if err != nil {
		return nil, err
	}
	return view.intro, nil
}

func (my *Engine) effectiveSchema(ctx context.Context) (view *effectiveView, err error) {
	defer func() {
		if err == nil && view != nil {
			err = enforceExecutionPin(ctx, view.artifact)
		}
	}()
	if _, err := my.Snapshot(); err != nil {
		return nil, err
	}
	if err := my.checkSecurity(ctx); err != nil {
		return nil, err
	}
	provider, identity, builtin, err := my.effectivePolicy(ctx)
	if err != nil {
		return nil, err
	}
	if provider == nil {
		return my.baseView, nil
	}
	if my.effectiveCache == nil {
		my.effectiveCache = newEffectiveCache(128)
	}
	cacheable := builtin
	providerKey := ""
	if !builtin {
		providerKey, cacheable = effectiveProviderCacheIdentity(ctx, provider, identity)
		cacheable = cacheable && strings.TrimSpace(providerKey) != ""
	} else {
		providerKey = "builtin"
	}
	key := my.effectiveSchemaKey(ctx, identity, provider)
	if !cacheable {
		return my.effectiveSchemaBuild(ctx, provider, identity, "", false)
	}
	if cached, ok := my.effectiveCache.Get(key); ok {
		return cached, nil
	}
	return sharedBuild(ctx, &my.effectiveFlight, key, func(shared context.Context) (*effectiveView, error) {
		if cached, ok := my.effectiveCache.Get(key); ok {
			return cached, nil
		}
		return my.effectiveSchemaBuild(shared, provider, identity, key, true, providerKey)
	})
}

func (my *Engine) effectiveSchemaBuild(ctx context.Context, provider PolicyProvider, identity Identity, key string, cacheable bool, cacheIdentity ...string) (*effectiveView, error) {
	route, _ := PinnedRoute(ctx)
	if route.Tenant == "" {
		route.Tenant = identity.Tenant()
	}
	schema, err := gqlparser.LoadSchema(&ast.Source{Name: "effective.graphql", Input: my.snapshot.Schema()})
	if err != nil {
		return nil, err
	}
	allowed := make(map[string]map[FieldUse][]string)
	shapeSeen := make(map[string]map[FieldUse]bool)
	outputs := make(map[string][]string)
	outputSeen := make(map[string]bool)
	writes := make(map[string]map[string][]string)
	writeSeen := make(map[string]map[string]bool)
	denied := make(map[string]bool)
	relationActions := make(map[string]map[string]bool)
	var signatures []string
	for _, capability := range my.snapshot.Capabilities() {
		root := capability.Parent == "Query" || capability.Parent == "Mutation" || capability.Parent == "Subscription"
		custom := capability.Handler == HandlerResolver || capability.Handler == HandlerRemote
		if capability.Resource == "" || len(capability.Actions) == 0 || !root && !custom {
			continue
		}
		request := PolicyRequest{Identity: identity, Route: route, Coordinate: capability.Coordinate, Resource: capability.Resource, Actions: capability.Actions, FieldUses: allFieldUses, Values: scopeValues(ctx)}
		decision, err := decidePolicy(ctx, provider, request)
		if err != nil {
			return nil, fmt.Errorf("policy %s: %w", capability.Coordinate, err)
		}
		if revision := identity.PolicyRevision(); revision != 0 && decision.PolicyRevision != revision {
			return nil, fmt.Errorf("policy %s: %w", capability.Coordinate, publicPolicyFailure("POLICY_REVISION_MISMATCH", "policy revision mismatch"))
		}
		class, governed := my.metadata.GetNode(capability.Resource)
		if governed && capability.Parent == "Mutation" {
			action := ""
			switch capability.Coordinate {
			case "Mutation." + protocol.CREATE + class.Name:
				action = protocol.OP_INSERT
			case "Mutation." + protocol.UPDATE + class.Name:
				action = protocol.OP_UPDATE
			}
			if action != "" {
				if relationActions[class.Name] == nil {
					relationActions[class.Name] = make(map[string]bool)
				}
				relationActions[class.Name][action] = decision.Allow
			}
		}
		// nil means the provider does not shape fields; a non-nil map is an
		// explicit shape, including an empty deny-all shape.
		hasFieldShape := decision.Fields != nil
		signatures = append(signatures, decision.PermissionSignature+":"+decision.ShapeSignature)
		if !decision.Allow {
			denied[capability.Coordinate] = true
			continue
		}
		if capability.Handler != HandlerSQL && hasRowPolicy(decision) && !capability.PolicyAware {
			return nil, fmt.Errorf("%w: %s", publicPolicyFailure("POLICY_HANDLER_REQUIRED", "policy requires a policy-aware handler"), capability.Coordinate)
		}
		if governed && hasFieldShape && capability.Handler == HandlerSQL {
			if allowed[class.Name] == nil {
				allowed[class.Name] = make(map[FieldUse][]string)
			}
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
			if shapeSeen[class.Name] == nil {
				shapeSeen[class.Name] = make(map[FieldUse]bool)
			}
			selectFields, selects := decision.Fields[FieldUseSelect]
			returnFields, returns := decision.Fields[FieldUseReturn]
			if selects || returns {
				output := append(append([]string(nil), selectFields...), returnFields...)
				if !outputSeen[class.Name] {
					outputs[class.Name], outputSeen[class.Name] = sortedUnique(output), true
				} else {
					outputs[class.Name] = intersectFieldNames(outputs[class.Name], output)
				}
			}
			if len(shapeSeen[class.Name]) == 0 {
				for use, fields := range decision.Fields {
					if use != FieldUseWrite {
						uses[use] = append([]string(nil), fields...)
						shapeSeen[class.Name][use] = true
					}
				}
			} else {
				for use, current := range uses {
					if use == FieldUseWrite {
						continue
					}
					if fields, ok := decision.Fields[use]; ok {
						uses[use] = intersectFieldNames(current, fields)
					}
				}
				for use, fields := range decision.Fields {
					if use != FieldUseWrite && !shapeSeen[class.Name][use] {
						uses[use] = append([]string(nil), fields...)
						shapeSeen[class.Name][use] = true
					}
				}
			}
			if len(decision.Fields[FieldUseWrite]) > 0 {
				if writes[class.Name] == nil {
					writes[class.Name] = make(map[string][]string)
				}
				if writeSeen[class.Name] == nil {
					writeSeen[class.Name] = make(map[string]bool)
				}
				key := ""
				if len(capability.Actions) > 1 && operationInList(capability.Actions, protocol.OP_INSERT) && operationInList(capability.Actions, protocol.OP_UPDATE) {
					key = protocol.UPSERT
				} else if len(capability.Actions) == 1 && (capability.Actions[0] == protocol.OP_INSERT || capability.Actions[0] == protocol.OP_UPDATE) {
					key = capability.Actions[0]
				}
				if key != "" {
					if !writeSeen[class.Name][key] {
						writes[class.Name][key] = append([]string(nil), decision.Fields[FieldUseWrite]...)
						writeSeen[class.Name][key] = true
					} else {
						writes[class.Name][key] = intersectFieldNames(writes[class.Name][key], decision.Fields[FieldUseWrite])
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
	for resource, actions := range relationActions {
		for _, suffix := range []string{protocol.SUFFIX_CREATE_RELATION_INPUT, protocol.SUFFIX_UPDATE_RELATION_INPUT} {
			if definition := schema.Types[resource+suffix]; definition != nil {
				if !actions[protocol.OP_UPDATE] {
					definition.Fields = removeSchemaField(removeSchemaField(definition.Fields, protocol.CONNECT), protocol.DISCONNECT)
				}
				if !actions[protocol.OP_INSERT] {
					definition.Fields = removeSchemaField(definition.Fields, protocol.CREATE)
				}
			}
		}
	}
	for resource, uses := range allowed {
		class, _ := my.metadata.GetNode(resource)
		filterEffectiveTypes(schema, class, uses, outputs[resource], writes[resource])
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
		Capabilities: schemaCapabilities(schema, my.snapshot.Capabilities()),
	}, schema: schema, intro: intro.New(schema)}
	if cacheable && len(cacheIdentity) > 0 {
		view.artifact.CacheIdentity = cacheIdentity[0]
	}
	if cacheable {
		my.effectiveCache.Put(key, view)
	}
	return view, nil
}

func schemaCapabilities(schema *ast.Schema, capabilities []Capability) []Capability {
	out := make([]Capability, 0, len(capabilities))
	for _, capability := range capabilities {
		parent := schema.Types[capability.Parent]
		if parent != nil {
			if field := parent.Fields.ForName(capability.Name); field != nil {
				capability = cloneCapability(capability)
				capability.Type, capability.Arguments = field.Type.String(), fieldArguments(field)
				out = append(out, capability)
			}
		}
	}
	return out
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

func intersectFieldShape(current map[FieldUse][]string, next map[FieldUse][]string) {
	for use, fields := range current {
		if use == FieldUseWrite {
			continue
		}
		if allowed, ok := next[use]; ok {
			current[use] = intersectFieldNames(fields, allowed)
		}
	}
}

func intersectFieldNames(a, b []string) []string {
	if slices.Contains(a, "*") {
		return sortedUnique(b)
	}
	if slices.Contains(b, "*") {
		return sortedUnique(a)
	}
	set := make(map[string]struct{}, len(b))
	for _, name := range b {
		set[name] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, name := range sortedUnique(a) {
		if _, ok := set[name]; ok {
			out = append(out, name)
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
	for _, name := range sortedMapKeys(metadata.Nodes) {
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

func (my *Engine) effectiveSchemaKey(ctx context.Context, identity Identity, provider PolicyProvider) string {
	route, _ := PinnedRoute(ctx)
	providerKey := ""
	providerKey, _ = effectiveProviderCacheIdentity(ctx, provider, identity)
	return digest(strings.Join([]string{
		my.snapshot.Fingerprint(), identity.Tenant(), providerKey,
		fmt.Sprint(identity.PolicyRevision()),
		fmt.Sprint(identity.CredentialEpoch()), route.Tenant, route.DataSource, route.Schema, route.Catalog, route.CacheKey,
		fmt.Sprint(route.Revision), fmt.Sprint(route.Mode),
	}, "\x00"))
}

func effectiveProviderCacheIdentity(ctx context.Context, provider PolicyProvider, identity Identity) (string, bool) {
	cacheIdentity, ok := provider.(PolicyCacheIdentity)
	if !ok {
		return "", false
	}
	route, _ := PinnedRoute(ctx)
	return cacheIdentity.PolicyCacheIdentity(ctx, identity, route, scopeValues(ctx))
}

func filterEffectiveTypes(schema *ast.Schema, class *protocol.Class, uses map[FieldUse][]string, output []string, writes map[string][]string) {
	if class == nil {
		return
	}
	output = append([]string(nil), output...)
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
	filterEffectiveInput(schema.Types[class.Name+protocol.SUFFIX_CREATE_INPUT], class, writes[protocol.OP_INSERT], uses[FieldUseJoin])
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_CREATE_ROW_INPUT], class, writes[protocol.OP_INSERT], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_UPSERT_ROW_INPUT], class, writes[protocol.UPSERT], false)
	filterEffectiveInput(schema.Types[class.Name+protocol.SUFFIX_UPDATE_INPUT], class, writes[protocol.OP_UPDATE], uses[FieldUseJoin])
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_UPDATE_ROW_INPUT], class, writes[protocol.OP_UPDATE], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_WHERE_INPUT], class, uses[FieldUseFilter], true)
	visibleOrder := make([]string, 0, len(uses[FieldUseOrder]))
	for _, field := range class.Fields {
		if allowedColumn(uses[FieldUseOrder], field) && allowedColumn(output, field) {
			visibleOrder = append(visibleOrder, field.Name)
		}
	}
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_SORT_INPUT], class, visibleOrder, true)
	for _, primary := range class.PrimaryKeys {
		if !allowedColumn(output, class.Fields[primary]) {
			removeCursorArguments(schema, class.Name)
			break
		}
	}
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

func removeCursorArguments(schema *ast.Schema, className string) {
	for _, root := range []*ast.Definition{schema.Query, schema.Subscription} {
		if root == nil {
			continue
		}
		connection := strcase.ToLowerCamel(className) + protocol.SUFFIX_CONNECTION
		root.Fields = removeSchemaField(root.Fields, connection)
		for _, fieldName := range []string{queryField(className), strcase.ToLowerCamel(className) + protocol.SUFFIX_CONNECTION} {
			if field := root.Fields.ForName(fieldName); field != nil {
				for _, argument := range []string{protocol.FIRST, protocol.AFTER, protocol.LAST, protocol.BEFORE} {
					field.Arguments = removeSchemaArgument(field.Arguments, argument)
				}
			}
		}
	}
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

func filterEffectiveInput(definition *ast.Definition, class *protocol.Class, writes, joins []string) {
	if definition == nil {
		return
	}
	fields := definition.Fields[:0]
	for _, definition := range definition.Fields {
		field := class.Fields[definition.Name]
		allowed := writes
		if field != nil && field.Relation != nil {
			allowed = joins
		}
		if field != nil && allowedColumn(allowed, field) {
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
