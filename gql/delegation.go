package gql

import (
	"container/heap"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// DelegationConfig defines one trust edge between a gateway and child endpoint.
type DelegationConfig struct {
	Issuer, Audience                string
	Key                             []byte
	KeyEpoch                        uint64
	PolicyRevision, CredentialEpoch uint64
	TTL, ClockSkew                  time.Duration
	MaxDepth                        int
	Clock                           func() time.Time
	Replay                          ReplayStore
}

var ErrReplay = errors.New("delegation replay")

type ReplayKey struct{ Issuer, Audience, ID string }

// ReplayStore atomically consumes a token once until its expiry. Backing-store
// errors remain distinguishable from ErrReplay while verification fails closed.
type ReplayStore interface {
	Consume(context.Context, ReplayKey, time.Time) error
}

// DelegationCodec signs and verifies short-lived identity envelopes.
type DelegationCodec struct {
	config DelegationConfig
	replay ReplayStore
}

type delegationDepthKey struct{}

type delegationClaims struct {
	jwt.RegisteredClaims
	Tenant          string         `json:"tenant"`
	Actor           string         `json:"actor,omitempty"`
	RequestID       string         `json:"request_id,omitempty"`
	Operation       string         `json:"operation"`
	Roles           []string       `json:"roles,omitempty"`
	Claims          map[string]any `json:"claims,omitempty"`
	PolicyRevision  uint64         `json:"policy_revision"`
	CredentialEpoch uint64         `json:"credential_epoch"`
	KeyEpoch        uint64         `json:"key_epoch"`
	Depth           int            `json:"depth"`
	ScopeVersion    uint8          `json:"scope_version,omitempty"`
	Scope           map[string]any `json:"scope,omitempty"`
}

const delegationScopeVersion uint8 = 1
const delegationScopeMaxBytes = 16 << 10

func NewDelegationCodec(config DelegationConfig) (*DelegationCodec, error) {
	if config.Issuer == "" || config.Audience == "" {
		return nil, fmt.Errorf("delegation issuer and audience are required")
	}
	if len(config.Key) < 32 {
		return nil, fmt.Errorf("delegation HMAC key must contain at least 32 bytes")
	}
	if config.TTL <= 0 {
		config.TTL = time.Minute
	}
	if config.TTL > 5*time.Minute || config.ClockSkew < 0 {
		return nil, fmt.Errorf("delegation TTL or clock skew is unsafe")
	}
	if config.MaxDepth <= 0 {
		config.MaxDepth = 4
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Replay == nil {
		return nil, fmt.Errorf("delegation replay store is required")
	}
	config.Key = append([]byte(nil), config.Key...)
	return &DelegationCodec{config: config, replay: config.Replay}, nil
}

// Sign binds the verified context identity to one exact operation digest.
func (my *DelegationCodec) Sign(ctx context.Context, operation string) (string, error) {
	identity, ok := IdentityFromContext(ctx)
	if !ok {
		return "", fmt.Errorf("delegation requires a verified identity")
	}
	depth, _ := ctx.Value(delegationDepthKey{}).(int)
	depth++
	if depth > my.config.MaxDepth {
		return "", fmt.Errorf("delegation depth exceeds %d", my.config.MaxDepth)
	}
	if operation == "" {
		return "", fmt.Errorf("delegation operation digest is required")
	}
	scope, err := delegationScope(ctx)
	if err != nil {
		return "", err
	}
	now := my.config.Clock()
	id, err := randomTokenID()
	if err != nil {
		return "", err
	}
	claims := delegationClaims{
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer: my.config.Issuer, Subject: identity.Subject(), Audience: jwt.ClaimStrings{my.config.Audience},
			ExpiresAt: jwt.NewNumericDate(now.Add(my.config.TTL)), NotBefore: jwt.NewNumericDate(now), IssuedAt: jwt.NewNumericDate(now), ID: id,
		},
		Tenant: identity.Tenant(), Actor: identity.Actor(), RequestID: identity.RequestID(),
		Roles: identity.Roles(), Claims: identity.Claims(), PolicyRevision: identity.PolicyRevision(),
		CredentialEpoch: identity.CredentialEpoch(), KeyEpoch: my.config.KeyEpoch, Depth: depth, Operation: operation,
		ScopeVersion: scopeVersion(scope), Scope: scope,
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(my.config.Key)
}

// Verify authenticates one envelope, its trust epochs, operation and replay id.
func (my *DelegationCodec) Verify(ctx context.Context, token, operation string) (context.Context, error) {
	claims := new(delegationClaims)
	parsed, err := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithIssuer(my.config.Issuer),
		jwt.WithAudience(my.config.Audience), jwt.WithLeeway(my.config.ClockSkew), jwt.WithTimeFunc(my.config.Clock), jwt.WithIssuedAt(), jwt.WithJSONNumber(),
	).ParseWithClaims(token, claims, func(*jwt.Token) (any, error) { return my.config.Key, nil })
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("invalid delegation token: %w", err)
	}
	if claims.KeyEpoch != my.config.KeyEpoch || claims.PolicyRevision < my.config.PolicyRevision || claims.CredentialEpoch < my.config.CredentialEpoch {
		return nil, fmt.Errorf("delegation trust revision is stale")
	}
	if claims.Depth < 1 || claims.Depth > my.config.MaxDepth {
		return nil, fmt.Errorf("delegation depth is invalid")
	}
	if subtle.ConstantTimeCompare([]byte(claims.Operation), []byte(operation)) != 1 {
		return nil, fmt.Errorf("delegation operation does not match")
	}
	if claims.ExpiresAt == nil || claims.ID == "" {
		return nil, ErrReplay
	}
	if err := my.replay.Consume(ctx, ReplayKey{Issuer: claims.Issuer, Audience: my.config.Audience, ID: claims.ID}, claims.ExpiresAt.Time.Add(my.config.ClockSkew)); err != nil {
		return nil, fmt.Errorf("delegation replay rejected: %w", err)
	}
	identity, err := NewIdentity(IdentityInput{
		Tenant: claims.Tenant, Subject: claims.RegisteredClaims.Subject, Actor: claims.Actor, Roles: claims.Roles, Claims: claims.Claims,
		PolicyRevision: claims.PolicyRevision, CredentialEpoch: claims.CredentialEpoch, RequestID: claims.RequestID,
	})
	if err != nil {
		return nil, fmt.Errorf("delegation identity: %w", err)
	}
	scope, err := verifiedDelegationScope(claims)
	if err != nil {
		return nil, err
	}
	verified := WithIdentity(ctx, identity)
	if scope != nil {
		verified = WithScope(verified, scope)
	}
	verified = context.WithValue(verified, delegationDepthKey{}, claims.Depth)
	return withDelegatedExecution(verified), nil
}

func scopeVersion(scope map[string]any) uint8 {
	if len(scope) == 0 {
		return 0
	}
	return delegationScopeVersion
}

func delegationScope(ctx context.Context) (map[string]any, error) {
	scope, _ := ctx.Value(scopeKey{}).(map[string]any)
	return cloneDelegationScope(scope)
}

func verifiedDelegationScope(claims *delegationClaims) (map[string]any, error) {
	if claims.ScopeVersion == 0 && len(claims.Scope) == 0 {
		return nil, nil
	}
	if claims.ScopeVersion != delegationScopeVersion {
		return nil, fmt.Errorf("unsupported delegation scope version")
	}
	return cloneDelegationScope(claims.Scope)
}

func cloneDelegationScope(scope map[string]any) (map[string]any, error) {
	if len(scope) == 0 {
		return nil, nil
	}
	cloned := make(map[string]any, len(scope))
	for key, value := range scope {
		if !delegationScopeKeyAllowed(key) {
			return nil, fmt.Errorf("delegation scope key is not allowlisted: %q", key)
		}
		cloned[key] = cloneVariableValue(value)
	}
	encoded, err := stdjson.Marshal(cloned)
	if err != nil {
		return nil, fmt.Errorf("delegation scope is not JSON-safe: %w", err)
	}
	if len(encoded) > delegationScopeMaxBytes {
		return nil, fmt.Errorf("delegation scope exceeds %d bytes", delegationScopeMaxBytes)
	}
	return cloned, nil
}

func delegationScopeKeyAllowed(key string) bool {
	if key == "" {
		return false
	}
	for index, char := range []byte(key) {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || char == '_' || index > 0 && char >= '0' && char <= '9' {
			continue
		}
		return false
	}
	return true
}

func randomTokenID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", fmt.Errorf("delegation token id: %w", err)
	}
	return hex.EncodeToString(id[:]), nil
}

type memoryReplayStore struct {
	mu     sync.Mutex
	clock  func() time.Time
	limit  int
	tokens map[string]time.Time
	expiry replayExpiryHeap
}

// NewMemoryReplayStore is the bounded standalone ReplayStore. Clustered
// deployments must inject a shared store with atomic use-once semantics.
func NewMemoryReplayStore(capacity int) ReplayStore {
	return newMemoryReplayStore(capacity, time.Now)
}

func newMemoryReplayStore(capacity int, clock func() time.Time) *memoryReplayStore {
	if capacity <= 0 {
		capacity = 65536
	}
	return &memoryReplayStore{clock: clock, limit: capacity, tokens: make(map[string]time.Time)}
}

func (my *memoryReplayStore) Consume(ctx context.Context, key ReplayKey, expiry time.Time) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	my.mu.Lock()
	defer my.mu.Unlock()
	id := key.Issuer + "\x00" + key.Audience + "\x00" + key.ID
	now := my.clock()
	for len(my.expiry) > 0 && !my.expiry[0].expiry.After(now) {
		item := heap.Pop(&my.expiry).(replayExpiry)
		if current, ok := my.tokens[item.id]; ok && current.Equal(item.expiry) {
			delete(my.tokens, item.id)
		}
	}
	if _, exists := my.tokens[id]; exists {
		return ErrReplay
	}
	if len(my.tokens) >= my.limit {
		return fmt.Errorf("replay store capacity exhausted")
	}
	my.tokens[id] = expiry
	heap.Push(&my.expiry, replayExpiry{id: id, expiry: expiry})
	return nil
}

type replayExpiry struct {
	id     string
	expiry time.Time
}

type replayExpiryHeap []replayExpiry

func (h replayExpiryHeap) Len() int           { return len(h) }
func (h replayExpiryHeap) Less(i, j int) bool { return h[i].expiry.Before(h[j].expiry) }
func (h replayExpiryHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *replayExpiryHeap) Push(value any)    { *h = append(*h, value.(replayExpiry)) }
func (h *replayExpiryHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}
