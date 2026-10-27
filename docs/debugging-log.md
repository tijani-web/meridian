# docs/debugging-log.md — Real Bugs and How They Were Found

This file records real bugs encountered during development.
Each entry: symptom, hypotheses tested, what disproved them, real cause.
This file doubles as interview material (§2.4).

> **Rule**: Only real bugs. No invented entries. No padding.
> Every entry must reference a GitHub issue number.

## Bug #1: Kind control plane startup failure due to obsolete SchedulingGates feature gate

- **Issue**: [#1](https://github.com/tijani-web/meridian/issues/1)
- **Phase**: 00 (Bootstrap)
- **Symptom**: `kind create cluster` failed during the wait-control-plane phase with `Post "https://172.20.0.2:6443/apis/rbac.authorization.k8s.io/v1/clusterrolebindings?timeout=10s": dial tcp 172.20.0.2:6443: connect: connection refused`.
- **Hypotheses tested**:
  1. Docker networking was blocking kind internal container communication.
     - *Disproved*: Docker daemon was operating normally and container was reachable.
  2. The custom image tag `kindest/node:v1.33.1` was unavailable or hanging.
     - *Disproved*: Node image pulled, but kube-apiserver crashed shortly after start.
  3. Control plane component flags in `config.yaml` contained invalid configuration.
     - *Confirmed*: `featureGates.SchedulingGates: true` was obsolete because `SchedulingGates` is GA in Kubernetes 1.30+ and removed from feature-gates in recent releases. Additionally, `nodeRegistration.kubeletExtraArgs` in the control plane InitConfiguration patch was deprecated and redundant.
- **Root cause**: Passing removed feature gates to modern Kubernetes control planes causes `kube-apiserver` to fail flag validation and exit immediately, causing connection refused during cluster bootstrap.
- **Fix**: Removed the obsolete `featureGates` block and redundant `kubeadmConfigPatches` from `deploy/kind/config.yaml`, allowing kind to boot standard control planes. Cluster verified healthy with all 5 nodes `Ready`.

