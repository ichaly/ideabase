package gql

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
