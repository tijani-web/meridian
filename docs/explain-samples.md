# Meridian Explainability & CLI Diagnostic Outputs

This document captures sample diagnostic outputs produced by `meridianctl explain` and `meridianctl status cluster` from the Meridian explainability engine (`internal/explain`).

All scenarios reflect real cluster states, queue admissions, scheduler filter rejections, permit reservations, and preemption/recovery histories as specified in `Meridian.md` §6.8.

---

## 1. Stuck-Workload Scenario: Quota-Blocked

When a workload is admitted into a queue whose allocated quota has reached its ceiling or guarantee, the workload is held at the `meridian.dev/admission` gate. `meridianctl explain` highlights what is blocking admission and precisely which workloads are holding the quota.

```bash
$ meridianctl explain workload training-run-1 -n research
```

```text
=== Workload: training-run-1 (namespace: research) ===
Phase: Pending  (duration: 2m0s)

1. Queue & Quota:
   Queue:      team-nlp
   Position:   4 workloads ahead
   Quota:      32 allocated / 32 guaranteed (ceiling 32)
   BLOCKED:    queue quota ceiling reached (32/32 GPUs)
   Holding Quota:
     - active-job-a
     - active-job-b

2. Scheduling Gate:
   Gate:   meridian.dev/admission
   Status: OPEN=false (Workload waiting for queue quota admission before scheduling)

3. Scheduler Filter & Score:
   (not in scheduling phase)

4. Gang Permit Status:
   Placement: 0/0 placed

5. Preemption & Recovery History:
   (no preemption or recovery events)
```

---

## 2. Stuck-Workload Scenario: Insufficient GPUs

When a workload passes queue admission and the scheduling gate is released, the scheduler filters cluster nodes. If nodes do not have enough unallocated or allocatable GPUs, `meridianctl explain` aggregates rejections by reason across the cluster.

```bash
$ meridianctl explain workload large-train-2 -n ml
```

```text
=== Workload: large-train-2 (namespace: ml) ===
Phase: Scheduling  (duration: 45s)

1. Queue & Quota:
   Queue:      team-vision
   Quota:      16 allocated / 32 guaranteed (ceiling 64)
   Admitted:   Quota available

2. Scheduling Gate:
   Status: All gates released (eligible for scheduling)

3. Scheduler Filter & Score:
   Rejections:
     32 nodes: insufficient nvidia.com/gpu

4. Gang Permit Status:
   Placement: 0/0 placed

5. Preemption & Recovery History:
   (no preemption or recovery events)
```

---

## 3. Stuck-Workload Scenario: Topology Constraint Unmet

When adequate GPU count exists across the cluster, but no single node or NVLink domain satisfies the workload's topology constraint (e.g. `SameNVLinkDomain`), the explainer identifies the exact topology rejection reason and ranks candidate nodes by composite score.

```bash
$ meridianctl explain workload distributed-gpt -n research
```

```text
=== Workload: distributed-gpt (namespace: research) ===
Phase: Scheduling  (duration: 1m20s)

1. Queue & Quota:
   Queue:      team-nlp
   Quota:      8 allocated / 32 guaranteed (ceiling 32)
   Admitted:   Quota available

2. Scheduling Gate:
   Status: All gates released (eligible for scheduling)

3. Scheduler Filter & Score:
   Rejections:
     16 nodes: topology SameNVLinkDomain unmet
     8 nodes: insufficient nvidia.com/gpu
   Top Candidates:
     worker-h100-0: composite=76 (fit=100, topo=40, health=100)

4. Gang Permit Status:
   Placement: 0/0 placed

5. Preemption & Recovery History:
   (no preemption or recovery events)
```

---

## 4. Stuck-Workload Scenario: Permit-Waiting (Gang Scheduling)

In gang scheduling, pods wait in the Permit plugin phase until all `minAvailable` members of the gang are reserved. If only a subset are reserved, `meridianctl explain` reports the progress and remaining timeout before eviction.

```bash
$ meridianctl explain workload gang-job-4 -n research
```

```text
=== Workload: gang-job-4 (namespace: research) ===
Phase: Scheduling  (duration: 15s)

1. Queue & Quota:
   Queue:      team-nlp
   Quota:      0 allocated / 0 guaranteed (ceiling 0)
   Admitted:   Quota available

2. Scheduling Gate:
   Status: All gates released (eligible for scheduling)

3. Scheduler Filter & Score:
   (not in scheduling phase)

4. Gang Permit Status:
   Waiting at Permit: 6/8 reserved
   Timeout Remaining: 45s

5. Preemption & Recovery History:
   (no preemption or recovery events)
```

---

## 5. Stuck-Workload Scenario: Preempted with Cooperative Checkpoint

When a lower-priority or borrowing workload is selected as a victim for reclaim preemption, the controller requests a cooperative checkpoint. The explainer prints the full sequence of events, why it was preempted, checkpoint ack duration, and pod termination status.

```bash
$ meridianctl explain workload preempted-worker-5 -n batch
```

```text
=== Workload: preempted-worker-5 (namespace: batch) ===
Phase: Preempting  (duration: 25s)

1. Queue & Quota:
   Queue:      batch-low-pri
   Quota:      0 allocated / 0 guaranteed (ceiling 0)
   Admitted:   Quota available

2. Scheduling Gate:
   Status: All gates released (eligible for scheduling)

3. Scheduler Filter & Score:
   (not in scheduling phase)

4. Gang Permit Status:
   Placement: 0/0 placed

5. Preemption & Recovery History:
   [seq=1] 13:05:22  (prev) -> Running  AdmittedAndBound: All pods running on worker-gpu-1
   [seq=2] 13:07:42  (prev) -> Checkpointing  CooperativePreemption: Victim chosen for higher priority workload team-nlp/distributed-gpt; checkpoint requested
   [seq=3] 13:07:57  (prev) -> Preempting  PreemptionEviction: Checkpoint acked in 14.8s; terminating gang pods
```

---

## 6. Cluster Status Overview

```bash
$ meridianctl status cluster
```

```text
=== Meridian Cluster Status ===
GPUs: 48/64 allocated (75.0% utilized, 16 available)
Nodes: 8 healthy, 1 unhealthy
Fragmentation Ratio: 0.12

Queues:
  - team-alpha (priority 10): 24/32 guaranteed (ceiling 48), 3 pending
  - team-beta  (priority 5):  24/16 guaranteed (ceiling 32), 1 pending
```

---

## 7. Inference Deployment Diagnosis

```bash
$ meridianctl explain inference llama-serve -n production
```

```text
=== InferenceDeployment: llama-serve (namespace: production) ===
Model: llama-3-8b-instruct
Replicas: 3/4 healthy
Warm-Up Gating: Passed
Canary Rollout: ACTIVE (weight=20%)

Revisions:
  - llama-serve-stable: weight=80% (4/4 ready)
  - llama-serve-canary: weight=20% (1/1 ready)

Routing Endpoints:
  - http://llama-serve.production.svc.cluster.local:8000/v1/chat/completions
```
