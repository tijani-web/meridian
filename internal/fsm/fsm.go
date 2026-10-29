package fsm

import (
	"fmt"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/metrics"
)

// MaxHistoryEntries defines the upper bound of retained transition records in status.history.
// Bounding history prevents unbounded status growth over many restarts or preemption cycles.
const MaxHistoryEntries = 50

// Event represents an external or internal stimulus triggering a transition in the workload FSM.
type Event string

const (
	// EventEnqueue is triggered when a newly created workload is validated and enqueued for quota admission.
	EventEnqueue Event = "Enqueue"

	// EventAdmit is triggered when queue admission succeeds and cluster quota is atomically reserved for the gang.
	EventAdmit Event = "Admit"

	// EventStartScheduling is triggered when gang member pods have been stamped and gated pods exist.
	EventStartScheduling Event = "StartScheduling"

	// EventAllRunning is triggered when all required gang members have been bound and are actively running.
	EventAllRunning Event = "AllRunning"

	// EventRequestCheckpoint is triggered when preemption or periodic checkpointing initiates.
	EventRequestCheckpoint Event = "RequestCheckpoint"

	// EventCheckpointAck is triggered when all active gang members acknowledge completion of their checkpoint.
	EventCheckpointAck Event = "CheckpointAck"

	// EventPreempt is triggered when capacity is reclaimed by a higher-priority workload or queue.
	EventPreempt Event = "Preempt"

	// EventEvictToQueue is triggered when preemption cleanup completes and the workload is returned to the queue.
	EventEvictToQueue Event = "EvictToQueue"

	// EventNodeFailure is triggered when an assigned node encounters fatal hardware faults or health eviction.
	EventNodeFailure Event = "NodeFailure"

	// EventRestart is triggered when recovery pod replacement completes and the workload resumes scheduling.
	EventRestart Event = "Restart"

	// EventSucceed is triggered when all pods in the gang terminate cleanly with exit code 0.
	EventSucceed Event = "Succeed"

	// EventFail is triggered when unrecoverable errors occur or maximum restart thresholds are exceeded.
	EventFail Event = "Fail"
)

// transitionKey identifies an allowed transition by the source state and the incoming event.
type transitionKey struct {
	from  meridianv1alpha1.GPUWorkloadPhase
	event Event
}

// TransitionRule specifies the destination state and standard machine-readable reason for a valid transition.
type TransitionRule struct {
	TargetPhase    meridianv1alpha1.GPUWorkloadPhase
	DefaultReason  string
	DefaultMessage string
}

// transitionTable encodes all mathematically valid lifecycle transitions for Meridian GPUWorkloads.
// Any state/event pair not present in this table is strictly invalid and will be rejected.
var transitionTable = map[transitionKey]TransitionRule{
	// Pending -> Queued
	{meridianv1alpha1.WorkloadPhasePending, EventEnqueue}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseQueued,
		DefaultReason:  "WorkloadEnqueued",
		DefaultMessage: "Workload passed admission validation and enrolled in GPU queue",
	},

	// Queued -> Admitted
	{meridianv1alpha1.WorkloadPhaseQueued, EventAdmit}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseAdmitted,
		DefaultReason:  "QuotaReserved",
		DefaultMessage: "Gang quota reserved in target GPU queue",
	},

	// Admitted -> Scheduling
	{meridianv1alpha1.WorkloadPhaseAdmitted, EventStartScheduling}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseScheduling,
		DefaultReason:  "PodsGated",
		DefaultMessage: "Gang member pods created with scheduling gates",
	},

	// Scheduling -> Running
	{meridianv1alpha1.WorkloadPhaseScheduling, EventAllRunning}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseRunning,
		DefaultReason:  "AllPodsRunning",
		DefaultMessage: "All gang member pods bound and executing on allocated GPU nodes",
	},

	// Scheduling -> Preempting (reclaimed before all pods could bind)
	{meridianv1alpha1.WorkloadPhaseScheduling, EventPreempt}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhasePreempting,
		DefaultReason:  "PreemptedWhileScheduling",
		DefaultMessage: "Quota reclaimed before gang placement was finalized",
	},

	// Running -> Checkpointing
	{meridianv1alpha1.WorkloadPhaseRunning, EventRequestCheckpoint}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseCheckpointing,
		DefaultReason:  "CheckpointRequested",
		DefaultMessage: "Checkpoint notification dispatched to gang members",
	},

	// Checkpointing -> Running (checkpoint finished cleanly, normal run continues)
	{meridianv1alpha1.WorkloadPhaseCheckpointing, EventCheckpointAck}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseRunning,
		DefaultReason:  "CheckpointAcknowledged",
		DefaultMessage: "All gang members successfully acknowledged checkpoint capture",
	},

	// Running -> Preempting (preemption eviction initiated)
	{meridianv1alpha1.WorkloadPhaseRunning, EventPreempt}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhasePreempting,
		DefaultReason:  "PreemptionInitiated",
		DefaultMessage: "Higher priority workload or queue reclaim preempted running gang",
	},

	// Checkpointing -> Preempting (checkpoint grace expired or eviction forced)
	{meridianv1alpha1.WorkloadPhaseCheckpointing, EventPreempt}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhasePreempting,
		DefaultReason:  "PreemptedAfterCheckpoint",
		DefaultMessage: "Workload evicting after checkpoint completion or timeout",
	},

	// Preempting -> Queued (reclaim done, returned to queue for next admission pass)
	{meridianv1alpha1.WorkloadPhasePreempting, EventEvictToQueue}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseQueued,
		DefaultReason:  "EvictedToQueue",
		DefaultMessage: "Victim pods drained and workload returned to queue",
	},

	// Running -> Recovering (node failure or XID error detected)
	{meridianv1alpha1.WorkloadPhaseRunning, EventNodeFailure}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseRecovering,
		DefaultReason:  "NodeHardwareFailure",
		DefaultMessage: "Hardware fault or taint detected on assigned GPU node",
	},

	// Scheduling -> Recovering (node failed while gang was scheduling)
	{meridianv1alpha1.WorkloadPhaseScheduling, EventNodeFailure}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseRecovering,
		DefaultReason:  "NodeFaultDuringScheduling",
		DefaultMessage: "Target node tainted before gang placement was complete",
	},

	// Recovering -> Scheduling (pods replaced under next generation)
	{meridianv1alpha1.WorkloadPhaseRecovering, EventRestart}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseScheduling,
		DefaultReason:  "GenerationRestarted",
		DefaultMessage: "Workload pods re-issued with incremented generation",
	},

	// Running -> Succeeded
	{meridianv1alpha1.WorkloadPhaseRunning, EventSucceed}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseSucceeded,
		DefaultReason:  "WorkloadCompleted",
		DefaultMessage: "All gang member pods completed successfully",
	},

	// Terminal failure transitions
	{meridianv1alpha1.WorkloadPhasePending, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "AdmissionFailed",
		DefaultMessage: "Workload rejected due to fatal admission criteria",
	},
	{meridianv1alpha1.WorkloadPhaseQueued, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "QueueDropped",
		DefaultMessage: "Queue deleted or quota permanently exhausted",
	},
	{meridianv1alpha1.WorkloadPhaseAdmitted, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "PodTemplateError",
		DefaultMessage: "Failed to construct gang member pods from template",
	},
	{meridianv1alpha1.WorkloadPhaseScheduling, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "SchedulingTimeout",
		DefaultMessage: "Gang scheduling timed out without finding viable placement",
	},
	{meridianv1alpha1.WorkloadPhaseRunning, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "PodExecutionFailed",
		DefaultMessage: "One or more pods exited with non-zero failure code",
	},
	{meridianv1alpha1.WorkloadPhaseCheckpointing, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "CheckpointCorrupted",
		DefaultMessage: "Fatal error during checkpoint synchronization",
	},
	{meridianv1alpha1.WorkloadPhasePreempting, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "PreemptionDrainFailed",
		DefaultMessage: "Failed to drain victim pods within hard timeout",
	},
	{meridianv1alpha1.WorkloadPhaseRecovering, EventFail}: {
		TargetPhase:    meridianv1alpha1.WorkloadPhaseFailed,
		DefaultReason:  "MaxRestartsExceeded",
		DefaultMessage: "Maximum allowable restarts exceeded without recovery",
	},
}

// CanTransition returns true if the specified event is valid from the given current phase.
func CanTransition(from meridianv1alpha1.GPUWorkloadPhase, event Event) bool {
	_, ok := transitionTable[transitionKey{from: from, event: event}]
	return ok
}

// NextPhase computes the target phase for the given source phase and event.
// Returns an error if the transition is disallowed by the FSM table.
func NextPhase(from meridianv1alpha1.GPUWorkloadPhase, event Event) (meridianv1alpha1.GPUWorkloadPhase, string, error) {
	rule, ok := transitionTable[transitionKey{from: from, event: event}]
	if !ok {
		return "", "", fmt.Errorf("invalid transition: no rule from phase %q on event %q", from, event)
	}
	return rule.TargetPhase, rule.DefaultReason, nil
}

// IsTerminal returns true if the workload has reached an immutable end state (Succeeded or Failed).
func IsTerminal(phase meridianv1alpha1.GPUWorkloadPhase) bool {
	return phase == meridianv1alpha1.WorkloadPhaseSucceeded || phase == meridianv1alpha1.WorkloadPhaseFailed
}

// ApplyTransition executes a state machine transition against a workload status object.
// It verifies validity, updates the Phase, appends an audited TransitionRecord to History,
// updates standard timestamps, and bounds History to MaxHistoryEntries.
func ApplyTransition(
	status *meridianv1alpha1.GPUWorkloadStatus,
	event Event,
	customReason string,
	customMessage string,
	now time.Time,
) error {
	from := status.Phase
	if from == "" {
		from = meridianv1alpha1.WorkloadPhasePending
	}

	rule, ok := transitionTable[transitionKey{from: from, event: event}]
	if !ok {
		return fmt.Errorf("FSM transition rejected: cannot transition from %q on event %q", from, event)
	}

	reason := rule.DefaultReason
	if customReason != "" {
		reason = customReason
	}
	message := rule.DefaultMessage
	if customMessage != "" {
		message = customMessage
	}

	metaNow := metav1.NewTime(now)

	// Determine next monotonic sequence number
	var nextSeq int64 = 1
	if n := len(status.History); n > 0 {
		nextSeq = status.History[n-1].Seq + 1
	}

	record := meridianv1alpha1.TransitionRecord{
		Seq:     nextSeq,
		Time:    metaNow,
		From:    string(from),
		To:      string(rule.TargetPhase),
		Reason:  reason,
		Message: message,
	}

	status.Phase = rule.TargetPhase
	metrics.Default().RecordPhaseTransition(string(from), string(rule.TargetPhase))

	// Update phase-specific timestamps
	switch rule.TargetPhase {
	case meridianv1alpha1.WorkloadPhaseAdmitted:
		if status.AdmittedAt == nil {
			status.AdmittedAt = &metaNow
		}
	case meridianv1alpha1.WorkloadPhaseRunning:
		if status.StartedAt == nil {
			status.StartedAt = &metaNow
		}
	case meridianv1alpha1.WorkloadPhaseRecovering:
		status.Generation++
	}

	// Append record and maintain bounded history
	status.History = append(status.History, record)
	if len(status.History) > MaxHistoryEntries {
		status.History = status.History[len(status.History)-MaxHistoryEntries:]
	}

	return nil
}
