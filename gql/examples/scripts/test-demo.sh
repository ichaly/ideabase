#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

cleanup() {
	if [[ -n "${SERVER_PID:-}" ]]; then
		kill "$SERVER_PID" 2>/dev/null || true
		wait "$SERVER_PID" 2>/dev/null || true
	fi
	docker compose down --volumes --remove-orphans >/dev/null
}
trap cleanup EXIT

# 每轮使用全新 schema/seed，确保 mutation 验收不是依赖上一次运行状态。
docker compose down --volumes --remove-orphans
docker compose up -d --wait
go test . -count=1

# 真实多进程 GA 门禁：两个独立 test binary 进程共享 Demo PostgreSQL 的
# replay/security 事实源，覆盖一次消费、撤权传播、活动副本和 revision 栅栏。
docker compose exec -T postgres psql -U demo -d demo -v ON_ERROR_STOP=1 \
	-c 'CREATE TABLE IF NOT EXISTS gql_delegation_replay (issuer text NOT NULL, audience text NOT NULL, token_id text NOT NULL, expires_at timestamptz NOT NULL, PRIMARY KEY (issuer, audience, token_id)); CREATE INDEX IF NOT EXISTS gql_delegation_replay_expiry ON gql_delegation_replay (expires_at); CREATE TABLE IF NOT EXISTS gql_security_state (singleton boolean PRIMARY KEY DEFAULT true CHECK (singleton), revision bigint NOT NULL, tenants jsonb NOT NULL); TRUNCATE gql_delegation_replay, gql_security_state; INSERT INTO gql_security_state (singleton, revision, tenants) VALUES (true, 0, jsonb_build_object());' >/dev/null
GQL_RUN_CLUSTER_GA=1 GQL_GA_POSTGRES_DSN='host=localhost port=5433 user=demo password=demo dbname=demo sslmode=disable' \
	go test .. -run '^TestGACluster' -count=1

# 真实 HTTP RemoteEndpoint -> Gateway 子进程门禁：manifest、delegated
# effective schema、执行、health 与运行中撤权都必须走真实 HTTP。
GQL_RUN_CLUSTER_GA=1 go test ../transport -run '^TestGAHTTPRemoteGatewayRoundTripAndStateChange$' -count=1

DEMO_HTTP_ADDR=:18080 go run . >/tmp/ideabase-gql-demo.log 2>&1 &
SERVER_PID=$!
for _ in $(seq 1 30); do
	if response=$(curl -fsS http://localhost:18080/graphql \
		-H 'Content-Type: application/json' \
		-H 'X-Demo-Tenant: 1' \
		-d '{"operationName":"CustomRootQuery"}' 2>/dev/null); then
		case "$response" in
			*'gql-complete-demo'*) break ;;
		esac
	fi
	sleep 1
done
case "${response:-}" in
	*'gql-complete-demo'*) ;;
	*) echo "Demo HTTP启动访问失败" >&2; cat /tmp/ideabase-gql-demo.log >&2; exit 1 ;;
esac

echo "Demo E2E passed: PostgreSQL + all persisted operations + HTTP startup access"
