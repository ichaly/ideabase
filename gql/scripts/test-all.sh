#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
COMPOSE=(docker compose -f "$ROOT/examples/docker-compose.yml")

cleanup() {
	if [[ ${GQL_TEST_KEEP_DB:-0} != 1 ]]; then
		"${COMPOSE[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
	fi
}
trap cleanup EXIT

if ! command -v docker >/dev/null 2>&1; then
	echo "test-all.sh requires Docker" >&2
	exit 1
fi

echo "Resetting PostgreSQL fixture"
"${COMPOSE[@]}" down --volumes --remove-orphans >/dev/null 2>&1 || true
"${COMPOSE[@]}" up -d --wait

echo "Running full test suite"
cd "$ROOT"
GOWORK=off go test ./... -count=1 "$@"
