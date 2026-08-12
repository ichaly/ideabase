#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 1 ]]; then
  echo "usage: $0 BASE_REF" >&2
  exit 2
fi

repo=$(git rev-parse --show-toplevel)
scratch=$(mktemp -d)
base_dir="$scratch/base"
cleanup() {
  git -C "$repo" worktree remove --force "$base_dir" >/dev/null 2>&1 || true
  rmdir "$scratch" >/dev/null 2>&1 || true
}
trap cleanup EXIT

git -C "$repo" worktree add --detach "$base_dir" "$1" >/dev/null

benchmark() {
  local source=$1 output=$2
  (
    cd "$source/gql"
    export GOMAXPROCS=1 GOWORK=off
    go mod download all
    go test . -run '^$' -bench '^(BenchmarkReplyBytes|BenchmarkHosts|BenchmarkGatewayWarmQuery|BenchmarkObserverDisabled)$' -benchmem -benchtime=1s -count=10 -cpu=1
    go test ./compiler/pgsql -run '^$' -bench '^BenchmarkCompile(Simple|Nested)$' -benchmem -benchtime=1s -count=10 -cpu=1
  ) >"$output"
}

benchmark "$base_dir" "$scratch/base.txt"
benchmark "$repo" "$scratch/head.txt"
cd "$repo/gql"
go run ./cmd/perfgate -budget 0.10 "$scratch/base.txt" "$scratch/head.txt"
go run golang.org/x/perf/cmd/benchstat@v0.0.0-20260709024250-82a0b07e230d "$scratch/base.txt" "$scratch/head.txt"
