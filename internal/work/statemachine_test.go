package work

import (
	"errors"
	"strconv"
	"strings"
	"testing"
)

// ── WorkState transition tests ─────────────────────────────────────────────

func TestWorkStateTransitions_Valid(t *testing.T) {
	valid := []struct {
		from, to WorkState
	}{
		{WorkDraft, WorkReady},
		{WorkDraft, WorkRunning},
		{WorkReady, WorkDraft},
		{WorkReady, WorkRunning},
		{WorkRunning, WorkCompleted},
		{WorkRunning, WorkFailed},
		{WorkRunning, WorkWaitingUser},
		{WorkRunning, WorkPaused},
		{WorkRunning, WorkCancelled},
		{WorkWaitingUser, WorkRunning},
		{WorkWaitingUser, WorkCancelled},
		{WorkPaused, WorkRunning},
		{WorkPaused, WorkCancelled},
		{WorkCompleted, WorkRunning},
		{WorkFailed, WorkRunning},
		{WorkFailed, WorkDraft},
		{WorkCancelled, WorkDraft},
	}
	for _, tc := range valid {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if err := ValidateWorkTransition(tc.from, tc.to); err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
			if !CanTransitionWork(tc.from, tc.to) {
				t.Error("CanTransitionWork returned false for valid transition")
			}
		})
	}
}

func TestWorkStateTransitions_SameState(t *testing.T) {
	states := []WorkState{
		WorkDraft, WorkReady, WorkRunning, WorkWaitingUser,
		WorkPaused, WorkCompleted, WorkFailed, WorkCancelled,
	}
	for _, s := range states {
		t.Run(string(s), func(t *testing.T) {
			if err := ValidateWorkTransition(s, s); err != nil {
				t.Errorf("same-state transition should be valid: %v", err)
			}
		})
	}
}

func TestWorkStateTransitions_Invalid(t *testing.T) {
	// Build the set of all invalid transitions by excluding the valid + same-state ones.
	allStates := []WorkState{
		WorkDraft, WorkReady, WorkRunning, WorkWaitingUser,
		WorkPaused, WorkCompleted, WorkFailed, WorkCancelled,
	}
	validSet := map[[2]WorkState]bool{
		{WorkDraft, WorkReady}:           true,
		{WorkDraft, WorkRunning}:         true,
		{WorkReady, WorkDraft}:           true,
		{WorkReady, WorkRunning}:         true,
		{WorkRunning, WorkCompleted}:     true,
		{WorkRunning, WorkFailed}:        true,
		{WorkRunning, WorkWaitingUser}:   true,
		{WorkRunning, WorkPaused}:        true,
		{WorkRunning, WorkCancelled}:     true,
		{WorkWaitingUser, WorkRunning}:   true,
		{WorkWaitingUser, WorkCancelled}: true,
		{WorkPaused, WorkRunning}:        true,
		{WorkPaused, WorkCancelled}:      true,
		{WorkCompleted, WorkRunning}:     true,
		{WorkFailed, WorkRunning}:        true,
		{WorkFailed, WorkDraft}:          true,
		{WorkCancelled, WorkDraft}:       true,
	}

	for _, from := range allStates {
		for _, to := range allStates {
			if from == to {
				continue
			}
			key := [2]WorkState{from, to}
			if validSet[key] {
				continue
			}
			err := ValidateWorkTransition(from, to)
			if err == nil {
				t.Errorf("expected invalid transition %s → %s to fail", from, to)
			}
			if CanTransitionWork(from, to) {
				t.Errorf("CanTransitionWork returned true for invalid %s → %s", from, to)
			}
		}
	}
	for _, tc := range [][2]WorkState{
		{"bogus", "bogus"},
		{"bogus", WorkDraft},
		{WorkDraft, "bogus"},
	} {
		if err := ValidateWorkTransition(tc[0], tc[1]); err == nil {
			t.Errorf("expected unknown transition %q → %q to fail", tc[0], tc[1])
		}
	}
}

func TestWorkStateTransitions_ErrorFormat(t *testing.T) {
	err := ValidateWorkTransition(WorkDraft, WorkCompleted)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "draft") || !strings.Contains(msg, "completed") {
		t.Errorf("error message missing states: %s", msg)
	}
}

// ── ArchiveState transition tests ──────────────────────────────────────────

func TestArchiveStateTransitions_Valid(t *testing.T) {
	valid := []struct {
		from, to WorkArchiveState
	}{
		{ArchiveActive, ArchiveArchived},
		{ArchiveActive, ArchiveDeleted},
		{ArchiveArchived, ArchiveActive},
		{ArchiveArchived, ArchiveDeleted},
		{ArchiveDeleted, ArchiveArchived},
		{ArchiveDeleted, ArchiveActive},
	}
	for _, tc := range valid {
		t.Run(string(tc.from)+"_to_"+string(tc.to), func(t *testing.T) {
			if err := ValidateArchiveTransition(tc.from, tc.to); err != nil {
				t.Errorf("expected valid, got: %v", err)
			}
			if !CanTransitionArchive(tc.from, tc.to) {
				t.Error("CanTransitionArchive returned false for valid transition")
			}
		})
	}
}

func TestArchiveStateTransitions_SameState(t *testing.T) {
	states := []WorkArchiveState{ArchiveActive, ArchiveArchived, ArchiveDeleted}
	for _, s := range states {
		t.Run(string(s), func(t *testing.T) {
			if err := ValidateArchiveTransition(s, s); err != nil {
				t.Errorf("same-state transition should be valid: %v", err)
			}
		})
	}
}

func TestArchiveStateTransitions_Invalid(t *testing.T) {
	for _, tc := range [][2]WorkArchiveState{
		{"bogus", "bogus"},
		{"bogus", ArchiveActive},
		{ArchiveActive, "bogus"},
	} {
		if err := ValidateArchiveTransition(tc[0], tc[1]); err == nil {
			t.Errorf("expected unknown transition %q → %q to fail", tc[0], tc[1])
		}
	}
}

// ── Future schema tests ────────────────────────────────────────────────────

func TestCheckSchemaVersion_CurrentAndPast(t *testing.T) {
	tests := []struct {
		kind string
		ver  int
	}{
		{"Work", 0},
		{"Work", 1},
		{"WorkRecord", 1},
		{"WorkEvent", 1},
	}
	for _, tc := range tests {
		t.Run(tc.kind+"/v"+strconv.Itoa(tc.ver), func(t *testing.T) {
			if err := CheckSchemaVersion(tc.kind, tc.ver); err != nil {
				t.Errorf("schema %d should be valid, got: %v", tc.ver, err)
			}
		})
	}
}

func TestCheckSchemaVersion_Future(t *testing.T) {
	tests := []struct {
		kind    string
		ver     int
		wantMsg string
	}{
		{"Work", 2, "read-only"},
		{"WorkRecord", 99, "read-only"},
	}
	for _, tc := range tests {
		t.Run(tc.kind+"/v"+strconv.Itoa(tc.ver), func(t *testing.T) {
			err := CheckSchemaVersion(tc.kind, tc.ver)
			if err == nil {
				t.Fatal("expected ErrFutureSchema")
			}
			if !strings.Contains(err.Error(), tc.wantMsg) {
				t.Errorf("error missing %q: %v", tc.wantMsg, err)
			}
			e, ok := err.(*ErrFutureSchema)
			if !ok {
				t.Fatalf("expected *ErrFutureSchema, got %T", err)
			}
			if e.Got != tc.ver {
				t.Errorf("Got = %d, want %d", e.Got, tc.ver)
			}
			if e.CurrentMax != SchemaVersion {
				t.Errorf("CurrentMax = %d, want %d", e.CurrentMax, SchemaVersion)
			}
		})
	}
}

func TestIsFutureSchema(t *testing.T) {
	tests := []struct {
		ver      int
		expected bool
	}{
		{0, false},
		{1, false},
		{2, true},
		{99, true},
	}
	for _, tc := range tests {
		t.Run("v"+strconv.Itoa(tc.ver), func(t *testing.T) {
			got := IsFutureSchema(tc.ver)
			if got != tc.expected {
				t.Errorf("IsFutureSchema(%d) = %v, want %v", tc.ver, got, tc.expected)
			}
		})
	}
}

func TestErrFutureSchema_TypeAssert(t *testing.T) {
	err := CheckSchemaVersion("Work", 5)
	if err == nil {
		t.Fatal("expected error")
	}
	var fs *ErrFutureSchema
	if !errors.As(err, &fs) {
		t.Fatalf("err is %T, not *ErrFutureSchema", err)
	}
}
