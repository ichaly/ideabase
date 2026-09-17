package gql

// These tests are deliberately opt-in because they start independent test
// processes and require a real PostgreSQL server. Run with:
//   GQL_RUN_CLUSTER_GA=1 go test ./gql -run TestGACluster -count=1

import (
	"bufio"
	"context"
	"fmt"
	"github.com/ichaly/ideabase/std"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	gaChildEnv = "GQL_GA_CHILD"
	gaDSNEnv   = "GQL_GA_DSN"
)

func TestGAClusterChild(t *testing.T) {
	mode := os.Getenv(gaChildEnv)
	if mode == "" {
		return
	}
	dsn := os.Getenv(gaDSNEnv)
	if dsn == "" {
		t.Fatal("missing GQL_GA_DSN")
	}
	config := &std.Config{}
	config.Database = dsn
	db, err := std.NewConnection(config)
	require.NoError(t, err)

	switch mode {
	case "replay":
		store, err := NewPostgresReplayStore(db)
		require.NoError(t, err)
		err = store.Consume(context.Background(), ReplayKey{Issuer: "ga", Audience: "child", ID: "one-token"}, time.Now().Add(time.Minute))
		if err == nil {
			fmt.Println("accepted")
			return
		}
		if err == ErrReplay {
			fmt.Println("replay")
			return
		}
		t.Fatalf("consume replay token: %v", err)
	case "security":
		provider, err := NewPostgresSecurityStateProvider(db)
		require.NoError(t, err)
		identity, err := NewIdentity(IdentityInput{Tenant: "ga-tenant", Subject: "worker", PolicyRevision: 1, CredentialEpoch: 1})
		require.NoError(t, err)
		err = provider.Check(context.Background(), identity, TenantRoute{Tenant: "ga-tenant", Revision: 1})
		if err == nil {
			fmt.Println("allowed")
			return
		}
		fmt.Printf("denied:%s\n", err)
	case "active-security":
		provider, err := NewPostgresSecurityStateProvider(db)
		require.NoError(t, err)
		identity, err := NewIdentity(IdentityInput{Tenant: "ga-tenant", Subject: "worker", PolicyRevision: 1, CredentialEpoch: 1})
		require.NoError(t, err)
		for i := 0; i < 50; i++ {
			err = provider.Check(context.Background(), identity, TenantRoute{Tenant: "ga-tenant", Revision: 1})
			if err != nil {
				fmt.Printf("denied:%s\n", err)
			} else {
				fmt.Println("allowed")
			}
			time.Sleep(20 * time.Millisecond)
		}
	default:
		t.Fatalf("unknown GA child mode %q", mode)
	}
}

func TestGAClusterReplayIsConsumedOnceAcrossProcesses(t *testing.T) {
	dsn, cleanup := gaClusterDatabase(t)
	defer cleanup()
	config := &std.Config{}
	config.Database = dsn
	db, err := std.NewConnection(config)
	require.NoError(t, err)
	testExec(t, db, PostgresReplayDDL)

	outputs := make(chan string, 2)
	for range 2 {
		go func() {
			cmd := exec.Command(os.Args[0], "-test.run=TestGAClusterChild", "-test.count=1")
			cmd.Env = append(os.Environ(), gaChildEnv+"=replay", gaDSNEnv+"="+dsn)
			out, runErr := cmd.CombinedOutput()
			if runErr != nil {
				outputs <- fmt.Sprintf("error:%v:%s", runErr, out)
				return
			}
			outputs <- string(out)
		}()
	}
	var accepted, replay int
	for range 2 {
		out := <-outputs
		if strings.Contains(out, "accepted") {
			accepted++
		}
		if strings.Contains(out, "replay") {
			replay++
		}
		require.NotContains(t, out, "error:", "child output: %s", out)
	}
	require.Equal(t, 1, accepted, "exactly one independent process may consume the token")
	require.Equal(t, 1, replay, "the other independent process must observe replay")
}

func TestGAClusterSecurityRevocationIsObservedByAnotherProcess(t *testing.T) {
	dsn, cleanup := gaClusterDatabase(t)
	defer cleanup()
	config := &std.Config{}
	config.Database = dsn
	db, err := std.NewConnection(config)
	require.NoError(t, err)
	testExec(t, db, PostgresSecurityStateDDL)
	provider, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 1, map[string]TenantSecurityState{"ga-tenant": {}}))

	before := gaRunChild(t, dsn, "security")
	require.Contains(t, before, "allowed")
	require.NoError(t, provider.Replace(context.Background(), 2, map[string]TenantSecurityState{"ga-tenant": {Disabled: true}}))
	after := gaRunChild(t, dsn, "security")
	require.Contains(t, after, "denied")
	require.Contains(t, after, "disabled")
}

func TestGAClusterRevocationPropagatesDuringActiveServing(t *testing.T) {
	dsn, cleanup := gaClusterDatabase(t)
	defer cleanup()
	config := &std.Config{}
	config.Database = dsn
	db, err := std.NewConnection(config)
	require.NoError(t, err)
	testExec(t, db, PostgresSecurityStateDDL)
	provider, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 10, map[string]TenantSecurityState{"ga-tenant": {}}))
	cmd := exec.Command(os.Args[0], "-test.run=TestGAClusterChild", "-test.count=1")
	cmd.Env = append(os.Environ(), gaChildEnv+"=active-security", gaDSNEnv+"="+dsn)
	stdout, err := cmd.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, cmd.Start())
	scanner := bufio.NewScanner(stdout)
	lines := make(chan string, 8)
	go func() {
		for scanner.Scan() {
			lines <- scanner.Text()
		}
		close(lines)
	}()
	seenAllowed := false
	for !seenAllowed {
		line, ok := <-lines
		require.True(t, ok, "active child exited before serving")
		seenAllowed = line == "allowed"
	}
	require.True(t, seenAllowed, "active child never served before revocation")
	require.NoError(t, provider.Replace(context.Background(), 11, map[string]TenantSecurityState{"ga-tenant": {Disabled: true}}))
	seenDenied := false
	deadline := time.After(2 * time.Second)
	for !seenDenied {
		select {
		case <-deadline:
			_ = cmd.Process.Kill()
			t.Fatal("active child did not observe revocation")
		case line, ok := <-lines:
			if ok && strings.HasPrefix(line, "denied:") {
				seenDenied = true
			}
		}
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}

func TestGAClusterRevisionRejectsStalePublishAndKeepsFreshState(t *testing.T) {
	dsn, cleanup := gaClusterDatabase(t)
	defer cleanup()
	config := &std.Config{}
	config.Database = dsn
	db, err := std.NewConnection(config)
	require.NoError(t, err)
	testExec(t, db, PostgresSecurityStateDDL)
	provider, err := NewPostgresSecurityStateProvider(db)
	require.NoError(t, err)
	require.NoError(t, provider.Replace(context.Background(), 20, map[string]TenantSecurityState{"ga-tenant": {}}))
	require.Error(t, provider.Replace(context.Background(), 19, map[string]TenantSecurityState{"ga-tenant": {Disabled: true}}))
	require.NoError(t, provider.Replace(context.Background(), 21, map[string]TenantSecurityState{"ga-tenant": {Disabled: true}}))
	revision, err := provider.Revision(context.Background())
	require.NoError(t, err)
	require.EqualValues(t, 21, revision)
}

func gaRunChild(t *testing.T, dsn, mode string) string {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestGAClusterChild", "-test.count=1")
	cmd.Env = append(os.Environ(), gaChildEnv+"="+mode, gaDSNEnv+"="+dsn)
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "child output: %s", out)
	return string(out)
}

func gaClusterDatabase(t *testing.T) (string, func()) {
	t.Helper()
	if os.Getenv("GQL_RUN_CLUSTER_GA") != "1" {
		t.Skip("set GQL_RUN_CLUSTER_GA=1 to run real multi-process PostgreSQL GA")
	}
	if dsn := os.Getenv("GQL_GA_POSTGRES_DSN"); dsn != "" {
		return dsn, func() {}
	}
	db, cleanup := setupTestDatabase(t)
	dsn := db.DSN()
	return dsn, cleanup
}
