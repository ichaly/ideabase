// Orders demonstrates a production-mode, tenant-isolated database API with two business resolvers.
package main

import (
	"context"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ichaly/ideabase/gql"
	"github.com/ichaly/ideabase/gql/transport"
	"github.com/ichaly/ideabase/std"
	_ "github.com/ichaly/ideabase/std/postgres"
)

//go:embed schema.sql
var schemaSQL string

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	migrate := flag.Bool("migrate", false, "initialize an empty database using its migration owner")
	issue := flag.Bool("token", false, "issue a local test token; production login supplies JWTs")
	tenant := flag.String("tenant", "a", "tenant for local test token")
	role := flag.String("role", "operator", "operator or viewer for local test token")
	flag.Parse()
	if flag.NArg() != 0 {
		return errors.New("unexpected positional arguments")
	}
	secret := []byte(os.Getenv("JWT_SECRET"))
	if *issue {
		if len(secret) < 32 || (*role != "operator" && *role != "viewer") {
			return errors.New("set JWT_SECRET (at least 32 bytes) and a valid role")
		}
		value, err := jwt.NewWithClaims(jwt.SigningMethodHS256, accessClaims{Tenant: *tenant, Roles: []string{*role}, RegisteredClaims: jwt.RegisteredClaims{
			Issuer: "orders-auth", Audience: jwt.ClaimStrings{"orders-api"}, Subject: "local-user", ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
		}}).SignedString(secret)
		if err == nil {
			fmt.Println(value)
		}
		return err
	}
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	config := &std.Config{}
	config.Database = dsn
	db, err := std.NewConnection(config)
	if err != nil {
		return err
	}
	defer db.Close()
	pool := db.Pool()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	tenants := strings.Split(os.Getenv("ORDER_TENANTS"), ",")
	for i, t := range tenants {
		tenants[i] = strings.TrimSpace(t)
		if tenants[i] == "" {
			return errors.New("ORDER_TENANTS must list trusted tenant IDs")
		}
	}
	if *migrate {
		migration, err := pool.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer migration.Rollback()
		if _, err = migration.ExecContext(ctx, schemaSQL+gql.PostgresSecurityStateDDL+gql.PostgresReplayDDL+`CREATE PUBLICATION ideabase_cdc FOR ALL TABLES;`); err != nil {
			return err
		}
		if err = migration.Commit(); err != nil {
			return err
		}
		security, err := gql.NewPostgresSecurityStateProvider(db)
		if err != nil {
			return err
		}
		states := make(map[string]gql.TenantSecurityState, len(tenants))
		for _, t := range tenants {
			states[t] = gql.TenantSecurityState{}
		}
		return security.Replace(ctx, 1, states)
	}
	if len(secret) < 32 {
		return errors.New("JWT_SECRET must contain at least 32 bytes")
	}
	engine, endpoint, err := buildEndpoint(db, os.Getenv("CDC_DATABASE_URL"), tenants, nil)
	if err != nil {
		return err
	}
	defer engine.Close()
	handler, err := newHTTPHandler(endpoint, secret)
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	mux.Handle("/graphql", handler)
	mux.Handle("/health", transport.NewHealthHandler(endpoint))
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:8080"
	}
	server := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10}
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-ctx.Done():
		engine.Close() // End SSE streams before waiting for HTTP handlers.
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
