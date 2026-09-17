// 全文搜索能力探测：PostgreSQL启动时探测，其他驱动使用ilike。
package gql

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/ichaly/ideabase/std"
	"github.com/jackc/pgx/v5/pgconn"
)

const startupProbeTimeout = 5 * time.Second

// detectPostgresSearch 探测PostgreSQL全文搜索能力：
// 1. 存在挂在jieba/zhparser解析器上的分词配置则tsvector（中文真分词）；
// 2. pg_trgm可用（尝试自动创建，contrib模块官方镜像自带）则trigram；3. 否则ilike降级
func startupProbeContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, startupProbeTimeout)
}

func detectPostgresSearch(db *std.Connection) (string, string, error) {
	if db == nil {
		return "", "", fmt.Errorf("database is required for search capability detection")
	}
	ctx, cancel := startupProbeContext(context.Background())
	defer cancel()
	var config string
	if err := db.Pool().QueryRowContext(ctx, `SELECT COALESCE((SELECT c.cfgname FROM pg_ts_config c
		JOIN pg_ts_parser p ON c.cfgparser = p.oid
		WHERE p.prsname IN ('jieba', 'zhparser') LIMIT 1), '')`).Scan(&config); err != nil {
		return "", "", fmt.Errorf("detect text search configuration: %w", err)
	} else if config != "" {
		return "tsvector", config, nil
	}

	// 先探测已安装（常态），缺失才尝试创建（权限不足时静默降级）
	probe := func() (bool, error) {
		var trigram int
		err := db.Pool().QueryRowContext(ctx, `SELECT COALESCE((SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm'), 0)`).Scan(&trigram)
		return trigram == 1, err
	}
	if installed, err := probe(); err != nil {
		return "", "", fmt.Errorf("detect pg_trgm: %w", err)
	} else if installed {
		return "trigram", "", nil
	}
	if _, err := db.Pool().ExecContext(ctx, `CREATE EXTENSION IF NOT EXISTS pg_trgm`); err != nil && !isPermissionDenied(err) {
		return "", "", fmt.Errorf("create pg_trgm extension: %w", err)
	}
	if installed, err := probe(); err != nil {
		return "", "", fmt.Errorf("verify pg_trgm: %w", err)
	} else if installed {
		return "trigram", "", nil
	}
	return "ilike", "", nil
}

func isPermissionDenied(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "42501"
}
