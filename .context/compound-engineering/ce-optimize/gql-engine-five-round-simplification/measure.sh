#!/usr/bin/env bash
set -uo pipefail

readonly api_hash='ce38e40a3bd40a2998098b5eb87660301ae93b057aeb04231decb5cfbce62718'
readonly tmp_dir="$(mktemp -d "${TMPDIR:-/tmp}/gql-optimize.XXXXXX")"
trap 'rm -rf -- "$tmp_dir"' EXIT

check() {
  local name="$1"
  shift
  if "$@" >"$tmp_dir/$name.log" 2>&1; then
    printf '1'
  else
    printf '%s failed:\n' "$name" >&2
    sed -n '1,200p' "$tmp_dir/$name.log" >&2
    printf '0'
  fi
}

benchmark_value() {
  local file="$1" benchmark="$2" unit="$3"
  awk -v benchmark="$benchmark" -v unit="$unit" '
    $1 ~ ("^" benchmark "-[0-9]+$") {
      for (i = 2; i <= NF; i++) {
        if ($(i + 1) == unit) {
          value = $i
        }
      }
    }
    END { print value + 0 }
  ' "$file"
}

production_go_lines="$({ find gql -type f -name '*.go' ! -name '*_test.go' -print0 | xargs -0 wc -l; } | awk 'END { print $1 + 0 }')"
production_go_files="$(find gql -type f -name '*.go' ! -name '*_test.go' | wc -l | tr -d ' ')"
production_functions="$(rg --glob '!**/*_test.go' '^func ' gql | wc -l | tr -d ' ')"
production_branch_tokens="$(rg --glob '!**/*_test.go' -o '\b(if|for|switch|select|case)\b' gql | wc -l | tr -d ' ')"
test_go_lines="$({ find gql -type f -name '*_test.go' -print0 | xargs -0 wc -l; } | awk 'END { print $1 + 0 }')"

unit_tests_passed="$(check unit_tests go test ./gql/...)"
race_tests_passed="$(check race_tests go test -race ./gql/...)"
vet_passed="$(check vet go vet ./gql/...)"

staticcheck_bin="$(command -v staticcheck || true)"
if [[ -z "$staticcheck_bin" && -x /Users/Chaly/Documents/Work/go/bin/staticcheck ]]; then
  staticcheck_bin=/Users/Chaly/Documents/Work/go/bin/staticcheck
fi
if [[ -n "$staticcheck_bin" ]]; then
  GOTOOLCHAIN=go1.25.7 "$staticcheck_bin" -tests=false ./gql/... >"$tmp_dir/staticcheck.log" 2>&1 || true
  staticcheck_findings="$(wc -l <"$tmp_dir/staticcheck.log" | tr -d ' ')"
else
  printf 'staticcheck not found\n' >&2
  staticcheck_findings=999999
fi

current_api_hash="$({ for package in $(go list ./gql/...); do go doc -all "$package"; done; } | shasum -a 256 | awk '{ print $1 }')"
api_compatibility_passed=0
if [[ "$current_api_hash" == "$api_hash" ]]; then
  api_compatibility_passed=1
fi

go test ./gql/compiler/pgsql -run '^$' \
  -bench '^BenchmarkCompatCompileBaseline(Simple|Nested)$' -benchmem -count=1 \
  >"$tmp_dir/compiler-bench.log" 2>&1
go test ./gql -run '^$' \
  -bench '^(BenchmarkGatewayWarmQuery|BenchmarkObserverDisabled)$' -benchmem -count=1 \
  >"$tmp_dir/gateway-bench.log" 2>&1

simple_compile_ns="$(benchmark_value "$tmp_dir/compiler-bench.log" BenchmarkCompatCompileBaselineSimple ns/op)"
simple_compile_allocs="$(benchmark_value "$tmp_dir/compiler-bench.log" BenchmarkCompatCompileBaselineSimple allocs/op)"
nested_compile_ns="$(benchmark_value "$tmp_dir/compiler-bench.log" BenchmarkCompatCompileBaselineNested ns/op)"
nested_compile_allocs="$(benchmark_value "$tmp_dir/compiler-bench.log" BenchmarkCompatCompileBaselineNested allocs/op)"
gateway_warm_ns="$(benchmark_value "$tmp_dir/gateway-bench.log" BenchmarkGatewayWarmQuery ns/op)"
gateway_warm_allocs="$(benchmark_value "$tmp_dir/gateway-bench.log" BenchmarkGatewayWarmQuery allocs/op)"
observer_disabled_ns="$(benchmark_value "$tmp_dir/gateway-bench.log" BenchmarkObserverDisabled ns/op)"
observer_disabled_allocs="$(benchmark_value "$tmp_dir/gateway-bench.log" BenchmarkObserverDisabled allocs/op)"

printf '{\n'
printf '  "production_go_lines": %s,\n' "$production_go_lines"
printf '  "unit_tests_passed": %s,\n' "$unit_tests_passed"
printf '  "race_tests_passed": %s,\n' "$race_tests_passed"
printf '  "vet_passed": %s,\n' "$vet_passed"
printf '  "staticcheck_findings": %s,\n' "$staticcheck_findings"
printf '  "api_compatibility_passed": %s,\n' "$api_compatibility_passed"
printf '  "simple_compile_allocs": %s,\n' "$simple_compile_allocs"
printf '  "nested_compile_allocs": %s,\n' "$nested_compile_allocs"
printf '  "gateway_warm_allocs": %s,\n' "$gateway_warm_allocs"
printf '  "observer_disabled_allocs": %s,\n' "$observer_disabled_allocs"
printf '  "production_go_files": %s,\n' "$production_go_files"
printf '  "production_functions": %s,\n' "$production_functions"
printf '  "production_branch_tokens": %s,\n' "$production_branch_tokens"
printf '  "test_go_lines": %s,\n' "$test_go_lines"
printf '  "simple_compile_ns": %s,\n' "$simple_compile_ns"
printf '  "nested_compile_ns": %s,\n' "$nested_compile_ns"
printf '  "gateway_warm_ns": %s,\n' "$gateway_warm_ns"
printf '  "observer_disabled_ns": %s\n' "$observer_disabled_ns"
printf '}\n'
