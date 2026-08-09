package gql

import (
	"bytes"
	"context"
	"crypto/sha256"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ichaly/ideabase/gql/internal/intro"
	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/gqlerror"
	validatorrules "github.com/vektah/gqlparser/v2/validator/rules"
)

const (
	gatewayMaxTokens        = 100_000
	gatewayMaxDepth         = 8
	gatewayMaxRootFields    = 64
	gatewayMaxFanout        = 32
	gatewayMaxChildResponse = 8 << 20
	gatewayTypeKey          = "__gql_gateway_type"
)

// Endpoint 是命名空间组合所需的最小传输无关契约。
type Endpoint interface {
	Snapshot() (*Snapshot, error)
	Execute(context.Context, string, map[string]any) ([]byte, error)
}

// SubscriptionEndpoint is implemented only by endpoints with an executable
// subscription source; its absence removes that namespace from introspection.
type SubscriptionEndpoint interface {
	Subscribe(context.Context, string, map[string]any, string) (<-chan []byte, error)
}

// Namespace 将一个端点挂载到稳定的公共GraphQL名称下。
type Namespace struct {
	Name       string
	EndpointID string
	Endpoint   Endpoint
}

// Gateway 组合不可变端点快照，不共享数据库、策略、事务或传输层。
type Gateway struct {
	id    string
	state atomic.Pointer[gatewayState]
}

type gatewayState struct {
	schema      *ast.Schema
	intro       *intro.Handler
	validation  *validatorrules.Rules
	snapshot    *Snapshot
	contracts   map[string]*namespaceContract
	mutations   map[string]*namespaceContract
	topologyIDs map[string]struct{}
	depth       int
}

type endpointTopology interface {
	topology() (map[string]struct{}, int)
}

// SnapshotEndpoint can bind execution to the exact manifest revision imported
// by a parent Gateway.
type SnapshotEndpoint interface {
	ForSnapshot(string) (Endpoint, error)
}

type gatewayRevision struct {
	id    string
	state *gatewayState
}

func (my *gatewayRevision) EndpointID() string           { return my.id }
func (my *gatewayRevision) Snapshot() (*Snapshot, error) { return my.state.snapshot, nil }
func (my *gatewayRevision) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.state.execute(ctx, query, variables, "")
}
func (my *gatewayRevision) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	return my.state.subscribe(ctx, query, variables, operationName)
}
func (my *gatewayRevision) topology() (map[string]struct{}, int) {
	ids := make(map[string]struct{}, len(my.state.topologyIDs))
	for id := range my.state.topologyIDs {
		ids[id] = struct{}{}
	}
	return ids, my.state.depth
}

// NewGateway 冻结确定性的命名空间拓扑与有效Schema。
func NewGateway(id string, namespaces ...Namespace) (*Gateway, error) {
	state, err := buildGatewayState(id, namespaces)
	if err != nil {
		return nil, err
	}
	gateway := &Gateway{id: id}
	gateway.state.Store(state)
	return gateway, nil
}

func buildGatewayState(id string, namespaces []Namespace) (*gatewayState, error) {
	if id == "" {
		return nil, fmt.Errorf("gateway缺少稳定endpoint identity")
	}
	namespaces = append([]Namespace(nil), namespaces...)
	for index := range namespaces {
		if pinner, ok := namespaces[index].Endpoint.(SnapshotEndpoint); ok {
			snapshot, err := namespaces[index].Endpoint.Snapshot()
			if err != nil {
				return nil, fmt.Errorf("namespace %s snapshot: %w", namespaces[index].Name, err)
			}
			pinned, err := pinner.ForSnapshot(snapshot.Fingerprint())
			if err != nil {
				return nil, fmt.Errorf("namespace %s snapshot changed during build: %w", namespaces[index].Name, err)
			}
			namespaces[index].Endpoint = pinned
		}
	}
	seen := map[string]string{id: "gateway"}
	depth := 1
	for _, namespace := range namespaces {
		childID, err := namespaceIdentity(namespace)
		if err != nil {
			return nil, err
		}
		if owner := seen[childID]; owner != "" {
			return nil, fmt.Errorf("endpoint identity重复: %s (%s, %s)", childID, owner, namespace.Name)
		}
		seen[childID] = namespace.Name
		if nested, ok := namespace.Endpoint.(endpointTopology); ok {
			ids, childDepth := nested.topology()
			if _, cycle := ids[id]; cycle {
				return nil, fmt.Errorf("gateway拓扑循环: %s", id)
			}
			for nestedID := range ids {
				if owner := seen[nestedID]; owner != "" && nestedID != childID {
					return nil, fmt.Errorf("endpoint identity重复: %s (%s, %s)", nestedID, owner, namespace.Name)
				}
				seen[nestedID] = namespace.Name
			}
			if childDepth+1 > depth {
				depth = childDepth + 1
			}
		}
	}
	if depth > gatewayMaxDepth {
		return nil, fmt.Errorf("gateway拓扑深度超过%d", gatewayMaxDepth)
	}
	schema, source, contracts, capabilities, err := composeNamespaces(namespaces)
	if err != nil {
		return nil, err
	}
	mutations := make(map[string]*namespaceContract)
	for _, contract := range contracts {
		for field := range contract.mutations {
			mutations[field] = contract
		}
	}
	identity := []string{id, source}
	for _, namespace := range namespaces {
		snapshot, snapshotErr := namespace.Endpoint.Snapshot()
		if snapshotErr != nil {
			return nil, fmt.Errorf("namespace %s snapshot: %w", namespace.Name, snapshotErr)
		}
		childID, _ := namespaceIdentity(namespace)
		identity = append(identity, namespace.Name+"\x00"+childID+"\x00"+snapshot.Fingerprint())
	}
	sort.Strings(identity[2:])
	fingerprint := sha256.Sum256([]byte(strings.Join(identity, "\x01")))
	index := make(map[string]Capability, len(capabilities))
	for _, capability := range capabilities {
		index[capability.Coordinate] = capability
	}
	snapshot := &Snapshot{schema: source, fingerprint: fmt.Sprintf("%x", fingerprint), capabilities: capabilities, index: index}
	return &gatewayState{
		schema: schema, intro: intro.New(schema), validation: validatorrules.NewDefaultRules(), snapshot: snapshot,
		contracts: contracts, mutations: mutations, topologyIDs: identitySet(seen), depth: depth,
	}, nil
}

func identitySet(names map[string]string) map[string]struct{} {
	ids := make(map[string]struct{}, len(names))
	for id := range names {
		ids[id] = struct{}{}
	}
	return ids
}

func namespaceIdentity(namespace Namespace) (string, error) {
	if namespace.Endpoint == nil {
		return "", fmt.Errorf("namespace %s缺少endpoint", namespace.Name)
	}
	if namespace.EndpointID != "" {
		return namespace.EndpointID, nil
	}
	if named, ok := namespace.Endpoint.(interface{ EndpointID() string }); ok && named.EndpointID() != "" {
		return named.EndpointID(), nil
	}
	return "", fmt.Errorf("namespace %s缺少稳定endpoint identity", namespace.Name)
}

func (my *Gateway) EndpointID() string { return my.id }

func (my *Gateway) ForSnapshot(fingerprint string) (Endpoint, error) {
	state := my.current()
	if state == nil || state.snapshot.Fingerprint() != fingerprint {
		return nil, fmt.Errorf("snapshot revision unavailable")
	}
	return &gatewayRevision{id: my.id, state: state}, nil
}

func (my *Gateway) topology() (map[string]struct{}, int) {
	state := my.current()
	if state == nil {
		return nil, 0
	}
	ids := make(map[string]struct{}, len(state.topologyIDs))
	for id := range state.topologyIDs {
		ids[id] = struct{}{}
	}
	return ids, state.depth
}

func (my *Gateway) Snapshot() (*Snapshot, error) {
	state := my.current()
	if state == nil || state.snapshot == nil {
		return nil, fmt.Errorf("gateway snapshot不可用")
	}
	return state.snapshot, nil
}

func (my *Gateway) Schema() (string, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return "", err
	}
	return snapshot.Schema(), nil
}

func (my *Gateway) current() *gatewayState {
	if my == nil {
		return nil
	}
	return my.state.Load()
}

// Refresh 构建并校验完整新快照后原子替换；失败时继续服务旧快照。
func (my *Gateway) Refresh(namespaces ...Namespace) error {
	if my == nil {
		return fmt.Errorf("gateway不可用")
	}
	state, err := buildGatewayState(my.id, namespaces)
	if err != nil {
		return err
	}
	my.state.Store(state)
	return nil
}

func (my *Gateway) Capabilities() ([]Capability, error) {
	snapshot, err := my.Snapshot()
	if err != nil {
		return nil, err
	}
	return snapshot.Capabilities(), nil
}

// SubscriptionAvailable reports whether the current composed snapshot exposes
// at least one executable namespace subscription.
func (my *Gateway) SubscriptionAvailable() bool {
	state := my.current()
	return state != nil && state.schema.Subscription != nil
}

// Subscribe validates the public single-root subscription, then forwards one
// child stream while preserving the namespace response and error paths.
func (my *Gateway) Subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	state := my.current()
	return state.subscribe(ctx, query, variables, operationName)
}

func (my *gatewayState) subscribe(ctx context.Context, query string, variables map[string]any, operationName string) (<-chan []byte, error) {
	if my == nil || my.schema.Subscription == nil {
		return nil, fmt.Errorf("gateway subscription不可用")
	}
	doc, errs := loadQuery(my.schema, query, gatewayMaxTokens, my.validation)
	if len(errs) > 0 {
		return nil, errs
	}
	operation := doc.Operations.ForName(operationName)
	if operation == nil || (operationName == "" && len(doc.Operations) != 1) || operation.Operation != ast.Subscription {
		return nil, fmt.Errorf("未找到可执行的subscription操作")
	}
	coerced, err := coerceVariables(my.schema, operation, cloneVariables(variables))
	if err != nil {
		return nil, err
	}
	operation.SelectionSet, err = normalizeSelectionWithSchema(operation.SelectionSet, doc.Fragments, coerced, my.schema)
	if err != nil {
		return nil, err
	}
	if len(operation.SelectionSet) != 1 {
		return nil, fmt.Errorf("subscription必须且只能选择一个namespace根字段")
	}
	field, ok := operation.SelectionSet[0].(*ast.Field)
	if !ok || len(field.SelectionSet) != 1 {
		return nil, fmt.Errorf("subscription namespace必须且只能选择一个child根字段")
	}
	contract := my.contracts[field.Name]
	subscriber, ok := contractEndpointSubscription(contract)
	if !ok {
		return nil, fmt.Errorf("namespace %s subscription不可用", field.Name)
	}
	childQuery, childVariables := childOperation(ast.Subscription, field.SelectionSet, operation.VariableDefinitions, coerced, contract.publicToChild)
	source, err := subscriber.Subscribe(ctx, childQuery, childVariables, "")
	if err != nil {
		return nil, err
	}
	events := make(chan []byte, 1)
	go my.forwardSubscription(ctx, events, source, field, contract)
	return events, nil
}

func contractEndpointSubscription(contract *namespaceContract) (SubscriptionEndpoint, bool) {
	if contract == nil || !contract.subscription {
		return nil, false
	}
	subscriber, ok := contract.endpoint.(SubscriptionEndpoint)
	return subscriber, ok
}

func (my *gatewayState) forwardSubscription(ctx context.Context, events chan<- []byte, source <-chan []byte, field *ast.Field, contract *namespaceContract) {
	defer close(events)
	for {
		select {
		case <-ctx.Done():
			return
		case body, ok := <-source:
			if !ok {
				return
			}
			child, err := decodeEndpointReply(body)
			if err != nil {
				frame, _ := marshalGatewayReply(gatewayReply{Data: map[string]any{responseKey(field): nil}, Errors: endpointProtocolError(responseKey(field), field)})
				events <- frame
				return
			}
			rewriteTypenames(my.schema, child.Data, field.SelectionSet, contract)
			definition := my.schema.Subscription.Fields.ForName(field.Name)
			value, completionErrors, _ := completeEndpointValue(my.schema, child.Data, definition.Type, field.SelectionSet, nil, child.Errors)
			child.Errors = append(child.Errors, completionErrors...)
			prefixErrors(child.Errors, responseKey(field), field)
			frame, err := marshalGatewayReply(gatewayReply{Data: map[string]any{responseKey(field): value}, Errors: child.Errors})
			if err != nil {
				return
			}
			select {
			case events <- frame:
			case <-ctx.Done():
				return
			}
		}
	}
}

// Execute 对组合Schema只校验一次，并将每个命名空间选择集作为一个子操作委派。
func (my *Gateway) Execute(ctx context.Context, query string, variables map[string]any) ([]byte, error) {
	return my.ExecuteOperation(ctx, query, variables, "")
}

// ExecuteOperation executes one named operation from a multi-operation document.
func (my *Gateway) ExecuteOperation(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	state := my.current()
	if state == nil {
		return nil, fmt.Errorf("gateway不可用")
	}
	return state.execute(ctx, query, variables, operationName)
}

func (my *gatewayState) execute(ctx context.Context, query string, variables map[string]any, operationName string) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	doc, errs := loadQuery(my.schema, query, gatewayMaxTokens, my.validation)
	if len(errs) > 0 {
		return marshalGatewayReply(gatewayReply{Errors: errs})
	}
	operation := doc.Operations.ForName(operationName)
	if operation == nil || (operationName == "" && len(doc.Operations) != 1) {
		return marshalGatewayReply(gatewayReply{Errors: gqlerror.List{gqlerror.Errorf("未找到名为'%s'的操作（多操作查询必须提供operationName）", operationName)}})
	}
	coerced, err := coerceVariables(my.schema, operation, cloneVariables(variables))
	if err != nil {
		return marshalGatewayReply(gatewayReply{Errors: gqlErrors(err)})
	}
	if len(operation.SelectionSet) > gatewayMaxRootFields {
		return marshalGatewayReply(gatewayReply{Errors: gatewayLimitError("root field")})
	}
	operation.SelectionSet, err = normalizeSelectionWithSchema(operation.SelectionSet, doc.Fragments, coerced, my.schema)
	if err != nil {
		return marshalGatewayReply(gatewayReply{Errors: gqlErrors(err)})
	}
	dataOperation, introspection := splitIntrospection(operation)
	data := make(map[string]any)
	var executed gatewayReply
	if dataOperation != nil {
		switch dataOperation.Operation {
		case ast.Query:
			executed = my.executeQuery(ctx, dataOperation, coerced)
		case ast.Mutation:
			executed = my.executeMutation(ctx, dataOperation, coerced)
		default:
			executed.Errors = gqlerror.List{gqlerror.Errorf("gateway暂不支持%s操作", dataOperation.Operation)}
		}
		if executed.Data == nil && len(executed.Errors) > 0 {
			return marshalGatewayReply(executed)
		}
		if !executed.NullData {
			for key, value := range executed.Data {
				data[key] = value
			}
		}
	}
	if introspection != nil {
		projected, introErr := my.intro.Introspect(introspection, coerced)
		if introErr != nil {
			executed.Errors = append(executed.Errors, gqlErrors(introErr)...)
		} else {
			for key, value := range projected {
				data[key] = value
			}
		}
	}
	executed.Data = data
	return marshalGatewayReply(executed)
}

type gatewayReply struct {
	Data       map[string]any
	Errors     gqlerror.List
	Extensions map[string]any
	NullData   bool
}

func marshalGatewayReply(reply gatewayReply) ([]byte, error) {
	if reply.NullData {
		return jsonReply.Marshal(struct {
			Data       any            `json:"data"`
			Errors     gqlerror.List  `json:"errors,omitempty"`
			Extensions map[string]any `json:"extensions,omitempty"`
		}{Data: nil, Errors: reply.Errors, Extensions: reply.Extensions})
	}
	if reply.Data == nil {
		return jsonReply.Marshal(struct {
			Errors     gqlerror.List  `json:"errors,omitempty"`
			Extensions map[string]any `json:"extensions,omitempty"`
		}{Errors: reply.Errors, Extensions: reply.Extensions})
	}
	return jsonReply.Marshal(struct {
		Data       any            `json:"data,omitempty"`
		Errors     gqlerror.List  `json:"errors,omitempty"`
		Extensions map[string]any `json:"extensions,omitempty"`
	}{Data: reply.Data, Errors: reply.Errors, Extensions: reply.Extensions})
}

func (my *gatewayState) executeQuery(ctx context.Context, operation *ast.OperationDefinition, variables map[string]any) gatewayReply {
	reply := gatewayReply{Data: make(map[string]any)}
	results := make([]queryResult, len(operation.SelectionSet))
	groups := make(map[*namespaceContract][]queryEntry)
	var wait sync.WaitGroup
	for index, selection := range operation.SelectionSet {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		if field.Name == "__typename" {
			results[index] = queryResult{key: responseKey(field), value: "Query"}
			continue
		}
		contract := my.contracts[field.Name]
		if contract == nil {
			continue
		}
		groups[contract] = append(groups[contract], queryEntry{index: index, field: field})
	}
	if len(groups) > gatewayMaxFanout {
		return gatewayReply{Errors: gatewayLimitError("namespace fan-out")}
	}
	for contract, entries := range groups {
		wait.Add(1)
		go func(contract *namespaceContract, entries []queryEntry) {
			defer wait.Done()
			my.executeQueryGroup(ctx, operation, variables, contract, entries, results)
		}(contract, entries)
	}
	wait.Wait()
	for _, result := range results {
		if result.key == "" {
			continue
		}
		reply.Data[result.key] = result.value
		reply.Errors = append(reply.Errors, result.errors...)
	}
	return reply
}

type queryEntry struct {
	index int
	field *ast.Field
}

type queryResult struct {
	key    string
	value  any
	errors gqlerror.List
}

type queryAlias struct {
	entry int
	key   string
}

func (my *gatewayState) executeQueryGroup(ctx context.Context, operation *ast.OperationDefinition, variables map[string]any, contract *namespaceContract, entries []queryEntry, results []queryResult) {
	set, aliases := groupedSelection(entries)
	query, childVariables := childOperation(ast.Query, set, operation.VariableDefinitions, variables, contract.publicToChild)
	body, err := contract.endpoint.Execute(ctx, query, childVariables)
	if err != nil {
		for _, entry := range entries {
			key := responseKey(entry.field)
			results[entry.index] = queryResult{key: key, errors: endpointError(key, entry.field, err)}
		}
		return
	}
	child, err := decodeEndpointReply(body)
	if err != nil {
		for _, entry := range entries {
			key := responseKey(entry.field)
			results[entry.index] = queryResult{key: key, errors: endpointProtocolError(key, entry.field)}
		}
		return
	}
	data := make([]map[string]any, len(entries))
	errors := make([]gqlerror.List, len(entries))
	for index := range data {
		data[index] = make(map[string]any)
	}
	for synthetic, alias := range aliases {
		if value, ok := child.Data[synthetic]; ok {
			data[alias.entry][alias.key] = value
		}
	}
	for _, childErr := range child.Errors {
		copy := cloneGraphQLError(childErr)
		name, ok := firstPathName(copy.Path)
		alias, mapped := aliases[name]
		if !ok || !mapped {
			for index := range errors {
				errors[index] = append(errors[index], gqlerror.Errorf("endpoint returned an unmappable error"))
			}
			continue
		}
		copy.Path[0] = ast.PathName(alias.key)
		errors[alias.entry] = append(errors[alias.entry], copy)
	}
	for index, entry := range entries {
		key := responseKey(entry.field)
		rewriteTypenames(my.schema, data[index], entry.field.SelectionSet, contract)
		definition := my.schema.Query.Fields.ForName(entry.field.Name)
		value, completionErrors, _ := completeEndpointValue(my.schema, data[index], definition.Type, entry.field.SelectionSet, nil, errors[index])
		errors[index] = append(errors[index], completionErrors...)
		prefixErrors(errors[index], key, entry.field)
		results[entry.index] = queryResult{key: key, value: value, errors: errors[index]}
	}
}

func groupedSelection(entries []queryEntry) (ast.SelectionSet, map[string]queryAlias) {
	aliases := make(map[string]queryAlias)
	if len(entries) == 1 {
		for _, selection := range entries[0].field.SelectionSet {
			if field, ok := selection.(*ast.Field); ok {
				key := responseKey(field)
				aliases[key] = queryAlias{key: key}
			}
		}
		return entries[0].field.SelectionSet, aliases
	}
	var set ast.SelectionSet
	for entryIndex, entry := range entries {
		for fieldIndex, selection := range entry.field.SelectionSet {
			field, ok := selection.(*ast.Field)
			if !ok {
				continue
			}
			clone := *field
			synthetic := fmt.Sprintf("_g%d_%d", entryIndex, fieldIndex)
			clone.Alias = synthetic
			set = append(set, &clone)
			aliases[synthetic] = queryAlias{entry: entryIndex, key: responseKey(field)}
		}
	}
	return set, aliases
}

func firstPathName(path ast.Path) (string, bool) {
	if len(path) == 0 {
		return "", false
	}
	name, ok := path[0].(ast.PathName)
	return string(name), ok
}

func cloneGraphQLError(err *gqlerror.Error) *gqlerror.Error {
	copy := *err
	copy.Path = append(ast.Path(nil), err.Path...)
	copy.Locations = append([]gqlerror.Location(nil), err.Locations...)
	if err.Extensions != nil {
		copy.Extensions = make(map[string]any, len(err.Extensions))
		for key, value := range err.Extensions {
			copy.Extensions[key] = value
		}
	}
	return &copy
}

func (my *gatewayState) executeMutation(ctx context.Context, operation *ast.OperationDefinition, variables map[string]any) gatewayReply {
	reply := gatewayReply{Data: make(map[string]any)}
	committed, committedSet := make([]string, 0), make(map[string]bool)
	indeterminate, indeterminateSet := make([]string, 0), make(map[string]bool)
	for _, selection := range operation.SelectionSet {
		field, ok := selection.(*ast.Field)
		if !ok {
			continue
		}
		if field.Name == "__typename" {
			reply.Data[responseKey(field)] = "Mutation"
			continue
		}
		contract := my.mutations[field.Name]
		if contract == nil {
			continue
		}
		key := responseKey(field)
		childField := *field
		childField.Name = contract.mutations[field.Name]
		childField.Alias = key
		query, childVariables := childOperation(ast.Mutation, ast.SelectionSet{&childField}, operation.VariableDefinitions, variables, contract.publicToChild)
		body, err := contract.endpoint.Execute(WithMutationCommitMetadata(ctx), query, childVariables)
		var child endpointReply
		uncertain := err != nil
		if err == nil {
			child, err = decodeEndpointReply(body)
			uncertain = err != nil
		}
		if err != nil {
			child.Errors = indeterminateCommitError(key, field)
		} else {
			if child.Data == nil {
				child.Data = make(map[string]any)
			}
			rewriteTypenames(my.schema, child.Data, ast.SelectionSet{field}, contract)
			definition := my.schema.Mutation.Fields.ForName(field.Name)
			completed, completionErrors, _ := completeEndpointValue(my.schema, child.Data[key], definition.Type, field.SelectionSet, ast.Path{ast.PathName(key)}, child.Errors)
			child.Data[key] = completed
			child.Errors = append(child.Errors, completionErrors...)
			prefixErrors(child.Errors, "", field)
		}
		value := child.Data[key]
		reply.Data[key] = value
		reply.Errors = append(reply.Errors, child.Errors...)
		commitState, _ := child.Extensions["mutationCommit"].(string)
		if commitState == "indeterminate" || (commitState == "" && len(child.Errors) > 0) {
			uncertain = true
		}
		if uncertain {
			markIndeterminateCommit(child.Errors)
			if !indeterminateSet[contract.name] {
				indeterminateSet[contract.name] = true
				indeterminate = append(indeterminate, contract.name)
			}
		} else if commitState != "rolled_back" && !committedSet[contract.name] {
			committedSet[contract.name] = true
			committed = append(committed, contract.name)
		}
		definition := my.schema.Mutation.Fields.ForName(field.Name)
		if len(child.Errors) > 0 && definition != nil && definition.Type.NonNull && value == nil {
			reply.NullData = true
			break
		}
	}
	if len(reply.Errors) > 0 {
		reply.Extensions = map[string]any{"committedNamespaces": committed}
		if len(indeterminate) > 0 {
			reply.Extensions["indeterminateNamespaces"] = indeterminate
		}
	}
	return reply
}

type endpointReply struct {
	Data       map[string]any `json:"data"`
	Errors     gqlerror.List  `json:"errors"`
	Extensions map[string]any `json:"extensions"`
}

func decodeEndpointReply(body []byte) (endpointReply, error) {
	var reply endpointReply
	if len(body) > gatewayMaxChildResponse {
		return reply, fmt.Errorf("endpoint response exceeds %d bytes", gatewayMaxChildResponse)
	}
	decoder := stdjson.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&reply); err != nil {
		return reply, fmt.Errorf("endpoint返回无效GraphQL JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return reply, fmt.Errorf("endpoint返回多个JSON值")
	} else if !errors.Is(err, io.EOF) {
		return reply, fmt.Errorf("endpoint返回无效尾部JSON: %w", err)
	}
	if reply.Data == nil && len(reply.Errors) == 0 {
		return reply, fmt.Errorf("endpoint响应缺少data或errors")
	}
	return reply, nil
}

func gatewayLimitError(resource string) gqlerror.List {
	err := gqlerror.Errorf("gateway %s limit exceeded", resource)
	err.Extensions = map[string]any{"code": "LIMIT_EXCEEDED"}
	return gqlerror.List{err}
}

func childOperation(kind ast.Operation, set ast.SelectionSet, definitions ast.VariableDefinitionList, variables map[string]any, reverse map[string]string) (string, map[string]any) {
	used := make(map[string]bool)
	collectVariables(set, used)
	var out strings.Builder
	out.WriteString(string(kind))
	childVariables := make(map[string]any, len(used))
	first := true
	for _, definition := range definitions {
		if !used[definition.Variable] {
			continue
		}
		if first {
			out.WriteString("(")
			first = false
		} else {
			out.WriteString(", ")
		}
		out.WriteString("$")
		out.WriteString(definition.Variable)
		out.WriteString(": ")
		writeChildType(&out, definition.Type, reverse)
		if value, ok := variables[definition.Variable]; ok {
			childVariables[definition.Variable] = cloneVariableValue(value)
		}
	}
	if !first {
		out.WriteString(")")
	}
	out.WriteString(" ")
	writeGatewaySelectionSet(&out, set, reverse)
	return out.String(), childVariables
}

func writeGatewaySelectionSet(out *strings.Builder, set ast.SelectionSet, reverse map[string]string) {
	out.WriteString("{")
	for _, selection := range set {
		switch node := selection.(type) {
		case *ast.Field:
			out.WriteString(" ")
			if node.Alias != "" && node.Alias != node.Name {
				out.WriteString(node.Alias)
				out.WriteString(": ")
			}
			out.WriteString(node.Name)
			if len(node.Arguments) > 0 {
				out.WriteString("(")
				for index, argument := range node.Arguments {
					if index > 0 {
						out.WriteString(", ")
					}
					out.WriteString(argument.Name)
					out.WriteString(": ")
					out.WriteString(argument.Value.String())
				}
				out.WriteString(")")
			}
			if len(node.SelectionSet) > 0 {
				out.WriteString(" ")
				writeGatewaySelectionSet(out, node.SelectionSet, reverse)
			}
		case *ast.InlineFragment:
			out.WriteString(" ...")
			if node.TypeCondition != "" {
				out.WriteString(" on ")
				if child := reverse[node.TypeCondition]; child != "" {
					out.WriteString(child)
				} else {
					out.WriteString(node.TypeCondition)
				}
			}
			out.WriteString(" ")
			writeGatewaySelectionSet(out, node.SelectionSet, reverse)
		}
	}
	if hasInlineFragment(set) {
		out.WriteString(" ")
		out.WriteString(gatewayTypeAlias(set))
		out.WriteString(": __typename")
	}
	out.WriteString(" }")
}

func hasInlineFragment(set ast.SelectionSet) bool {
	for _, selection := range set {
		if _, ok := selection.(*ast.InlineFragment); ok {
			return true
		}
	}
	return false
}

func gatewayTypeAlias(set ast.SelectionSet) string {
	for alias := gatewayTypeKey; ; alias += "_" {
		conflict := false
		for _, selection := range set {
			field, ok := selection.(*ast.Field)
			if ok && responseKey(field) == alias {
				conflict = true
				break
			}
		}
		if !conflict {
			return alias
		}
	}
}

func writeChildType(out *strings.Builder, typ *ast.Type, reverse map[string]string) {
	if typ.Elem != nil {
		out.WriteString("[")
		writeChildType(out, typ.Elem, reverse)
		out.WriteString("]")
	} else if child := reverse[typ.NamedType]; child != "" {
		out.WriteString(child)
	} else {
		out.WriteString(typ.NamedType)
	}
	if typ.NonNull {
		out.WriteString("!")
	}
}

func responseKey(field *ast.Field) string {
	if field.Alias != "" {
		return field.Alias
	}
	return field.Name
}

func endpointError(key string, field *ast.Field, _ error) gqlerror.List {
	gqlErr := gqlerror.Errorf("endpoint unavailable")
	gqlErr.Extensions = map[string]any{"code": "REMOTE_UNAVAILABLE"}
	if key != "" {
		gqlErr.Path = ast.Path{ast.PathName(key)}
	}
	setPublicLocation(gqlErr, field)
	return gqlerror.List{gqlErr}
}

func endpointProtocolError(key string, field *ast.Field) gqlerror.List {
	err := gqlerror.Errorf("endpoint returned an invalid GraphQL response")
	err.Path = ast.Path{ast.PathName(key)}
	err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
	setPublicLocation(err, field)
	return gqlerror.List{err}
}

func indeterminateCommitError(key string, field *ast.Field) gqlerror.List {
	err := gqlerror.Errorf("mutation commit status is indeterminate")
	err.Path = ast.Path{ast.PathName(key)}
	err.Extensions = map[string]any{"code": "INDETERMINATE_COMMIT"}
	setPublicLocation(err, field)
	return gqlerror.List{err}
}

func markIndeterminateCommit(errors gqlerror.List) {
	for _, err := range errors {
		if err.Extensions == nil {
			err.Extensions = make(map[string]any)
		}
		err.Extensions["code"] = "INDETERMINATE_COMMIT"
	}
}

func prefixErrors(errors gqlerror.List, prefix string, field *ast.Field) {
	for _, err := range errors {
		set := ast.SelectionSet{field}
		if prefix != "" {
			set = field.SelectionSet
		}
		position, valid := childErrorPosition(set, err.Path)
		if !valid {
			err.Message = "endpoint returned an invalid error path"
			err.Path = nil
			err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
			position = field
		}
		if prefix != "" {
			err.Path = append(ast.Path{ast.PathName(prefix)}, err.Path...)
		}
		setPublicLocation(err, position)
	}
}

func childErrorPosition(set ast.SelectionSet, path ast.Path) (*ast.Field, bool) {
	var position *ast.Field
	for _, element := range path {
		name, ok := element.(ast.PathName)
		if !ok { // List indices do not change the selected field.
			if _, index := element.(ast.PathIndex); index {
				continue
			}
			return nil, false
		}
		position = selectedField(set, string(name))
		if position == nil {
			return nil, false
		}
		set = position.SelectionSet
	}
	return position, position != nil
}

func selectedField(set ast.SelectionSet, key string) *ast.Field {
	for _, selection := range set {
		if field, ok := selection.(*ast.Field); ok && responseKey(field) == key {
			return field
		}
	}
	return nil
}

func setPublicLocation(err *gqlerror.Error, field *ast.Field) {
	if field != nil && field.Position != nil {
		err.Locations = []gqlerror.Location{{Line: field.Position.Line, Column: field.Position.Column}}
	}
}

func rewriteTypenames(schema *ast.Schema, value any, set ast.SelectionSet, contract *namespaceContract) {
	switch value := value.(type) {
	case []any:
		for _, item := range value {
			rewriteTypenames(schema, item, set, contract)
		}
	case map[string]any:
		runtimeType := ""
		if alias := gatewayTypeAlias(set); hasInlineFragment(set) {
			if name, ok := value[alias].(string); ok {
				runtimeType = publicTypename(contract, name)
				value[alias] = runtimeType
			}
		}
		for _, selection := range set {
			switch node := selection.(type) {
			case *ast.Field:
				key := responseKey(node)
				if node.Name == "__typename" {
					if name, ok := value[key].(string); ok {
						value[key] = publicTypename(contract, name)
					}
					continue
				}
				rewriteTypenames(schema, value[key], node.SelectionSet, contract)
			case *ast.InlineFragment:
				if typeConditionApplies(schema, node.TypeCondition, runtimeType) {
					rewriteTypenames(schema, value, node.SelectionSet, contract)
				}
			}
		}
	}
}

func publicTypename(contract *namespaceContract, name string) string {
	if name == "Query" {
		return contract.prefix + name
	}
	if contract.publicToChild[name] != "" { // Already rewritten by an overlapping fragment.
		return name
	}
	return contract.prefix + name
}

// completeEndpointValue projects untrusted child JSON through the public
// selection and reapplies GraphQL Non-Null bubbling before it crosses the
// namespace boundary.
func completeEndpointValue(schema *ast.Schema, value any, typ *ast.Type, set ast.SelectionSet, path ast.Path, known gqlerror.List) (any, gqlerror.List, bool) {
	if value == nil {
		if typ != nil && typ.NonNull {
			if endpointErrorCovers(known, path) {
				return nil, nil, true
			}
			err := gqlerror.ErrorPathf(path, "endpoint returned null for non-null field")
			err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
			return nil, gqlerror.List{err}, true
		}
		return nil, nil, false
	}
	if typ != nil && typ.Elem != nil {
		items, ok := value.([]any)
		if !ok {
			return invalidEndpointValue(path, typ, known)
		}
		out := make([]any, len(items))
		var errors gqlerror.List
		for index, item := range items {
			completed, itemErrors, violation := completeEndpointValue(schema, item, typ.Elem, set, gatewayAppendPath(path, ast.PathIndex(index)), known)
			errors = append(errors, itemErrors...)
			if violation {
				return nil, errors, typ.NonNull
			}
			out[index] = completed
		}
		return out, errors, false
	}
	if len(set) == 0 {
		if completed, ok := coerceEndpointScalar(schema, typ, value); ok {
			return completed, nil, false
		}
		return invalidEndpointValue(path, typ, known)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return invalidEndpointValue(path, typ, known)
	}
	out := make(map[string]any, len(set))
	var errors gqlerror.List
	runtimeType := ""
	if hasInlineFragment(set) {
		alias := gatewayTypeAlias(set)
		var ok bool
		runtimeType, ok = object[alias].(string)
		definition := schema.Types[runtimeType]
		if !ok || definition == nil || definition.Kind != ast.Object {
			return invalidEndpointValue(path, typ, known)
		}
	}
	for _, selection := range set {
		switch node := selection.(type) {
		case *ast.Field:
			key := responseKey(node)
			fieldPath := gatewayAppendPath(path, ast.PathName(key))
			if node.Name == "__typename" {
				name, ok := object[key].(string)
				definition := schema.Types[name]
				if !ok || definition == nil || definition.Kind != ast.Object {
					return invalidEndpointValue(fieldPath, ast.NonNullNamedType("String", nil), known)
				}
			}
			fieldType := (*ast.Type)(nil)
			if node.Definition != nil {
				fieldType = node.Definition.Type
			} else if node.Name == "__typename" {
				fieldType = ast.NonNullNamedType("String", nil)
			}
			completed, fieldErrors, violation := completeEndpointValue(schema, object[key], fieldType, node.SelectionSet, fieldPath, known)
			errors = append(errors, fieldErrors...)
			if violation {
				return nil, errors, typ != nil && typ.NonNull
			}
			out[key] = completed
		case *ast.InlineFragment:
			if !typeConditionApplies(schema, node.TypeCondition, runtimeType) {
				continue
			}
			completed, fragmentErrors, violation := completeEndpointValue(schema, object, typ, node.SelectionSet, path, known)
			errors = append(errors, fragmentErrors...)
			if violation {
				return nil, errors, typ != nil && typ.NonNull
			}
			fragment, ok := completed.(map[string]any)
			if !ok {
				return nil, errors, typ != nil && typ.NonNull
			}
			for key, value := range fragment {
				out[key] = value
			}
		}
	}
	return out, errors, false
}

func typeConditionApplies(schema *ast.Schema, condition, runtimeType string) bool {
	if condition == "" || condition == runtimeType {
		return true
	}
	for _, possible := range schema.PossibleTypes[condition] {
		if possible.Name == runtimeType {
			return true
		}
	}
	return false
}

func coerceEndpointScalar(schema *ast.Schema, typ *ast.Type, value any) (any, bool) {
	if typ == nil {
		return value, true
	}
	switch typ.NamedType {
	case "String":
		_, ok := value.(string)
		return value, ok
	case "ID":
		if _, ok := value.(string); ok {
			return value, true
		}
		if integer, ok := endpointInteger(value); ok {
			return strconv.FormatInt(integer, 10), true
		}
		return nil, false
	case "Int":
		integer, ok := endpointInteger(value)
		return value, ok && integer >= math.MinInt32 && integer <= math.MaxInt32
	case "Float":
		number, ok := endpointFloat(value)
		return value, ok && !math.IsInf(number, 0) && !math.IsNaN(number)
	case "Boolean":
		_, ok := value.(bool)
		return value, ok
	}
	definition := schema.Types[typ.NamedType]
	if definition == nil || definition.Kind != ast.Enum {
		return value, true // Custom scalar serialization belongs to its declaring endpoint.
	}
	name, ok := value.(string)
	return value, ok && definition.EnumValues.ForName(name) != nil
}

func endpointInteger(value any) (int64, bool) {
	switch value := value.(type) {
	case stdjson.Number:
		integer, err := value.Int64()
		return integer, err == nil
	case int:
		return int64(value), true
	case int32:
		return int64(value), true
	case int64:
		return value, true
	default:
		return 0, false
	}
}

func endpointFloat(value any) (float64, bool) {
	switch value := value.(type) {
	case stdjson.Number:
		number, err := value.Float64()
		return number, err == nil
	case float64:
		return value, true
	case float32:
		return float64(value), true
	default:
		integer, ok := endpointInteger(value)
		return float64(integer), ok
	}
}

func invalidEndpointValue(path ast.Path, typ *ast.Type, known gqlerror.List) (any, gqlerror.List, bool) {
	if endpointErrorCovers(known, path) {
		return nil, nil, typ != nil && typ.NonNull
	}
	err := gqlerror.ErrorPathf(path, "endpoint returned a value with the wrong shape")
	err.Extensions = map[string]any{"code": "BAD_GATEWAY_RESPONSE"}
	return nil, gqlerror.List{err}, typ != nil && typ.NonNull
}

func endpointErrorCovers(errors gqlerror.List, path ast.Path) bool {
	want := path.String()
	for _, err := range errors {
		have := err.Path.String()
		if have == want || (want != "" && strings.HasPrefix(have, want+".")) || (want != "" && strings.HasPrefix(have, want+"[")) {
			return true
		}
	}
	return false
}

func gatewayAppendPath(path ast.Path, element ast.PathElement) ast.Path {
	out := make(ast.Path, len(path), len(path)+1)
	copy(out, path)
	return append(out, element)
}
