package main

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/transport"
)

type accessClaims struct {
	Tenant string   `json:"tenant"`
	Roles  []string `json:"roles"`
	Epoch  uint64   `json:"epoch"`
	jwt.RegisteredClaims
}

// Authentication owns the identity boundary; no request header supplies trusted scope.
func newHTTPHandler(endpoint gql.Endpoint, secret []byte) (http.Handler, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT_SECRET must contain at least 32 bytes")
	}
	graphql := transport.NewHandler(endpoint, transport.Options{})
	parser := jwt.NewParser(jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("orders-auth"), jwt.WithAudience("orders-api"), jwt.WithExpirationRequired())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		claims := &accessClaims{}
		raw, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_, err := parser.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return secret, nil })
		if err != nil || claims.Subject == "" || claims.Tenant == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		id, err := gql.NewIdentity(gql.IdentityInput{Tenant: claims.Tenant, Subject: claims.Subject, Roles: claims.Roles, CredentialEpoch: claims.Epoch})
		if err != nil {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		ctx := gql.WithScope(gql.WithIdentity(r.Context(), id), map[string]any{"tenant": claims.Tenant})
		ctx, cancel := context.WithDeadline(ctx, claims.ExpiresAt.Time)
		defer cancel()
		graphql.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}
