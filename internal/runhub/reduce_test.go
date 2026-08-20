package runhub

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func baseRun(state RunState, rev uint64) AgentRun {
	now := time.Unix(1_700_000_000, 0)
	return AgentRun{
		ID:         RunID("run_test"),
		Source:     SourceDSH,
		Ownership:  OwnershipManaged,
		State:      state,
		Revision:   rev,
		LastSeenAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
}

func TestReduceTerminalIsFrozen(t *testing.T) {
	run := baseRun(StateSucceeded, 5)
	for _, typ := range []EventType{
		EventQueued, EventStarting, EventRunning, EventWaitingUser,
		EventActivity, EventSummary, EventTitle, EventFailed, EventCancelled,
	} {
		_, err := Reduce(run, RunEvent{EventID: EventID("e_" + string(typ)), RunID: run.ID, Type: typ})
		var te *TransitionError
		if !errors.As(err, &te) || te.Code != TransitionStale {
			t.Fatalf("Reduce(%s) on terminal run: got %v, want TransitionStale", typ, err)
		}
	}
}

func TestReduceInvalidTransition(t *testing.T) {
	cases := []struct {
		from RunState
		to   EventType
	}{
		{StateRunning, EventStarting},
		{StateStarting, EventQueued},
		{StateWaitingUser, EventStarting},
		{StateWaitingUser, EventQueued},
		{StateRunning, EventQueued},
	}
	for _, c := range cases {
		run := baseRun(c.from, 3)
		_, err := Reduce(run, RunEvent{EventID: "e_invalid", RunID: run.ID, Type: c.to})
		var te *TransitionError
		if !errors.As(err, &te) || te.Code != TransitionInvalid {
			t.Fatalf("%s -> %s: got %v, want TransitionInvalid", c.from, c.to, err)
		}
	}
}

func TestReduceTerminalFromNonTerminal(t *testing.T) {
	run := baseRun(StateRunning, 3)
	next, err := Reduce(run, RunEvent{EventID: "e_success", RunID: run.ID, Type: EventSucceeded})
	if err != nil {
		t.Fatalf("Reduce succeeded: %v", err)
	}
	if next.State != StateSucceeded {
		t.Fatalf("state = %s, want succeeded", next.State)
	}
	if next.Revision != 4 {
		t.Fatalf("revision = %d, want 4", next.Revision)
	}
	if next.Activity != ActivityIdle || next.ActivityLabel != "" {
		t.Fatalf("terminal activity not cleared: %q %q", next.Activity, next.ActivityLabel)
	}
}

func TestReduceWaitingUserRoundTrip(t *testing.T) {
	run := baseRun(StateRunning, 3)
	waiting, err := Reduce(run, RunEvent{EventID: "e_wait", RunID: run.ID, Type: EventWaitingUser})
	if err != nil || waiting.State != StateWaitingUser {
		t.Fatalf("running -> waiting_user: state=%s err=%v", waiting.State, err)
	}
	back, err := Reduce(waiting, RunEvent{EventID: "e_resume", RunID: run.ID, Type: EventRunning})
	if err != nil || back.State != StateRunning {
		t.Fatalf("waiting_user -> running: state=%s err=%v", back.State, err)
	}
}

func TestReduceMetadataOnly(t *testing.T) {
	run := baseRun(StateRunning, 3)

	next, err := Reduce(run, RunEvent{EventID: "e_act", RunID: run.ID, Type: EventActivity,
		Payload: EventPayload{Activity: ActivityTool, Label: "read_file"}})
	if err != nil {
		t.Fatalf("activity reduce: %v", err)
	}
	if next.State != StateRunning || next.Activity != ActivityTool || next.ActivityLabel != "read_file" {
		t.Fatalf("activity not applied: %+v", next)
	}

	next, err = Reduce(next, RunEvent{EventID: "e_sum", RunID: run.ID, Type: EventSummary, Payload: EventPayload{Summary: "done"}})
	if err != nil || next.Summary != "done" {
		t.Fatalf("summary not applied: %+v err=%v", next, err)
	}

	next, err = Reduce(next, RunEvent{EventID: "e_title", RunID: run.ID, Type: EventTitle, Payload: EventPayload{Title: "t"}})
	if err != nil || next.Title != "t" {
		t.Fatalf("title not applied: %+v err=%v", next, err)
	}
	if next.Revision != 6 {
		t.Fatalf("revision = %d, want 6", next.Revision)
	}
}

func TestReduceExpectRevisionMismatchIsStale(t *testing.T) {
	run := baseRun(StateRunning, 4)
	_, err := Reduce(run, RunEvent{EventID: "e_stale", RunID: run.ID, Type: EventRunning, ExpectRevision: 3})
	var te *TransitionError
	if !errors.As(err, &te) || te.Code != TransitionStale {
		t.Fatalf("got %v, want TransitionStale", err)
	}
}

func TestValidateLaunchIntent(t *testing.T) {
	if err := ValidateLaunchIntent(LaunchIntent{RequestID: "req-1", Source: SourceDSH}); err != nil {
		t.Fatalf("valid intent rejected: %v", err)
	}
	for _, bad := range []string{"", "../escape", "a/b", "a b"} {
		if err := ValidateLaunchIntent(LaunchIntent{RequestID: bad, Source: SourceDSH}); err == nil {
			t.Fatalf("requestId %q accepted, want error", bad)
		}
	}
	if err := ValidateLaunchIntent(LaunchIntent{RequestID: "req-1"}); err == nil {
		t.Fatal("launch without source accepted")
	}
}

func TestValidateEvent(t *testing.T) {
	good := RunEvent{EventID: "evt-1", RunID: "run_1", Type: EventRunning}
	if err := ValidateEvent(good); err != nil {
		t.Fatalf("valid event rejected: %v", err)
	}
	for _, bad := range []RunEvent{
		{EventID: "", RunID: "run_1", Type: EventRunning},
		{EventID: "evt-1", RunID: "", Type: EventRunning},
		{EventID: "evt-1", RunID: "run_1", Type: ""},
		{EventID: "evt-1", RunID: "run_1", Type: EventType("mystery")},
		{EventID: "evt-1", RunID: "run_1", Type: EventRunning, Source: Source("nope")},
		{EventID: "evt-1", RunID: "run_1", Type: EventActivity, Payload: EventPayload{Activity: Activity("nope")}},
		{EventID: "a/b", RunID: "run_1", Type: EventRunning},
		{EventID: "evt-1", RunID: "..", Type: EventRunning},
	} {
		if err := ValidateEvent(bad); err == nil {
			t.Fatalf("event %+v accepted, want error", bad)
		}
	}
}

func TestValidateEventAllowsOpaqueColonIDs(t *testing.T) {
	for _, evt := range []RunEvent{
		{EventID: "ds:e1:2", RunID: "run_1", Type: EventRunning},
		{EventID: "evt-1", RunID: "run_abc", Type: EventRunning},
	} {
		if err := ValidateEvent(evt); err != nil {
			t.Fatalf("opaque id event %+v rejected: %v", evt, err)
		}
	}
}

func TestValidateLaunchIntentAllowsOpaqueColonIDs(t *testing.T) {
	for _, id := range []string{"req:1", "ds:req:2"} {
		if err := ValidateLaunchIntent(LaunchIntent{RequestID: id, Source: SourceDSH}); err != nil {
			t.Fatalf("requestId %q rejected: %v", id, err)
		}
	}
	for _, bad := range []string{"a b", "a\nb", "a/b", "a\\b", "../escape"} {
		if err := ValidateLaunchIntent(LaunchIntent{RequestID: bad, Source: SourceDSH}); err == nil {
			t.Fatalf("requestId %q accepted, want error", bad)
		}
	}
}

func TestEventPayloadHasNoRawField(t *testing.T) {
	typ := reflect.TypeOf(EventPayload{})
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if f.Name == "Raw" || strings.Contains(f.Tag.Get("json"), "raw") {
			t.Fatalf("EventPayload exposes arbitrary payload field %q (tag %q)", f.Name, f.Tag)
		}
	}
}

func TestRunStateTerminalTruthTable(t *testing.T) {
	want := map[RunState]bool{
		StateQueued: false, StateStarting: false, StateRunning: false, StateWaitingUser: false,
		StateSucceeded: true, StateFailed: true, StateCancelled: true, StateInterrupted: true, StateStale: true,
	}
	for _, s := range []RunState{
		StateQueued, StateStarting, StateRunning, StateWaitingUser,
		StateSucceeded, StateFailed, StateCancelled, StateInterrupted, StateStale,
	} {
		if s.IsTerminal() != want[s] {
			t.Fatalf("IsTerminal(%s) = %v, want %v", s, s.IsTerminal(), want[s])
		}
	}
}

func TestEnumValidity(t *testing.T) {
	for _, s := range []Source{SourceDSH, SourceCodex, SourceClaude} {
		if !s.Valid() {
			t.Fatalf("source %q should be valid", s)
		}
	}
	if Source("nope").Valid() || Source("").Valid() {
		t.Fatalf("invalid/empty source reported valid")
	}
	for _, o := range []Ownership{OwnershipManaged, OwnershipObserved} {
		if !o.Valid() {
			t.Fatalf("ownership %q should be valid", o)
		}
	}
	if Ownership("nope").Valid() || Ownership("").Valid() {
		t.Fatalf("invalid/empty ownership reported valid")
	}
	for _, a := range []Activity{ActivityThinking, ActivityTool, ActivityResponding, ActivityBackground, ActivityIdle} {
		if !a.Valid() {
			t.Fatalf("activity %q should be valid", a)
		}
	}
	if Activity("nope").Valid() {
		t.Fatalf("invalid activity reported valid")
	}
	if EventType("nope").Valid() {
		t.Fatalf("invalid event type reported valid")
	}
	if ReceiptStatus("nope").Valid() {
		t.Fatalf("invalid receipt status reported valid")
	}
}

func TestReduceRejectsUnknownEventType(t *testing.T) {
	run := baseRun(StateRunning, 3)
	_, err := Reduce(run, RunEvent{EventID: "e_unknown", RunID: run.ID, Type: EventType("mystery")})
	var te *TransitionError
	if !errors.As(err, &te) || te.Code != TransitionInvalid {
		t.Fatalf("unknown type: got %v, want TransitionInvalid", err)
	}
}

func TestReduceRejectsConflictingSource(t *testing.T) {
	run := baseRun(StateRunning, 3) // Source: SourceDSH
	_, err := Reduce(run, RunEvent{EventID: "e_codex", RunID: run.ID, Type: EventRunning, Source: SourceCodex})
	var te *TransitionError
	if !errors.As(err, &te) || te.Code != TransitionInvalid {
		t.Fatalf("conflicting source: got %v, want TransitionInvalid", err)
	}
}

func TestReduceRejectsInvalidSource(t *testing.T) {
	run := baseRun(StateRunning, 3)
	_, err := Reduce(run, RunEvent{EventID: "e_bad", RunID: run.ID, Type: EventRunning, Source: Source("nope")})
	var te *TransitionError
	if !errors.As(err, &te) || te.Code != TransitionInvalid {
		t.Fatalf("invalid source: got %v, want TransitionInvalid", err)
	}
}

func TestReduceRejectsInvalidActivity(t *testing.T) {
	run := baseRun(StateRunning, 3)
	_, err := Reduce(run, RunEvent{EventID: "e_act", RunID: run.ID, Type: EventActivity, Payload: EventPayload{Activity: Activity("nope")}})
	var te *TransitionError
	if !errors.As(err, &te) || te.Code != TransitionInvalid {
		t.Fatalf("invalid activity: got %v, want TransitionInvalid", err)
	}
}
