# docs/INTERVIEW-NOTES.md — Design Decisions and Real Bugs

This document tells the story behind each major design decision and each real bug
encountered during Meridian's development. It is derived from the ADRs, the
`docs/debugging-log.md`, and the Phase postmortem.

Intended for: technical interview preparation; onboarding new contributors.

---

## Major Design Decisions

### 1. SchedulingGate-based admission (ADR-0001)

**The question**: How does the queue controller hold workload pods back from
scheduling until the queue has capacity to admit them?

**Options considered**:
- Label watch + node selector: flaky, races with pod re-creation.
- Priority Class anti-affinity: too broad, affects unrelated pods.
- **SchedulingGates** (chosen): a list of strings on the PodSpec that the apiserver
  enforces. The scheduler ignores pods with a non-empty gate list. The queue controller
  removes the gate string when admitting. Zero races — gate removal is atomic via
  server-side apply.

**Why it matters**: Without admission gating, pods would enter the scheduler queue
before the workload quota system had approved them. They'd consume scheduling cycles
and potentially be placed on nodes before fair-share accounting ran. SchedulingGates
give the queue controller a race-free `admit` signal.

**Interview talking point**: SchedulingGates were GA in Kubernetes 1.30. The first
kind cluster attempt failed because we passed `featureGates.SchedulingGates: true`
to the kind config, which was removed in 1.30+. See Bug #1 below.

---

### 2. Second scheduler (ADR-0002)

**The question**: Should we extend the default kube-scheduler (single scheduler) or
run a second scheduler binary with the out-of-tree plugin framework?

**Options considered**:
- Webhook-only: mutating webhook rewrites scheduling constraints. Cannot implement
  gang-permit synchronization.
- Single scheduler with in-tree patches: requires rebuilding upstream scheduler;
  breaks on every Kubernetes version.
- **Second scheduler with scheduler-framework plugins** (chosen): the Kubernetes
  scheduler framework (`sigs.k8s.io/scheduler-plugins`) provides stable extension
  points (Filter, Score, Permit, Reserve, PreBind, Bind). Meridian registers at Permit
  to synchronize gang members and at Reserve to deduct GPU quota atomically.

**Why it matters**: Gang scheduling requires global coordination between all pods in a
group. Only the Permit plugin's `WaitOnPermit` mechanism provides a synchronization
point where all members can rendezvous before any of them proceeds to Bind. No other
extension point can implement this without re-entrant scheduler state.

**Interview talking point**: The Permit plugin exposed a TOCTOU race (see Bug #3 below)
that was only catchable by running `go test -race`. This was a real production-critical
bug caught in development.

---

### 3. Workload-layer preemption (ADR-0003)

**The question**: Should preemption operate at the Kubernetes pod level (via priority
classes and pod evictions) or at the GPUWorkload layer?

**Options**: Pod-level preemption via PriorityClass and preemption hooks is built into
the scheduler. However:
- Pod-level preemption evicts individual pods, not entire gangs. This leaves partial
  gangs alive and wastes GPU-seconds.
- The checkpoint protocol requires notifying the trainer process before eviction, which
  is not possible at the pod-level preemption hook.

**Decision**: Workload-layer preemption. The controller sends a checkpoint request
annotation to the workload, waits for the trainer to acknowledge (`checkpoint-ack`
annotation), then deletes the gang pods in order. This preserves the gang atomicity
invariant and enables resume-from-checkpoint.

**Interview talking point**: The checkpoint-preemption e2e test (`test/e2e/checkpoint_preemption_test.go`)
verifies the full path: preempt → checkpoint ack → evict → resume with step continuity.
The `vllm-emu` trainer is a real HTTP server that implements the annotation protocol.

---

### 4. Extended resources for GPU modeling (ADR-0004)

**The question**: How to model GPU availability on nodes so the scheduler can place
gang pods onto nodes that actually have enough GPUs?

**Options**:
- Device plugin + real resource: requires a real NVML device plugin running on each
  node. Fine for production; not testable on kind without GPU hardware.
- **Extended resources + fake allocator** (chosen for CI): nodes are labeled with
  `meridian.dev/gpu-type=nvidia-h100` and annotated with capacity. The Meridian scheduler
  plugin uses these labels as placement constraints. In CI (kind), KWOK node objects
  carry the labels. On real GPU nodes, an actual device plugin (not part of this repo)
  would allocate `nvidia.com/gpu` resources.

**Why it matters**: This design allows the full scheduling path to be exercised in CI
without real GPU hardware. The limitation is documented in `docs/LIMITATIONS.md`.

---

### 5. Permit snapshot-at-fire-time gang abort (ADR-0005)

**The question**: How to ensure gang abort is atomic — all members rejected, no
stragglers proceeding to Bind?

See the postmortem for the full story. The key design decision: the abort timer goroutine
must take its snapshot of `waitingPods[gangID]` and delete the key atomically **inside
the mutex lock at fire time**, not at timer creation time. Combined with a guard in
`RunPermit` that checks for the deleted key before appending a new member, this ensures
no pod can join a gang that is in the process of being aborted.

**Property test**: `TestPermitTimerRaceNoPartialGang` runs 100 concurrent goroutines all
racing to join a gang whose timer fires at random intervals. In 10,000 iterations,
`meridian_gang_partial_starts_total` was always 0.

---

## Real Bugs

### Bug #1: kind cluster startup failure (Phase 00)

**GitHub issue**: [#1](https://github.com/tijani-web/meridian/issues/1)

**Symptom**: `kind create cluster` succeeded at the Docker level but the kube-apiserver
crashed immediately during bootstrap. Connection refused on `https://172.20.0.2:6443`.

**Hypothesis 1**: Docker networking. *Disproved* — Docker was healthy.
**Hypothesis 2**: Bad node image. *Disproved* — node image pulled, container started.
**Real cause**: The kind config included `featureGates.SchedulingGates: true`. This
feature gate was removed in Kubernetes 1.30 (GA). Passing a removed flag to
`kube-apiserver` causes it to exit during flag validation, before accepting connections.

**Fix**: Removed the `featureGates` block entirely from `deploy/kind/config.yaml`.

**Lesson**: Always verify feature gate names against the target Kubernetes version before
committing cluster configs. GA features move from featureGates to unconditionally on.

---

### Bug #2 (near-miss): Gang partial-start TOCTOU race (Phase 05)

**GitHub issue**: No public issue was filed (caught before commit).

**Full story**: See [`docs/postmortems/2026-10-28-gang-permit-partial-start-near-miss.md`](postmortems/2026-10-28-gang-permit-partial-start-near-miss.md)

**Summary**: The Permit plugin's abort timer captured a snapshot of `waitingPods[gangID]`
at timer-creation time via a Go closure. A pod arriving in the window between timer
creation and timer fire was not in the snapshot, so its rejection was missed. That pod
proceeded to Bind while the rest of the gang was unreserved — 1/8 pods Running, 7/8
Unschedulable.

**Detection**: The envtest crash-restart test, which exercised concurrent reconciliation
at the same instant the Permit timer fired. `go test -race` would also have caught it.

**Fix**: Snapshot-at-fire-time with atomic key deletion (ADR-0005).

**Why it matters in interviews**: This is the exact invariant Meridian is built around.
Finding and fixing it — and verifying with a property test and chaos test — demonstrates
the difference between "it works in happy path tests" and "it's correct under
concurrency."

---

### Bug #3: envtest webhook defaulting for Port=0 and MaxQueueDepth=0 (Phase 11)

**Symptom**: CI integration tests (envtest) were failing with:
`admission webhook rejected: spec.port must be > 0`

**Root cause**: The webhook defaulting function in `inferencedeployment_webhook.go`
was checking `if spec.Port == 0` and replacing it with a default. In envtest, the
admission webhook runs against a real apiserver, and the empty port was being treated
as "not set" — but the validator then rejected it because `0` doesn't pass the
`minimum=1` kubebuilder marker.

**Fix**: Moved the `Port` default into `SetDefaults` so the field is populated before
validation runs, and changed the validation to check `if r.Spec.Port == 0` only in
`Default()`, not in `ValidateCreate`.

---

## What I Would Do Differently

1. **Real GPU node from day one** (even a single g4dn.xlarge spot instance). The
   extended-resource simulation is a good pragmatic choice, but the NVML code path
   is entirely untested on real hardware.

2. **Chaos tests earlier** (Phase 03 or 04, not Phase 12). The gang atomicity near-miss
   happened in Phase 05. Having the chaos test in place earlier would have caught it
   at the commit level.

3. **Helm chart alongside the first deployment** (Phase 00 or 01). We added it in
   Phase 12. Having it earlier would have forced us to think about RBAC least-privilege
   and PDB placement from the start.
