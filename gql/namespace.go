package gql

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/iancoleman/strcase"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"
)

var namespaceName = regexp.MustCompile(`^[_A-Za-z][_0-9A-Za-z]*$`)

type namespaceContract struct {
	name, id, prefix string
	endpoint         Endpoint
	publicToChild    map[string]string
	mutations        map[string]string
	subscription     bool
}

func composeNamespaces(namespaces []Namespace) (*ast.Schema, string, map[string]*namespaceContract, []Capability, error) {
	query := &ast.Definition{Kind: ast.Object, Name: "Query"}
	mutation := &ast.Definition{Kind: ast.Object, Name: "Mutation"}
	subscription := &ast.Definition{Kind: ast.Object, Name: "Subscription"}
	types := map[string]*ast.Definition{"Query": query}
	contracts := make(map[string]*namespaceContract, len(namespaces))
	var capabilities []Capability
	sorted := append([]Namespace(nil), namespaces...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	for _, namespace := range sorted {
		contract, definitions, fields, err := importNamespace(namespace)
		if err != nil {
			return nil, "", nil, nil, err
		}
		if contracts[contract.name] != nil {
			return nil, "", nil, nil, fmt.Errorf("namespace重复: %s", contract.name)
		}
		contracts[contract.name] = contract
		for name, definition := range definitions {
			if types[name] != nil {
				return nil, "", nil, nil, fmt.Errorf("组合类型冲突: %s", name)
			}
			types[name] = definition
		}
		query.Fields = append(query.Fields, &ast.FieldDefinition{Name: contract.name, Type: ast.NamedType(contract.prefix+"Query", nil)})
		capabilities = append(capabilities, Capability{Coordinate: "Query." + contract.name, Parent: "Query", Name: contract.name, Type: contract.prefix + "Query", Handler: HandlerRemote, Planner: "namespace", Cost: 1})
		for _, field := range fields {
			mutation.Fields = append(mutation.Fields, field)
			capabilities = append(capabilities, Capability{Coordinate: "Mutation." + field.Name, Parent: "Mutation", Name: field.Name, Type: field.Type.String(), Handler: HandlerRemote, Planner: "namespace", Cost: 1})
		}
		if contract.subscription {
			subscription.Fields = append(subscription.Fields, &ast.FieldDefinition{Name: contract.name, Type: ast.NamedType(contract.prefix+"Subscription", nil)})
			capabilities = append(capabilities, Capability{Coordinate: "Subscription." + contract.name, Parent: "Subscription", Name: contract.name, Type: contract.prefix + "Subscription", Handler: HandlerRemote, Planner: "namespace", Cost: 1})
		}
	}
	if len(mutation.Fields) > 0 {
		types[mutation.Name] = mutation
	}
	if len(subscription.Fields) > 0 {
		types[subscription.Name] = subscription
	}
	composed := &ast.Schema{Query: query, Types: types, Directives: map[string]*ast.DirectiveDefinition{}}
	if len(mutation.Fields) > 0 {
		composed.Mutation = mutation
	}
	if len(subscription.Fields) > 0 {
		composed.Subscription = subscription
	}
	source := formatSchema(composed)
	validated, err := gqlparser.LoadSchema(&ast.Source{Name: "gateway.graphql", Input: source})
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("组合schema无效: %w", err)
	}
	disableUnsupportedDirectives(validated)
	return validated, source, contracts, capabilities, nil
}

func importNamespace(namespace Namespace) (*namespaceContract, map[string]*ast.Definition, ast.FieldList, error) {
	if !namespaceName.MatchString(namespace.Name) || strings.HasPrefix(namespace.Name, "__") {
		return nil, nil, nil, fmt.Errorf("namespace名称无效: %q", namespace.Name)
	}
	if namespace.Endpoint == nil {
		return nil, nil, nil, fmt.Errorf("namespace %s缺少endpoint", namespace.Name)
	}
	id := namespace.EndpointID
	if id == "" {
		if named, ok := namespace.Endpoint.(interface{ EndpointID() string }); ok {
			id = named.EndpointID()
		}
	}
	if id == "" {
		return nil, nil, nil, fmt.Errorf("namespace %s缺少稳定endpoint identity", namespace.Name)
	}
	snapshot, err := namespace.Endpoint.Snapshot()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("namespace %s snapshot: %w", namespace.Name, err)
	}
	child, err := gqlparser.LoadSchema(&ast.Source{Name: namespace.Name + ".graphql", Input: snapshot.Schema()})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("namespace %s schema: %w", namespace.Name, err)
	}
	for name := range child.Directives {
		if !builtInDirective(name) {
			return nil, nil, nil, fmt.Errorf("namespace %s暂不支持自定义directive @%s", namespace.Name, name)
		}
	}
	for name, definition := range child.Types {
		if !definition.BuiltIn && (definition.Kind == ast.Interface || definition.Kind == ast.Union) {
			return nil, nil, nil, fmt.Errorf("namespace %s包含尚不可执行的抽象类型 %s", namespace.Name, name)
		}
	}
	prefix := strcase.ToCamel(namespace.Name) + "_"
	toChild := make(map[string]string)
	rename := make(map[string]string)
	for name, definition := range child.Types {
		if definition.BuiltIn || strings.HasPrefix(name, "__") || definition == child.Query || definition == child.Mutation || definition == child.Subscription {
			continue
		}
		rename[name] = prefix + name
		toChild[prefix+name] = name
	}
	for old, renamed := range rename {
		definition := child.Types[old]
		definition.Name = renamed
		renameDefinition(definition, rename)
	}
	definitions := make(map[string]*ast.Definition, len(rename)+1)
	for old, renamed := range rename {
		definitions[renamed] = child.Types[old]
	}
	queryType := &ast.Definition{Kind: ast.Object, Name: prefix + "Query"}
	for _, field := range child.Query.Fields {
		renameType(field.Type, rename)
		for _, argument := range field.Arguments {
			renameType(argument.Type, rename)
		}
		queryType.Fields = append(queryType.Fields, field)
	}
	definitions[queryType.Name] = queryType
	contract := &namespaceContract{name: namespace.Name, id: id, prefix: prefix, endpoint: namespace.Endpoint, publicToChild: toChild, mutations: make(map[string]string)}
	var mutations ast.FieldList
	if child.Mutation != nil {
		for _, field := range child.Mutation.Fields {
			childName := field.Name
			field.Name = namespace.Name + "_" + childName
			renameType(field.Type, rename)
			for _, argument := range field.Arguments {
				renameType(argument.Type, rename)
			}
			contract.mutations[field.Name] = childName
			mutations = append(mutations, field)
		}
	}
	if child.Subscription != nil {
		if _, ok := namespace.Endpoint.(SubscriptionEndpoint); ok {
			subscriptionType := &ast.Definition{Kind: ast.Object, Name: prefix + "Subscription"}
			for _, field := range child.Subscription.Fields {
				renameType(field.Type, rename)
				for _, argument := range field.Arguments {
					renameType(argument.Type, rename)
				}
				subscriptionType.Fields = append(subscriptionType.Fields, field)
			}
			definitions[subscriptionType.Name] = subscriptionType
			contract.subscription = true
		}
	}
	return contract, definitions, mutations, nil
}

func builtInDirective(name string) bool {
	switch name {
	case "skip", "include", "deprecated", "specifiedBy", "oneOf", "defer":
		return true
	default:
		return false
	}
}

func renameDefinition(definition *ast.Definition, names map[string]string) {
	for i, name := range definition.Interfaces {
		if renamed := names[name]; renamed != "" {
			definition.Interfaces[i] = renamed
		}
	}
	for i, name := range definition.Types {
		if renamed := names[name]; renamed != "" {
			definition.Types[i] = renamed
		}
	}
	for _, field := range definition.Fields {
		renameType(field.Type, names)
		for _, argument := range field.Arguments {
			renameType(argument.Type, names)
		}
	}
}

func renameType(kind *ast.Type, names map[string]string) {
	if kind == nil {
		return
	}
	if renamed := names[kind.NamedType]; renamed != "" {
		kind.NamedType = renamed
	}
	renameType(kind.Elem, names)
}
