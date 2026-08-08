package gql

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestDelegationCodecAuthenticatesBoundIdentityOnce(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "gateway", Audience: "crm", Key: []byte("0123456789abcdef0123456789abcdef"),
		KeyEpoch: 7, PolicyRevision: 11, CredentialEpoch: 5, TTL: time.Minute, MaxDepth: 3,
		Clock: func() time.Time { return now },
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
	got, ok := IdentityFromContext(verified)
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
	codec, err := NewDelegationCodec(DelegationConfig{
		Issuer: "a", Audience: "b", Key: []byte("0123456789abcdef0123456789abcdef"), TTL: time.Minute, MaxDepth: 1,
	})
	require.NoError(t, err)
	identity, err := NewIdentity(IdentityInput{Tenant: "tenant", Subject: "user"})
	require.NoError(t, err)
	ctx := context.WithValue(WithIdentity(context.Background(), identity), delegationDepthKey{}, 1)
	_, err = codec.Sign(ctx, "operation")
	require.ErrorContains(t, err, "depth")
}
