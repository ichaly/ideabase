package gql

import (
	"context"
	"encoding/base64"
	stdjson "encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDelegationCodecPropagatesTrustedScopeAndRejectsTampering(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	codec, err := NewDelegationCodec(DelegationConfig{Issuer: "gateway", Audience: "child", Key: key, Replay: NewMemoryReplayStore(8)})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user"})
	require.NoError(t, err)
	scope := map[string]any{"tenant_id": "tenant-a", "region": "cn"}
	token, err := codec.Sign(WithScope(WithIdentity(context.Background(), identity), scope), "operation")
	require.NoError(t, err)
	scope["region"] = "forged-after-sign"
	verified, err := codec.Verify(context.Background(), token, "operation")
	require.NoError(t, err)
	require.Equal(t, "cn", scopeValues(verified)["region"], "delegated scope must be immutable at signing time")

	parts := strings.Split(token, ".")
	require.Len(t, parts, 3)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	require.NoError(t, err)
	var claims map[string]any
	require.NoError(t, stdjson.Unmarshal(payload, &claims))
	claims["scope"] = map[string]any{"region": "forged"}
	payload, err = stdjson.Marshal(claims)
	require.NoError(t, err)
	parts[1] = base64.RawURLEncoding.EncodeToString(payload)
	_, err = codec.Verify(context.Background(), strings.Join(parts, "."), "operation")
	require.Error(t, err, "scope tampering must invalidate the signed envelope")
}

func TestDelegationCodecRejectsUnallowlistedScopeKeys(t *testing.T) {
	codec, err := NewDelegationCodec(DelegationConfig{Issuer: "gateway", Audience: "child", Key: []byte("0123456789abcdef0123456789abcdef"), Replay: NewMemoryReplayStore(8)})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant-a", Subject: "user"})
	require.NoError(t, err)
	_, err = codec.Sign(WithScope(WithIdentity(context.Background(), identity), map[string]any{"bad-key": "value"}), "operation")
	require.ErrorContains(t, err, "allowlisted")
}

type failingReplayStore struct{ err error }

func (store failingReplayStore) Consume(context.Context, ReplayKey, time.Time) error {
	return store.err
}

func TestDelegationCodecAuthenticatesBoundIdentityOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "gateway", Audience: "crm", Key: []byte("0123456789abcdef0123456789abcdef"),
		KeyEpoch: 7, PolicyRevision: 11, CredentialEpoch: 5, TTL: time.Minute, MaxDepth: 3,
		Clock: func() time.Time { return now }, Replay: newMemoryReplayStore(64, func() time.Time { return now }),
	})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{
		Tenant: "tenant-a", Subject: "user-1", Actor: "admin-1", Roles: []string{"editor"},
		Claims: map[string]any{"region": "cn"}, PolicyRevision: 12, CredentialEpoch: 6, RequestID: "request-1",
	})
	require.NoError(t, err)
	token, err := codec.Sign(WithIdentity(context.Background(), identity), "operation-sha256")
	require.NoError(t, err)

	verified, err := codec.Verify(context.Background(), token, "operation-sha256")
	require.NoError(t, err)
	require.False(t, delegatedExecution(WithIdentity(context.Background(), identity)))
	require.True(t, delegatedExecution(verified))
	got, ok := IdentityFrom(verified)
	require.True(t, ok)
	require.Equal(t, identity.Tenant(), got.Tenant())
	require.Equal(t, identity.Subject(), got.Subject())
	require.Equal(t, identity.Roles(), got.Roles())
	require.Equal(t, identity.Claims(), got.Claims())

	_, err = codec.Verify(context.Background(), token, "operation-sha256")
	require.ErrorContains(t, err, "replay")
}

func TestDelegationCodecRejectsWrongBoundaryAndExpiredToken(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	config := DelegationConfig{
		Issuer: "gateway", Audience: "crm", Key: []byte("0123456789abcdef0123456789abcdef"),
		KeyEpoch: 2, TTL: time.Minute, MaxDepth: 1, Clock: func() time.Time { return now },
		Replay: newMemoryReplayStore(64, func() time.Time { return now }),
	}
	codec, err := NewDelegationCodec(config)
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	token, err := codec.Sign(WithIdentity(context.Background(), identity), "operation-a")
	require.NoError(t, err)

	wrongAudience := config
	wrongAudience.Audience = "erp"
	verifier, err := NewDelegationCodec(wrongAudience)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), token, "operation-a")
	require.Error(t, err)
	_, err = codec.Verify(context.Background(), token, "operation-b")
	require.ErrorContains(t, err, "operation")

	expired := config
	expired.Clock = func() time.Time { return now.Add(2 * time.Minute) }
	verifier, err = NewDelegationCodec(expired)
	require.NoError(t, err)
	_, err = verifier.Verify(context.Background(), token, "operation-a")
	require.Error(t, err)
}

func TestDelegationCodecRejectsWeakConfigurationAndDepth(t *testing.T) {
	_, err := NewDelegationCodec(DelegationConfig{Issuer: "a", Audience: "b", Key: []byte("short")})
	require.Error(t, err)
	_, err = NewDelegationCodec(DelegationConfig{
		Issuer: "a", Audience: "b", Key: []byte("0123456789abcdef0123456789abcdef"),
	})
	require.ErrorContains(t, err, "replay store")
	replay := NewMemoryReplayStore(65536)
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "a", Audience: "b", Key: []byte("0123456789abcdef0123456789abcdef"), TTL: time.Minute, MaxDepth: 1, Replay: replay,
	})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	ctx := context.WithValue(WithIdentity(context.Background(), identity), delegationDepthKey{}, 1)
	_, err = codec.Sign(ctx, "operation")
	require.ErrorContains(t, err, "depth")
}

func TestDefaultReplayStoreDoesNotRejectValidTrafficAtAnArbitraryCapacity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	replay := newMemoryReplayStore(65536, func() time.Time { return now })
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "a", Audience: "b", Key: []byte("0123456789abcdef0123456789abcdef"), Clock: func() time.Time { return now },
		Replay: replay,
	})
	require.NoError(t, err)
	for i := 0; i < 5000; i++ {
		require.NoError(t, codec.replay.Consume(context.Background(), ReplayKey{ID: fmt.Sprint(i)}, now.Add(time.Minute)), "valid token %d was rejected", i)
	}
}

func TestDefaultReplayStoreFailsClosedAtConfiguredCapacity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	replay := newMemoryReplayStore(2, func() time.Time { return now })
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "a", Audience: "b", Key: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now }, Replay: replay,
	})
	require.NoError(t, err)
	require.NoError(t, codec.replay.Consume(context.Background(), ReplayKey{ID: "a"}, now.Add(time.Minute)))
	require.NoError(t, codec.replay.Consume(context.Background(), ReplayKey{ID: "b"}, now.Add(time.Minute)))
	require.Error(t, codec.replay.Consume(context.Background(), ReplayKey{ID: "c"}, now.Add(time.Minute)))
}

func TestDelegationReplayIsSharedAcrossInstances(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	replay := newMemoryReplayStore(64, func() time.Time { return now })
	config := DelegationConfig{
		Issuer: "gateway", Audience: "child", Key: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now }, Replay: replay,
	}
	first, err := NewDelegationCodec(config)
	require.NoError(t, err)
	second, err := NewDelegationCodec(config)
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	token, err := first.Sign(WithIdentity(context.Background(), identity), "operation")
	require.NoError(t, err)
	_, err = first.Verify(context.Background(), token, "operation")
	require.NoError(t, err)
	_, err = second.Verify(context.Background(), token, "operation")
	require.ErrorContains(t, err, "replay")
}

func TestDelegationReplayStoreFailureIsObservableAndFailsClosed(t *testing.T) {
	unavailable := errors.New("shared replay store unavailable")
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "gateway", Audience: "child", Key: []byte("0123456789abcdef0123456789abcdef"),
		Replay: failingReplayStore{err: unavailable},
	})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	token, err := codec.Sign(WithIdentity(context.Background(), identity), "operation")
	require.NoError(t, err)
	_, err = codec.Verify(context.Background(), token, "operation")
	require.ErrorIs(t, err, unavailable)
	require.NotErrorIs(t, err, ErrReplay)
}

func TestDelegationReplayRemainsConsumedThroughoutClockSkew(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "gateway", Audience: "child", Key: []byte("0123456789abcdef0123456789abcdef"),
		TTL: time.Minute, ClockSkew: 10 * time.Second, Clock: clock,
		Replay: newMemoryReplayStore(64, clock),
	})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	token, err := codec.Sign(WithIdentity(context.Background(), identity), "operation")
	require.NoError(t, err)
	now = now.Add(time.Minute + 5*time.Second)
	_, err = codec.Verify(context.Background(), token, "operation")
	require.NoError(t, err)
	_, err = codec.Verify(context.Background(), token, "operation")
	require.ErrorIs(t, err, ErrReplay)
}

func TestGADelegationReplayIsAtomicUnderClusterLoad(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	replay := newMemoryReplayStore(128, func() time.Time { return now })
	config := DelegationConfig{
		Issuer: "gateway", Audience: "child", Key: []byte("0123456789abcdef0123456789abcdef"),
		Clock: func() time.Time { return now }, Replay: replay,
	}
	codec, err := NewDelegationCodec(config)
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	token, err := codec.Sign(WithIdentity(context.Background(), identity), "operation")
	require.NoError(t, err)
	var accepted atomic.Int32
	var wait sync.WaitGroup
	for range 64 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			verifier, createErr := NewDelegationCodec(config)
			require.NoError(t, createErr)
			if _, verifyErr := verifier.Verify(context.Background(), token, "operation"); verifyErr == nil {
				accepted.Add(1)
			} else {
				require.ErrorIs(t, verifyErr, ErrReplay)
			}
		}()
	}
	wait.Wait()
	require.EqualValues(t, 1, accepted.Load())
}
