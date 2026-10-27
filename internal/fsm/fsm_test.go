package fsm_test

import (
	"fmt"
	"testing"
	"time"

	meridianv1alpha1 "github.com/tijani-web/meridian/api/v1alpha1"
	"github.com/tijani-web/meridian/internal/fsm"
)

// TestValidTransitions verifies every legitimate transition in the FSM table.
func TestValidTransitions(t *testing.T) {
	testCases := []struct {
		name        string
		from        meridianv1alpha1.GPUWorkloadPhase
		event       fsm.Event
		expectedTo  meridianv1alpha1.GPUWorkloadPhase
		expectGenUp bool
	}{
		{
			name:       "Pending to Queued via Enqueue",
			from:       meridianv1alpha1.WorkloadPhasePending,
			event:      fsm.EventEnqueue,
			expectedTo: meridianv1alpha1.WorkloadPhaseQueued,
		},
		{
			name:       "Queued to Admitted via Admit",
			from:       meridianv1alpha1.WorkloadPhaseQueued,
			event:      fsm.EventAdmit,
			expectedTo: meridianv1alpha1.WorkloadPhaseAdmitted,
		},
		{
			name:       "Admitted to Scheduling via StartScheduling",
			from:       meridianv1alpha1.WorkloadPhaseAdmitted,
			event:      fsm.EventStartScheduling,
			expectedTo: meridianv1alpha1.WorkloadPhaseScheduling,
		},
		{
			name:       "Scheduling to Running via AllRunning",
			from:       meridianv1alpha1.WorkloadPhaseScheduling,
			event:      fsm.EventAllRunning,
			expectedTo: meridianv1alpha1.WorkloadPhaseRunning,
		},
		{
			name:       "Running to Checkpointing via RequestCheckpoint",
			from:       meridianv1alpha1.WorkloadPhaseRunning,
			event:      fsm.EventRequestCheckpoint,
			expectedTo: meridianv1alpha1.WorkloadPhaseCheckpointing,
		},
		{
			name:       "Checkpointing to Running via CheckpointAck",
			from:       meridianv1alpha1.WorkloadPhaseCheckpointing,
			event:      fsm.EventCheckpointAck,
			expectedTo: meridianv1alpha1.WorkloadPhaseRunning,
		},
		{
			name:       "Running to Preempting via Preempt",
			from:       meridianv1alpha1.WorkloadPhaseRunning,
			event:      fsm.EventPreempt,
			expectedTo: meridianv1alpha1.WorkloadPhasePreempting,
		},
		{
			name:       "Preempting to Queued via EvictToQueue",
			from:       meridianv1alpha1.WorkloadPhasePreempting,
			event:      fsm.EventEvictToQueue,
			expectedTo: meridianv1alpha1.WorkloadPhaseQueued,
		},
		{
			name:        "Running to Recovering via NodeFailure",
			from:        meridianv1alpha1.WorkloadPhaseRunning,
			event:       fsm.EventNodeFailure,
			expectedTo:  meridianv1alpha1.WorkloadPhaseRecovering,
			expectGenUp: true,
		},
		{
			name:       "Recovering to Scheduling via Restart",
			from:       meridianv1alpha1.WorkloadPhaseRecovering,
			event:      fsm.EventRestart,
			expectedTo: meridianv1alpha1.WorkloadPhaseScheduling,
		},
		{
			name:       "Running to Succeeded via Succeed",
			from:       meridianv1alpha1.WorkloadPhaseRunning,
			event:      fsm.EventSucceed,
			expectedTo: meridianv1alpha1.WorkloadPhaseSucceeded,
		},
		{
			name:       "Running to Failed via Fail",
			from:       meridianv1alpha1.WorkloadPhaseRunning,
			event:      fsm.EventFail,
			expectedTo: meridianv1alpha1.WorkloadPhaseFailed,
		},
	}

	now := time.Now()
	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			if !fsm.CanTransition(tc.from, tc.event) {
				t.Fatalf("expected CanTransition(%q, %q) = true, got false", tc.from, tc.event)
			}

			next, _, err := fsm.NextPhase(tc.from, tc.event)
			if err != nil {
				t.Fatalf("unexpected NextPhase error: %v", err)
			}
			if next != tc.expectedTo {
				t.Fatalf("NextPhase mismatch: expected %q, got %q", tc.expectedTo, next)
			}

			status := &meridianv1alpha1.GPUWorkloadStatus{
				Phase:      tc.from,
				Generation: 1,
			}

			err = fsm.ApplyTransition(status, tc.event, "", "", now)
			if err != nil {
				t.Fatalf("ApplyTransition failed: %v", err)
			}

			if status.Phase != tc.expectedTo {
				t.Fatalf("ApplyTransition phase: expected %q, got %q", tc.expectedTo, status.Phase)
			}

			if tc.expectGenUp && status.Generation != 2 {
				t.Fatalf("expected generation increment from 1 to 2, got %d", status.Generation)
			}

			if len(status.History) != 1 {
				t.Fatalf("expected 1 history record, got %d", len(status.History))
			}

			rec := status.History[0]
			if rec.From != string(tc.from) || rec.To != string(tc.expectedTo) {
				t.Fatalf("history record mismatch: from=%q to=%q", rec.From, rec.To)
			}
			if rec.Seq != 1 {
				t.Fatalf("expected sequence 1, got %d", rec.Seq)
			}
		})
	}
}

// TestInvalidTransitions verifies that illegal state transitions are strictly rejected.
func TestInvalidTransitions(t *testing.T) {
	invalidCases := []struct {
		from  meridianv1alpha1.GPUWorkloadPhase
		event fsm.Event
	}{
		{meridianv1alpha1.WorkloadPhasePending, fsm.EventAllRunning},
		{meridianv1alpha1.WorkloadPhasePending, fsm.EventCheckpointAck},
		{meridianv1alpha1.WorkloadPhaseQueued, fsm.EventAllRunning},
		{meridianv1alpha1.WorkloadPhaseAdmitted, fsm.EventAllRunning},
		{meridianv1alpha1.WorkloadPhaseSucceeded, fsm.EventEnqueue},
		{meridianv1alpha1.WorkloadPhaseSucceeded, fsm.EventRestart},
		{meridianv1alpha1.WorkloadPhaseFailed, fsm.EventAdmit},
		{meridianv1alpha1.WorkloadPhaseFailed, fsm.EventRestart},
	}

	for _, tc := range invalidCases {
		name := fmt.Sprintf("%s on %s", tc.from, tc.event)
		t.Run(name, func(t *testing.T) {
			if fsm.CanTransition(tc.from, tc.event) {
				t.Fatalf("expected CanTransition(%q, %q) = false, got true", tc.from, tc.event)
			}

			_, _, err := fsm.NextPhase(tc.from, tc.event)
			if err == nil {
				t.Fatalf("expected NextPhase error for %q on %q, got nil", tc.from, tc.event)
			}

			status := &meridianv1alpha1.GPUWorkloadStatus{Phase: tc.from}
			err = fsm.ApplyTransition(status, tc.event, "", "", time.Now())
			if err == nil {
				t.Fatalf("expected ApplyTransition error for %q on %q, got nil", tc.from, tc.event)
			}
		})
	}
}

// TestTerminalStates verifies terminal state detection.
func TestTerminalStates(t *testing.T) {
	if !fsm.IsTerminal(meridianv1alpha1.WorkloadPhaseSucceeded) {
		t.Errorf("expected Succeeded to be terminal")
	}
	if !fsm.IsTerminal(meridianv1alpha1.WorkloadPhaseFailed) {
		t.Errorf("expected Failed to be terminal")
	}
	if fsm.IsTerminal(meridianv1alpha1.WorkloadPhaseRunning) {
		t.Errorf("expected Running to NOT be terminal")
	}
	if fsm.IsTerminal(meridianv1alpha1.WorkloadPhasePending) {
		t.Errorf("expected Pending to NOT be terminal")
	}
}

// TestHistoryBounding verifies that history stays within MaxHistoryEntries and maintains monotonic seq.
func TestHistoryBounding(t *testing.T) {
	status := &meridianv1alpha1.GPUWorkloadStatus{Phase: meridianv1alpha1.WorkloadPhasePending}
	now := time.Now()

	// Initial transition to Queued
	if err := fsm.ApplyTransition(status, fsm.EventEnqueue, "", "", now); err != nil {
		t.Fatalf("failed initial transition: %v", err)
	}

	// Loop back and forth between Queued -> Admitted -> Scheduling -> Preempting -> Queued
	for i := 0; i < 30; i++ {
		if err := fsm.ApplyTransition(status, fsm.EventAdmit, "", "", now); err != nil {
			t.Fatalf("admit %d failed: %v", i, err)
		}
		if err := fsm.ApplyTransition(status, fsm.EventStartScheduling, "", "", now); err != nil {
			t.Fatalf("schedule %d failed: %v", i, err)
		}
		if err := fsm.ApplyTransition(status, fsm.EventPreempt, "", "", now); err != nil {
			t.Fatalf("preempt %d failed: %v", i, err)
		}
		if err := fsm.ApplyTransition(status, fsm.EventEvictToQueue, "", "", now); err != nil {
			t.Fatalf("evict %d failed: %v", i, err)
		}
	}

	if len(status.History) > fsm.MaxHistoryEntries {
		t.Fatalf("history exceeded max %d: got %d", fsm.MaxHistoryEntries, len(status.History))
	}

	// Verify monotonically increasing sequence numbers
	for i := 1; i < len(status.History); i++ {
		if status.History[i].Seq <= status.History[i-1].Seq {
			t.Fatalf("non-monotonic seq at %d: %d <= %d",
				i, status.History[i].Seq, status.History[i-1].Seq)
		}
	}
}
