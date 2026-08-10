package gql

import (
	"container/heap"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
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

// ReplayStore atomically accepts a token id once until its expiry.
type ReplayStore interface {
	Use(id string, expiry time.Time) bool
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
}

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
	replay := config.Replay
	if replay == nil {
		replay = &memoryReplayStore{clock: config.Clock, tokens: make(map[string]time.Time)}
	}
	config.Key = append([]byte(nil), config.Key...)
	return &DelegationCodec{config: config, replay: replay}, nil
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
	if claims.ExpiresAt == nil || claims.ID == "" || !my.replay.Use(claims.ID, claims.ExpiresAt.Time) {
		return nil, fmt.Errorf("delegation replay rejected")
	}
	identity, err := NewIdentity(IdentityInput{
		Tenant: claims.Tenant, Subject: claims.RegisteredClaims.Subject, Actor: claims.Actor, Roles: claims.Roles, Claims: claims.Claims,
		PolicyRevision: claims.PolicyRevision, CredentialEpoch: claims.CredentialEpoch, RequestID: claims.RequestID,
	})
	if err != nil {
		return nil, fmt.Errorf("delegation identity: %w", err)
	}
	return context.WithValue(WithIdentity(ctx, identity), delegationDepthKey{}, claims.Depth), nil
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
	tokens map[string]time.Time
	expiry replayExpiryHeap
}

func (my *memoryReplayStore) Use(id string, expiry time.Time) bool {
	my.mu.Lock()
	defer my.mu.Unlock()
	now := my.clock()
	for len(my.expiry) > 0 && !my.expiry[0].expiry.After(now) {
		item := heap.Pop(&my.expiry).(replayExpiry)
		if current, ok := my.tokens[item.id]; ok && current.Equal(item.expiry) {
			delete(my.tokens, item.id)
		}
	}
	if _, exists := my.tokens[id]; exists {
		return false
	}
	my.tokens[id] = expiry
	heap.Push(&my.expiry, replayExpiry{id: id, expiry: expiry})
	return true
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
