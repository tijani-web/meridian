/*
Package chaos provides chaos engineering tests for Meridian's HA properties.

Every test in this package targets a specific failure mode from Meridian.md §7
("Chaos" layer). Tests run against a real kind cluster provisioned by
`make kind-up` and require a Meridian control plane deployed in the
`meridian-system` namespace.

Test categories:
  - Leader election failover: kill manager/scheduler pod mid-reconcile, verify
    the standby takes over within the lease window and reconciliation continues.
  - Node GPU fault: inject a fatal XID event and verify the health controller
    taints the node and reschedules affected workloads.
  - Permit timeout under churn: submit a large gang while competing smaller
    workloads consume capacity, verify no partial gang is ever bound.
  - API server latency injection: add iptables delay to etcd, verify controllers
    back off and do not hot-loop.

Run: KUBECONFIG=<path> go test -v -timeout 15m ./test/chaos/...
*/
package chaos
