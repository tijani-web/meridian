package workload

const (
	// WorkloadFinalizer guarantees that gang pods and reserved GPU quota are cleanly torn down
	// before the API object is completely expunged from etcd.
	WorkloadFinalizer = "meridian.dev/workload-finalizer"

	// AdmissionSchedulingGate is the Kubernetes scheduling gate attached to every gang member pod
	// upon creation. Pods cannot be bound by the scheduler until queue quota admission succeeds.
	AdmissionSchedulingGate = "meridian.dev/admission-gate"

	// LabelWorkload stores the controlling GPUWorkload name on every gang member pod.
	LabelWorkload = "meridian.dev/workload"

	// LabelGangSize stores the gang size string on each gang member pod.
	LabelGangSize = "meridian.dev/gang-size"

	// LabelGeneration tracks the workload recovery generation to differentiate pods across restarts.
	LabelGeneration = "meridian.dev/generation"

	// LabelGangIndex identifies the individual pod's ordinal index within the gang [0, Size-1].
	LabelGangIndex = "meridian.dev/gang-index"

	// LabelQueue references the parent GPUQueue.
	LabelQueue = "meridian.dev/queue"

	// FieldManagerName is the SSA field manager identifier for Meridian workload controller status updates.
	FieldManagerName = "meridian-workload-controller"
)
