# STATE.md — Meridian Resume State

## Current Phase: Phase 13 (Final Integration, Benchmark Re-run, README)

## Last Completed: Phase 12 — Packaging, HA, Chaos Engineering ✅

### Phase 12 Deliverables (all committed)
- `charts/meridian/` — Full Helm chart (manager 2-replica HA, scheduler, agent DaemonSet, router, cert-manager webhook, ServiceMonitors, PrometheusRules, CRDs, NOTES.txt)
- `deploy/argocd/` — ArgoCD AppProject + Application (ignoreDifferences for dynamic runtime fields)
- `terraform/gpu-demo/` — AWS Deep Learning AMI + k3s + NVIDIA Container Toolkit provisioning
- `docs/runbooks/` — 7 operational runbooks (all 7 failure modes from Meridian.md §11)
- `test/chaos/` — Leader election failover test + gang atomicity under churn test
- `docs/postmortems/2026-10-28-gang-permit-partial-start-near-miss.md` — blameless postmortem
- `docs/adr/0005-permit-atomicity-snapshot-at-fire.md` — Permit TOCTOU fix design decision

## Phase 13 TODO

- [ ] `make kind-up && helm install meridian charts/meridian -n meridian-system --create-namespace`
- [ ] Run full benchmark suite: `cd bench && ./scripts/run-benchmarks.sh` to refresh `bench/results/` with Phase 12+ data
- [ ] Update `README.md` with: architecture diagram, quick-start (kind), benchmark results table, links to all runbooks and ADRs
- [ ] Update `docs/VERSIONS.md` with Phase 12 changelog
- [ ] Tag `phase-12-done` and push
- [ ] Open Phase 13 PR

## Branch: `phase/12-packaging-ha-chaos` → (merge to main, then new branch `phase/13-final-integration`)

## Git State
- All Phase 12 files staged/committed per push-after-commit rule (see commit below)
- Next: `git add -A && git commit -m "feat(phase12): packaging HA chaos..." && git push`

## Key File Locations
- Module: `github.com/tijani-web/meridian`
- Helm chart: `charts/meridian/`
- Chaos tests: `test/chaos/`
- Runbooks: `docs/runbooks/`
- Postmortem: `docs/postmortems/2026-10-28-gang-permit-partial-start-near-miss.md`
- ADRs: `docs/adr/0001` through `0005`
- Bench results: `bench/results/2026-10-28-scale-benchmarks/`

## Invariants (never break)
- `meridian_gang_partial_starts_total` must be 0 always (PrometheusRule alert + chaos test)
- No fake/mock/stub in product code (`cmd/`, `pkg/`, `internal/`, `api/`, `controllers/`)
- Every benchmark claim backed by `bench/results/` CSV + JSON
- `docs/LIMITATIONS.md` stays honest
