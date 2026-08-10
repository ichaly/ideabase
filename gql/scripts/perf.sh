#!/usr/bin/env bash
set -euo pipefail

cd "$(dirname "$0")/.."
export GOMAXPROCS=1

go test . -run '^TestGAAllocationBudgets$' -count=1
go test ./compiler/pgsql -run '^TestGACompilerAllocationBudgets$' -count=1
go test . -run '^$' -bench '^(BenchmarkReplyBytes|BenchmarkHosts|BenchmarkGatewayWarmQuery|BenchmarkObserverDisabled)$' -benchmem -benchtime=1s -count=10 -cpu=1
go test ./compiler/pgsql -run '^$' -bench '^BenchmarkCompile(Simple|Nested)$' -benchmem -benchtime=1s -count=10 -cpu=1
