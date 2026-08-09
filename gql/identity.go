package gql

import (
	stdjson "encoding/json"
	"fmt"
)

// Identity is the verified request identity supplied by a host authenticator.
// Its state is immutable: accessors return defensive copies.
type Identity struct {
	tenant, subject, actor, requestID string
	roles                             []string
	claims                            map[string]any
	policyRevision, credentialEpoch   uint64
}

// IdentityInput is the trusted boundary for constructing an Identity. Callers
// must populate it from an authenticator or another trusted host integration.
type IdentityInput struct {
	Tenant          string
	Subject         string
	Actor           string
	Roles           []string
	Claims          map[string]any
	PolicyRevision  uint64
	CredentialEpoch uint64
	RequestID       string
}

func NewIdentity(input IdentityInput) (Identity, error) {
	if input.Tenant == "" {
		return Identity{}, fmt.Errorf("identity tenant is required")
	}
	if input.Subject == "" {
		return Identity{}, fmt.Errorf("identity subject is required")
	}
	claims, err := cloneClaims(input.Claims)
	if err != nil {
		return Identity{}, err
	}
	if input.Actor == "" {
		input.Actor = input.Subject
	}
	return Identity{
		tenant: input.Tenant, subject: input.Subject, actor: input.Actor,
		requestID: input.RequestID, roles: append([]string(nil), input.Roles...),
		claims: claims, policyRevision: input.PolicyRevision, credentialEpoch: input.CredentialEpoch,
	}, nil
}

func (i Identity) Tenant() string          { return i.tenant }
func (i Identity) Subject() string         { return i.subject }
func (i Identity) Actor() string           { return i.actor }
func (i Identity) RequestID() string       { return i.requestID }
func (i Identity) PolicyRevision() uint64  { return i.policyRevision }
func (i Identity) CredentialEpoch() uint64 { return i.credentialEpoch }
func (i Identity) Roles() []string         { return append([]string(nil), i.roles...) }
func (i Identity) Claims() map[string]any  { claims, _ := cloneClaims(i.claims); return claims }

func cloneClaims(in map[string]any) (map[string]any, error) {
	if in == nil {
		return nil, nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		copy, err := cloneClaim(v)
		if err != nil {
			return nil, fmt.Errorf("identity claim %q: %w", k, err)
		}
		out[k] = copy
	}
	return out, nil
}

func cloneClaim(v any) (any, error) {
	switch x := v.(type) {
	case map[string]any:
		return cloneClaims(x)
	case []any:
		out := make([]any, len(x))
		for n := range x {
			copy, err := cloneClaim(x[n])
			if err != nil {
				return nil, err
			}
			out[n] = copy
		}
		return out, nil
	case []string:
		return append([]string(nil), x...), nil
	case map[string]string:
		out := make(map[string]string, len(x))
		for k, value := range x {
			out[k] = value
		}
		return out, nil
	case stdjson.Number:
		if integer, err := x.Int64(); err == nil {
			return integer, nil
		}
		if number, err := x.Float64(); err == nil {
			return number, nil
		}
		return nil, fmt.Errorf("invalid JSON number")
	case nil, bool, string,
		int, int8, int16, int32, int64,
		uint, uint8, uint16, uint32, uint64,
		float32, float64:
		return v, nil
	default:
		return nil, fmt.Errorf("unsupported mutable value %T", v)
	}
}
