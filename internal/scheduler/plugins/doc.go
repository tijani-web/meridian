/*
Package plugins implements the Meridian out-of-tree Kubernetes scheduler plugins.

All plugins in this package integrate with the Kubernetes scheduler framework via
the framework.Plugin interface hierarchy. No mock clients or canned results are used;
every scheduling decision is derived from real node labels, live GPU capacity, and
the Meridian admission state published in pod annotations and labels.

Plugin registration:
  - QueueSort   – priority + FIFO within priority for GPU workloads
  - PreFilter   – validate gang metadata and cluster-wide GPU feasibility
  - Filter      – GPU type/count fit, health taints, topology required constraints
  - Score       – composite anti-fragmentation + topology affinity + health
  - NormalizeScore – linear normalize into [0, framework.MaxNodeScore]
  - Reserve     – per-gang tentative GPU assignment tracking
  - Unreserve   – undo tentative assignment on failure
  - Permit      – gang scheduling: hold until minAvailable members reserved, timeout = reject all
*/
package plugins
