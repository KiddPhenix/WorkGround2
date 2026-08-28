package assistant

import (
	"testing"
	"time"

	"workground2/internal/event"
)

func TestBuildExperimentCandidatesProducesAtLeastTwoFromOptions(t *testing.T) {
	questions := []event.AskQuestion{{
		ID:      "q1",
		Prompt:  "Which environment?",
		Options: []event.AskOption{{Label: "Staging"}, {Label: "Production"}, {Label: "Local"}},
	}}
	inferred := []event.AskAnswer{{QuestionID: "q1", Selected: []string{"Staging"}}}

	got := BuildExperimentCandidates(questions, inferred)
	if len(got) < 2 {
		t.Fatalf("candidates = %d, want >= 2", len(got))
	}
	// The inferred answer is the first (most preferred) candidate.
	if len(got[0]) != 1 || got[0][0].QuestionID != "q1" || got[0][0].Selected[0] != "Staging" {
		t.Fatalf("first candidate = %+v, want inferred Staging", got[0])
	}
	// Every candidate is a distinct answer set.
	seen := map[string]bool{}
	for _, c := range got {
		key := EncodeTrialAnswer(c)
		if seen[key] {
			t.Fatalf("duplicate candidate %s", key)
		}
		seen[key] = true
	}
}

func TestBuildExperimentCandidatesFallsBackToVerbatim(t *testing.T) {
	questions := []event.AskQuestion{{
		ID:      "q1",
		Prompt:  "Continue?",
		Options: []event.AskOption{{Label: "Yes"}},
	}}
	inferred := []event.AskAnswer{{QuestionID: "q1", Selected: []string{"Yes"}}}

	got := BuildExperimentCandidates(questions, inferred)
	if len(got) < 2 {
		t.Fatalf("candidates = %d, want >= 2 (verbatim fallback)", len(got))
	}
	if got[1][0].Selected[0] != "Continue?" {
		t.Fatalf("verbatim candidate = %+v, want the question prompt", got[1])
	}
}

func TestTrialAnswerRoundTrips(t *testing.T) {
	answers := []event.AskAnswer{{QuestionID: "q1", Selected: []string{"A", "B"}}}
	raw := EncodeTrialAnswer(answers)
	got, err := DecodeTrialAnswer(raw)
	if err != nil {
		t.Fatalf("DecodeTrialAnswer: %v", err)
	}
	if len(got) != 1 || got[0].QuestionID != "q1" || len(got[0].Selected) != 2 {
		t.Fatalf("round-tripped answers = %+v", got)
	}
}

func TestResolveExperimentSettlesWhenAllTerminalAndCancelsLosers(t *testing.T) {
	trials := []TrialState{
		{SessionID: "fork-1", Answer: "A", Status: TrialStatusRunning},
		{SessionID: "fork-2", Answer: "B", Status: TrialStatusRunning},
		{SessionID: "fork-3", Answer: "C", Status: TrialStatusRunning},
	}
	status := func(id string) (string, bool) {
		switch id {
		case "fork-1":
			return TrialStatusFailed, true
		case "fork-2":
			return TrialStatusDone, true
		default:
			return TrialStatusRunning, true
		}
	}
	startedAt := time.Now().Add(-time.Minute)

	// fork-3 is still running: the race stays open even though fork-2 done.
	res := ResolveExperiment(trials, status, time.Now(), startedAt, time.Hour)
	if len(res.Pending) != 1 || res.Pending[0] != "fork-3" {
		t.Fatalf("pending = %v, want [fork-3] (race must stay open)", res.Pending)
	}
	if res.HasWinner || res.Fallback {
		t.Fatalf("settled early: winner=%v fallback=%v", res.HasWinner, res.Fallback)
	}

	// fork-3 completes: all terminal -> fork-2 (the only completed) wins.
	statusDone := func(id string) (string, bool) {
		switch id {
		case "fork-1":
			return TrialStatusFailed, true
		default:
			return TrialStatusDone, true
		}
	}
	res = ResolveExperiment(trials, statusDone, time.Now(), startedAt, time.Hour)
	if !res.HasWinner || res.Winner.SessionID != "fork-2" {
		t.Fatalf("winner = %+v hasWinner=%v, want fork-2", res.Winner, res.HasWinner)
	}
	if len(res.Losers) != 2 || res.Losers[0] != "fork-1" || res.Losers[1] != "fork-3" {
		t.Fatalf("losers = %v, want [fork-1 fork-3]", res.Losers)
	}
	if res.Fallback {
		t.Fatal("completed candidate present: must not fall back")
	}
}

func TestResolveExperimentPrefersFirstCompletedInPreferenceOrder(t *testing.T) {
	trials := []TrialState{
		{SessionID: "fork-1", Answer: "A", Status: TrialStatusRunning},
		{SessionID: "fork-2", Answer: "B", Status: TrialStatusRunning},
	}
	allDone := func(string) (string, bool) { return TrialStatusDone, true }
	res := ResolveExperiment(trials, allDone, time.Now(), time.Now().Add(-time.Minute), time.Hour)
	if !res.HasWinner || res.Winner.SessionID != "fork-1" {
		t.Fatalf("winner = %+v, want the first (inferred) candidate fork-1", res.Winner)
	}
	if len(res.Losers) != 1 || res.Losers[0] != "fork-2" {
		t.Fatalf("losers = %v, want [fork-2]", res.Losers)
	}
}

func TestResolveExperimentAllFailedFallsBack(t *testing.T) {
	trials := []TrialState{
		{SessionID: "fork-1", Answer: "A", Status: TrialStatusRunning},
		{SessionID: "fork-2", Answer: "B", Status: TrialStatusRunning},
	}
	allFailed := func(string) (string, bool) { return TrialStatusFailed, true }
	res := ResolveExperiment(trials, allFailed, time.Now(), time.Now().Add(-time.Minute), time.Hour)
	if res.HasWinner || !res.Fallback {
		t.Fatalf("winner=%v fallback=%v, want fallback with no winner", res.HasWinner, res.Fallback)
	}
	if len(res.Losers) != 2 {
		t.Fatalf("losers = %v, want both forks cancelled", res.Losers)
	}
}

func TestResolveExperimentTimeoutTerminatesRunningTrials(t *testing.T) {
	trials := []TrialState{
		{SessionID: "fork-1", Answer: "A", Status: TrialStatusRunning},
		{SessionID: "fork-2", Answer: "B", Status: TrialStatusRunning},
	}
	allRunning := func(string) (string, bool) { return TrialStatusRunning, true }
	startedAt := time.Now().Add(-2 * time.Hour)

	res := ResolveExperiment(trials, allRunning, time.Now(), startedAt, time.Hour)
	if len(res.Pending) != 0 {
		t.Fatalf("pending = %v, want none (all timed out)", res.Pending)
	}
	if len(res.TimedOut) != 2 {
		t.Fatalf("timed_out = %v, want both trials", res.TimedOut)
	}
	if !res.Fallback {
		t.Fatal("no candidate completed: must fall back")
	}

	// A vanished session also counts as terminal (failed).
	res = ResolveExperiment(trials, func(string) (string, bool) { return "", false }, time.Now(), startedAt, time.Hour)
	if len(res.Pending) != 0 || !res.Fallback {
		t.Fatalf("vanished sessions: pending=%v fallback=%v, want settled fallback", res.Pending, res.Fallback)
	}
}
