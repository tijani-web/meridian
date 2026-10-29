# docs/VERSIONS.md — Pinned Versions

All versions pinned for reproducibility. Latest stable verified on **2026-10-27**.
Check official release pages; do not rely on memory.

---

| Component | Version | Source / Release Page | Date Verified |
|---|---|---|---|
| Go | 1.27.1 | https://go.dev/dl/ | 2026-10-27 |
| Kubernetes (kind node image) | v1.33.1 | https://github.com/kubernetes-sigs/kind/releases | 2026-10-27 |
| controller-runtime | v0.21.0 | https://github.com/kubernetes-sigs/controller-runtime/releases | 2026-10-27 |
| kubebuilder (CLI, for codegen reference) | v4.7.0 | https://github.com/kubernetes-sigs/kubebuilder/releases | 2026-10-27 |
| kind | v0.29.0 | https://github.com/kubernetes-sigs/kind/releases | 2026-10-27 |
| KWOK | v0.8.0 | https://github.com/kubernetes-sigs/kwok/releases | 2026-10-27 |
| vLLM (pinned for vllm-emu metric names) | v0.9.2 | https://github.com/vllm-project/vllm/releases | 2026-10-27 |
| Helm | v4.3.1 | https://github.com/helm/helm/releases | 2026-10-27 |
| golangci-lint | v2.3.0 | https://github.com/golangci/golangci-lint/releases | 2026-10-27 |
| cert-manager | v1.18.1 | https://github.com/cert-manager/cert-manager/releases | 2026-10-27 |
| scheduler-plugins (reference, not copied) | v0.33.0 | https://github.com/kubernetes-sigs/scheduler-plugins/releases | 2026-10-27 |
| kube-scheduler (out-of-tree, matches node image) | v1.33.1 | https://github.com/kubernetes/kubernetes/releases | 2026-10-27 |
| OpenTelemetry Go SDK | v1.37.0 | https://github.com/open-telemetry/opentelemetry-go/releases | 2026-10-27 |
| Prometheus client_golang | v1.22.0 | https://github.com/prometheus/client_golang/releases | 2026-10-27 |
| go-nvml | v0.12.4-1 | https://github.com/NVIDIA/go-nvml/releases | 2026-10-27 |
| GitHub CLI (gh) | v2.103.3 | https://github.com/cli/cli/releases | 2026-10-27 |

---

> **NOTE**: Versions must be verified against live release pages at Phase 0 start.
> If a newer stable version exists when re-reading this, update and record the date.
> The Go module `go.mod` is the authoritative pin for all Go dependency versions.

## v0.1.0 — Phase 13 (2026-10-29)

### Added
- README.md: complete with architecture diagram, quickstart, measured benchmark tables, design decision index, and runbook links
- docs/INTERVIEW-NOTES.md: story behind every ADR and every real bug from debugging-log.md
- docs/RESUME-BULLETS.md: portfolio claims with every number traceable to a committed result file
- docs/design/informers-and-indexers.md: informer cache, three custom indexers, watch predicates, and startup sync
- docs/design/workload-fsm.md: full state diagram, implementation pattern, idempotency invariants, testing strategy
- docs/design/slos.md: 4 SLO definitions, multi-window burn-rate alerting math, PrometheusRule examples
- Phase 12 Phase 13 tagged, v0.1.0 released

### Design docs complete
- docs/design/: fair-share.md, scoring.md, checkpoint-protocol.md, informers-and-indexers.md,
  workload-fsm.md, router-strategies.md, slos.md, decision-records.md, observability.md, node-health-and-xid.md
- docs/adr/: 0001-0005 all complete

### All 13 phases done
