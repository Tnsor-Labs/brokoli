#!/usr/bin/env bash
set -euo pipefail

: "${BROKOLI_BENCH_FLIGHTSQL_URI:?set BROKOLI_BENCH_FLIGHTSQL_URI}"
: "${BROKOLI_BENCH_FLIGHTSQL_QUERY:?set BROKOLI_BENCH_FLIGHTSQL_QUERY}"
: "${BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS:?set BROKOLI_BENCH_FLIGHTSQL_EXPECTED_ROWS}"
: "${BROKOLI_BENCH_NATIVE_FLIGHTSQL_LIBRARY:?set BROKOLI_BENCH_NATIVE_FLIGHTSQL_LIBRARY}"
: "${GOMAXPROCS:?set a fixed GOMAXPROCS}"

out=${1:?usage: scripts/bench-flightsql-receive-alternating.sh OUTPUT_DIRECTORY}
runs=${BROKOLI_BENCH_RUNS:-10}
mkdir -p "$out"
: >"$out/pure-go.txt"
: >"$out/driver-manager.txt"

for _ in $(seq 1 "$runs"); do
  go test -tags adbc ./engine -run '^$' \
    -bench '^BenchmarkFlightSQLReceiveOnly$' \
    -benchmem -benchtime=1x >>"$out/pure-go.txt"
  go test -tags adbc ./engine -run '^$' \
    -bench '^BenchmarkNativeFlightSQLReceiveOnly$' \
    -benchmem -benchtime=1x >>"$out/driver-manager.txt"
done

# Keep the original output for auditability, but give benchstat the same
# benchmark name so it can calculate a paired driver ratio and significance.
sed 's/BenchmarkNativeFlightSQLReceiveOnly/BenchmarkFlightSQLReceiveOnly/' \
  "$out/driver-manager.txt" >"$out/driver-manager-comparable.txt"
benchstat "$out/pure-go.txt" "$out/driver-manager-comparable.txt"
