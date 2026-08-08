package gql

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/ichaly/ideabase/gql/protocol"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
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
func (my *Executor) GenerateSchemaProfile(ctx context.Context, name string) (SchemaProfile, error) {
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
	_, errors := gqlparser.LoadQuery(schema, query)
	if len(errors) > 0 {
		return errors
	}
	return nil
}

// CheckProfileCompatibility rejects removals and field type changes that
// would break clients generated from the previous profile.
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
			}
		}
	}
	if len(breaking) > 0 {
		sort.Strings(breaking)
		return fmt.Errorf("incompatible schema profile: %s", strings.Join(breaking, ", "))
	}
	return nil
}

type effectiveView struct {
	artifact SchemaArtifact
	intro    *intro.Handler
}

var allFieldUses = []FieldUse{
	FieldUseSelect, FieldUseFilter, FieldUseOrder, FieldUseGroup,
	FieldUseAggregate, FieldUseJoin, FieldUseWrite, FieldUseReturn,
	FieldUseInternalDependency,
}

// EffectiveSchema returns the exact identity-specific schema artifact used by
// introspection. Runtime authorization remains mandatory on every handler.
func (my *Executor) EffectiveSchema(ctx context.Context) (SchemaArtifact, error) {
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

func (my *Executor) effectiveIntrospection(ctx context.Context) (*intro.Handler, error) {
	view, err := my.effectiveSchema(ctx)
	if err != nil {
		return nil, err
	}
	return view.intro, nil
}

func (my *Executor) effectiveSchema(ctx context.Context) (*effectiveView, error) {
	if my.policy == nil {
		return &effectiveView{artifact: SchemaArtifact{Schema: my.snapshot.Schema(), Fingerprint: my.snapshot.Fingerprint(), StructuralFingerprint: my.snapshot.Fingerprint()}, intro: my.intro}, nil
	}
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return nil, fmt.Errorf("verified identity is required for effective schema")
	}
	key := my.effectiveSchemaKey(ctx, identity)
	if identity.PolicyRevision() != 0 {
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
	denied := make(map[string]bool)
	var signatures []string
	for _, capability := range my.snapshot.Capabilities() {
		if capability.Resource == "" || len(capability.Actions) == 0 || capability.Parent != "Query" && capability.Parent != "Mutation" && capability.Parent != "Subscription" {
			continue
		}
		request := PolicyRequest{Identity: identity, Route: route, Coordinate: capability.Coordinate, Resource: capability.Resource, Actions: capability.Actions, FieldUses: allFieldUses, Values: scopeValues(ctx)}
		decision, err := my.policy.Decide(ctx, request)
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
		if governed && allowed[class.Name] == nil {
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
		if governed {
			if err := ValidateDecisionCatalog(class, decision); err != nil {
				return nil, err
			}
			uses := allowed[class.Name]
			if uses == nil {
				uses = make(map[FieldUse][]string)
				allowed[class.Name] = uses
			}
			for use, fields := range decision.Fields {
				uses[use] = append(uses[use], fields...)
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
		filterEffectiveTypes(schema, class, uses)
	}
	disableUnsupportedDirectives(schema)
	source := formatSchema(schema)
	sort.Strings(signatures)
	permission := digest(strings.Join(signatures, "\x00"))
	view := &effectiveView{artifact: SchemaArtifact{
		Schema: source, Fingerprint: digest(source), StructuralFingerprint: my.snapshot.Fingerprint(),
		PermissionSignature: permission, PolicyRevision: identity.PolicyRevision(), RouteRevision: route.Revision,
	}, intro: intro.New(schema)}
	if identity.PolicyRevision() != 0 {
		my.effectiveMu.Lock()
		if my.effective == nil || len(my.effective) >= 128 {
			my.effective = make(map[string]*effectiveView)
		}
		my.effective[key] = view
		my.effectiveMu.Unlock()
	}
	return view, nil
}

func (my *Executor) effectiveSchemaKey(ctx context.Context, identity Identity) string {
	claims, _ := stdjson.Marshal(identity.Claims())
	route, _ := PinnedRoute(ctx)
	return digest(strings.Join([]string{
		my.snapshot.Fingerprint(), identity.Tenant(), identity.Subject(), strings.Join(identity.Roles(), ","),
		string(claims), fmt.Sprint(identity.PolicyRevision()), route.CacheKey,
	}, "\x00"))
}

func filterEffectiveTypes(schema *ast.Schema, class *protocol.Class, uses map[FieldUse][]string) {
	if class == nil {
		return
	}
	output := append(append(append([]string(nil), uses[FieldUseSelect]...), uses[FieldUseReturn]...), uses[FieldUseJoin]...)
	filterEffectiveFields(schema.Types[class.Name], class, output, false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_CREATE_INPUT], class, uses[FieldUseWrite], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_UPDATE_INPUT], class, uses[FieldUseWrite], false)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_WHERE_INPUT], class, uses[FieldUseFilter], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_SORT_INPUT], class, uses[FieldUseOrder], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_KEY_INPUT], class, uses[FieldUseFilter], true)
	filterEffectiveFields(schema.Types[class.Name+protocol.SUFFIX_HAVING_INPUT], class, uses[FieldUseGroup], true)
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
