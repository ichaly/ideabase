package gql

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/ichaly/ideabase/gql/internal"
	"github.com/ichaly/ideabase/std"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCDCUsesConnectionDSN(t *testing.T) {
	config := &std.Config{}
	config.Database = "postgres://localhost/demo?sslmode=disable"
	connection, err := std.NewConnection(config)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, connection.Close()) })
	// Invalid publication stops validation before network access. Reaching this
	// error proves CDC obtained the saved DSN instead of rejecting it as missing.
	_, err = newPostgresNotifier(connection, internal.SubscriptionConfig{Publication: "invalid-name"})
	require.ErrorContains(t, err, "publication名称不安全")
}

func TestListenerCloseWaitsForRunAndIsIdempotent(t *testing.T) {
	l := newListener("", "")
	started := make(chan struct{})
	release := make(chan struct{})
	l.runWG.Add(1)
	go func() {
		defer l.runWG.Done()
		close(started)
		<-release
	}()
	<-started

	done := make(chan struct{})
	go func() {
		l.close()
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("close returned before run exited")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close did not wait for run")
	}
	l.close()
	_, err := l.watch([]string{"public.users"})
	require.ErrorContains(t, err, "已关闭")
}

func TestListenerValidatedIdleIsNotConnected(t *testing.T) {
	l := newListener("", "")
	l.setIdleValidated()
	state, message := l.cdcState()
	require.Equal(t, CDCStateIdleValidated, state)
	require.Empty(t, message)
	connected, _ := l.status()
	require.True(t, connected, "legacy CDCConnected remains ready-compatible while idle")
	l.setStatus(false, fmt.Errorf("connection lost"))
	state, _ = l.cdcState()
	require.Equal(t, CDCStateDisconnected, state)
}

func TestReplicationSlotNamesAreClusterSafePostgresIdentifiers(t *testing.T) {
	valid := regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
	seen := make(map[string]struct{}, 1000)
	for range 1000 {
		name, err := replicationSlotName()
		require.NoError(t, err)
		require.True(t, valid.MatchString(name), name)
		require.LessOrEqual(t, len(name), 63)
		_, duplicate := seen[name]
		require.False(t, duplicate, name)
		seen[name] = struct{}{}
	}
}

func TestCDCRejectsUnsafePublicationNamesBeforeConnecting(t *testing.T) {
	for _, name := range []string{
		`public.ideabase_cdc`,
		`ideabase-cdc`,
		`ideabase_cdc' WITH (publish = 'truncate') --`,
		`ideabase_cdc\" FOR TABLE secrets --`,
		strings.Repeat("a", 64),
	} {
		t.Run(name, func(t *testing.T) {
			listener := newListener("host=127.0.0.1 port=1 connect_timeout=1", name)
			err := listener.validate()
			require.ErrorContains(t, err, "CDC publication名称不安全")
			listener.close()
		})
	}
}

// TestListenerWatchRetryAfterFailure 首次建连失败不得缓存错误（原sync.Once会永久缓存）：
// started保持未置位，数据库恢复后下一次Subscribe可自动重试
func TestListenerWatchRetryAfterFailure(t *testing.T) {
	// 指向必然拒绝连接的地址，建连快速失败
	l := newListener("host=127.0.0.1 port=1 user=x password=x dbname=x sslmode=disable connect_timeout=1", "")

	_, err := l.watch([]string{"users"})
	require.Error(t, err, "建连失败应返回错误")
	assert.False(t, l.started, "失败不应置位started，为下次重试留路")
	assert.Empty(t, l.watchers, "失败时不应登记watcher")

	// 第二次调用应再次尝试建连（而非命中Once缓存直接返回旧错误或误判成功）
	_, err = l.watch([]string{"users"})
	require.Error(t, err, "DB仍不可用时第二次调用应重试并再次报错")
	assert.False(t, l.started)
}
