package compiler

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
)

// CursorContract is the immutable request/schema context a cursor belongs to.
// Sort contains the complete, deterministic ordering (including the PK tie-breaker).
type CursorContract struct {
	StructuralFingerprint string   `json:"s"`
	PolicyRevision        uint64   `json:"p"`
	Tenant                string   `json:"t"`
	Route                 string   `json:"r"`
	Coordinate            string   `json:"c"`
	Sort                  []string `json:"o"`
}

type cursorEnvelope struct {
	Version  uint8          `json:"v"`
	Contract CursorContract `json:"k"`
	Tuple    []any          `json:"x"`
}

type CursorError struct{ Err error }

func (e CursorError) Error() string     { return e.Err.Error() }
func (e CursorError) Unwrap() error     { return e.Err }
func (e CursorError) ErrorCode() string { return "BAD_CURSOR" }
func BadCursor(err error) error         { return CursorError{Err: err} }

// EncodeCursorToken creates a versioned cursor bound to schema, policy,
// tenant, route, coordinate and ordering. Its tuple is client-visible and
// untrusted; authorization and row policy are reapplied on every request.
func EncodeCursorToken(contract CursorContract, tuple []any) (string, error) {
	if err := validateCursorContract(contract); err != nil {
		return "", err
	}
	payload, err := json.Marshal(cursorEnvelope{Version: 1, Contract: contract, Tuple: tuple})
	if err != nil {
		return "", fmt.Errorf("encode cursor: %w", err)
	}
	return "v1." + base64.StdEncoding.EncodeToString(payload), nil
}

// DecodeCursorToken validates version, context contract and tuple shape.
func DecodeCursorToken(token string, expected CursorContract) ([]any, error) {
	if err := validateCursorContract(expected); err != nil {
		return nil, err
	}
	const prefix = "v1."
	if len(token) <= len(prefix) || token[:len(prefix)] != prefix {
		return nil, fmt.Errorf("invalid cursor: unsupported version")
	}
	payload, err := base64.StdEncoding.DecodeString(token[len(prefix):])
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: malformed payload")
	}
	var envelope cursorEnvelope
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || envelope.Version != 1 {
		return nil, fmt.Errorf("invalid cursor: malformed envelope")
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("invalid cursor: trailing payload")
	}
	if !reflect.DeepEqual(envelope.Contract, expected) {
		return nil, fmt.Errorf("invalid cursor: contract mismatch")
	}
	if len(envelope.Tuple) != len(expected.Sort) {
		return nil, fmt.Errorf("invalid cursor: ordering tuple mismatch")
	}
	return normalizeCursorNumbers(envelope.Tuple), nil
}

func validateCursorContract(c CursorContract) error {
	if c.StructuralFingerprint == "" || c.Tenant == "" || c.Route == "" || c.Coordinate == "" || len(c.Sort) == 0 {
		return fmt.Errorf("cursor contract is incomplete")
	}
	return nil
}

const (
	CursorTenantContext = "__gql_cursor_tenant"
	CursorRouteContext  = "__gql_cursor_route"
	CursorPolicyContext = "__gql_cursor_policy"
)

func ResolveCursorContract(contract CursorContract, scope map[string]any) CursorContract {
	if tenant, ok := scope[CursorTenantContext].(string); ok {
		contract.Tenant = tenant
	}
	if route, ok := scope[CursorRouteContext].(string); ok {
		contract.Route = route
	}
	switch revision := scope[CursorPolicyContext].(type) {
	case uint64:
		contract.PolicyRevision = revision
	case int64:
		contract.PolicyRevision = uint64(revision)
	case int:
		contract.PolicyRevision = uint64(revision)
	}
	return contract
}
