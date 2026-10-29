#!/usr/bin/env bash
# bench/scripts/run-router-benchmarks.sh — Meridian Phase 10 inference router benchmark execution
set -euo pipefail

echo "=== Building and Running Meridian Phase 10 Inference Router Benchmarks ==="

mkdir -p bin
go build -o bin/run-router-benchmarks.exe ./bench/cmd/run-router-benchmarks

./bin/run-router-benchmarks.exe
