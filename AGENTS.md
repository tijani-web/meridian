# Meridian — GPU Fleet Control Plane

> **Agent rules extracted from `Meridian.md` §1–§2. Every agent session reads this file first.**

## §1 STRICT AGENT RULES (NON-NEGOTIABLE)

### 1.1 No mocks, no stubs, no fakes in product code
- **Forbidden in product code** (`cmd/`, `pkg/`, `internal/`, `api/`, `controllers/`): mock structs, fake clients, stub functions returning canned data, `panic("not implemented")`, `// TODO: implement`, empty function bodies, hard-coded sample results, placeholder metrics.
- **Forbidden in tests**: `fake.NewClientBuilder()` / `client-go/fake` for controllers. Controllers tested via **envtest**; e2e on **real kind cluster**.
- **Allowed test infrastructure** (under `tools/` or `test/`): KWOK nodes, `tools/vllm-emu`, `tools/gpu-fault-injector`, `tools/loadgen`. All must speak the real protocol and be labeled "simulated".
- Every simulated result must be labeled "simulated" in docs and graphs.

### 1.2 No fabricated claims
- Every number in README/docs comes from a committed benchmark run under `bench/results/`.
- Never claim availability, speedup, or quality without a measurement. If not measured, not claimed.
- `docs/LIMITATIONS.md` is mandatory and honest.

### 1.3 Autonomy
- Work through phases without stopping for approval.
- Stop only if: missing `gh` auth, no Docker/kind, or a contradictory requirement. State blocker and what was tried.
- All design forks get an ADR in `docs/adr/`.
- Before context fills: update `STATE.md`, push.

### 1.4 Quality bar
- Every exported type/function has a doc comment explaining *why*, not just *what*.
- Errors: `fmt.Errorf("...: %w", err)`. Never swallow. Never `_ =` without a justifying comment.
- Controllers: idempotent and crash-safe at every line.
- No global mutable state. Contexts propagated. All goroutines have a documented owner and shutdown path.
- Lint clean (`golangci-lint`), `go vet` clean, `go test -race` clean.

## §2 GIT & GITHUB PROTOCOL

- **Push after every commit.** Never leave unpushed work.
- Branch per phase: `phase/NN-short-name`. PR to `main` at start (draft), merge when DoD passes.
- No squash merges — use merge commit or rebase-merge.
- Commit format: Conventional Commits (`feat(scope):`, `fix(scope):`, `test(scope):`, `docs(scope):`, `chore(scope):`).
- Every phase: PR merged + `PROGRESS.md` updated + tag `phase-NN-done` pushed.
- Tracking files (always current): `PROGRESS.md`, `STATE.md`, `docs/VERSIONS.md`.
- **Secrets**: never commit credentials, kubeconfigs, tokens. `.gitignore` covers them.
- Images publish to `ghcr.io/tijani-web/meridian-*` from CI only.
- Bug/error protocol: `gh issue create` → failing test commit → fix commit → `docs/debugging-log.md` entry.
