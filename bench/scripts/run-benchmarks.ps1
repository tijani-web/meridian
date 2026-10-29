# run-benchmarks.ps1 executes the Phase 08 scale benchmarks.
# All numbers produced are committed into bench/results/.
$ErrorActionPreference = "Stop"

Write-Host "=== Building benchmark suite ==="
go build -o bin/run-benchmarks.exe ./bench/cmd/run-benchmarks

Write-Host "=== Running benchmarks ==="
.\bin\run-benchmarks.exe
