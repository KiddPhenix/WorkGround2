package work

import "fmt"

// ── WorkState transitions ──────────────────────────────────────────────────

// validWorkTransitions is the closed set of allowed WorkState changes derived
// from the V1 state diagram (§5.3).
var validWorkTransitions = map[WorkState]map[WorkState]bool{
	WorkDraft: {
		WorkReady:   true, // input and dependency preflight passed
		WorkRunning: true,
	},
	WorkReady: {
		WorkDraft:   true, // edits or dependency changes invalidated preflight
		WorkRunning: true,
	},
	WorkRunning: {
		WorkCompleted:   true,
		WorkFailed:      true,
		WorkWaitingUser: true,
		WorkPaused:      true,
		WorkCancelled:   true,
	},
	WorkWaitingUser: {
		WorkRunning: true,
	},
	WorkPaused: {
		WorkRunning: true,
	},
	WorkCompleted: {
		WorkRunning: true,
	},
	WorkFailed: {
		WorkRunning: true,
		WorkDraft:   true,
	},
	WorkCancelled: {
		WorkDraft: true,
	},
}

// ValidateWorkTransition returns nil if the transition from → to is legal.
func ValidateWorkTransition(from, to WorkState) error {
	if !isWorkState(from) || !isWorkState(to) {
		return fmt.Errorf("work: unknown WorkState in transition %q → %q", from, to)
	}
	if from == to {
		return nil
	}
	dests, ok := validWorkTransitions[from]
	if !ok || !dests[to] {
		return fmt.Errorf("work: invalid WorkState transition %s → %s", from, to)
	}
	return nil
}

func isWorkState(state WorkState) bool {
	_, ok := validWorkTransitions[state]
	return ok
}

// CanTransitionWork reports whether from → to is a legal WorkState change.
func CanTransitionWork(from, to WorkState) bool {
	return ValidateWorkTransition(from, to) == nil
}

// ── ArchiveState transitions ───────────────────────────────────────────────

// validArchiveTransitions is the closed set of allowed ArchiveState changes.
// Archive is an independent lifecycle: active ↔ archived → deleted, with
// grace-period restore.
var validArchiveTransitions = map[WorkArchiveState]map[WorkArchiveState]bool{
	ArchiveActive: {
		ArchiveArchived: true,
		ArchiveDeleted:  true,
	},
	ArchiveArchived: {
		ArchiveActive:  true,
		ArchiveDeleted: true,
	},
	ArchiveDeleted: {
		ArchiveArchived: true,
		ArchiveActive:   true,
	},
}

// ValidateArchiveTransition returns nil if the transition from → to is legal.
func ValidateArchiveTransition(from, to WorkArchiveState) error {
	if !isArchiveState(from) || !isArchiveState(to) {
		return fmt.Errorf("work: unknown ArchiveState in transition %q → %q", from, to)
	}
	if from == to {
		return nil
	}
	dests, ok := validArchiveTransitions[from]
	if !ok || !dests[to] {
		return fmt.Errorf("work: invalid ArchiveState transition %s → %s", from, to)
	}
	return nil
}

func isArchiveState(state WorkArchiveState) bool {
	_, ok := validArchiveTransitions[state]
	return ok
}

// CanTransitionArchive reports whether from → to is a legal ArchiveState change.
func CanTransitionArchive(from, to WorkArchiveState) bool {
	return ValidateArchiveTransition(from, to) == nil
}

// ── Future schema protection ───────────────────────────────────────────────

// ErrFutureSchema is returned when a Work, WorkRecord, or WorkEvent carries a
// SchemaVersion greater than the current binary's SchemaVersion. Callers must
// refuse writes and may offer read-only access.
type ErrFutureSchema struct {
	Kind       string // "Work", "WorkRecord", or "WorkEvent"
	Got        int
	CurrentMax int
}

func (e *ErrFutureSchema) Error() string {
	return fmt.Sprintf("work: %s schema version %d exceeds current max %d; read-only access is required",
		e.Kind, e.Got, e.CurrentMax)
}

// CheckSchemaVersion returns ErrFutureSchema if got > SchemaVersion.
func CheckSchemaVersion(kind string, got int) error {
	if got > SchemaVersion {
		return &ErrFutureSchema{Kind: kind, Got: got, CurrentMax: SchemaVersion}
	}
	return nil
}

// IsFutureSchema returns true when schemaVersion exceeds the current V1
// SchemaVersion.
func IsFutureSchema(schemaVersion int) bool {
	return schemaVersion > SchemaVersion
}
