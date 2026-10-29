# bench/scripts/run-router-benchmarks.ps1 — Meridian Phase 10 inference router benchmark execution
$ErrorActionPreference = "Stop"

Write-Host "=== Building and Running Meridian Phase 10 Inference Router Benchmarks ===" -ForegroundColor Cyan

if (-not (Test-Path "bin")) {
    New-Item -ItemType Directory -Path "bin" | Out-Null
}

go build -o bin/run-router-benchmarks.exe ./bench/cmd/run-router-benchmarks

& .\bin\run-router-benchmarks.exe
