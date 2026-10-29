#!/usr/bin/env bash
# run-benchmarks.sh executes the Phase 08 scale benchmarks.
# All numbers produced are committed into bench/results/.
set -euo pipefail

echo "=== Building benchmark suite ==="
go build -o bin/run-benchmarks ./bench/cmd/run-benchmarks

echo "=== Running benchmarks ==="
./bin/run-benchmarks
