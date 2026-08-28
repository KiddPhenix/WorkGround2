package assistant

import (
	"testing"
)

func hasTrigger(triggers []ExpansionTrigger, want ExpansionTrigger) bool {
	for _, t := range triggers {
		if t == want {
			return true
		}
	}
	return false
}

func TestEvaluateExpansionPlanEmpty(t *testing.T) {
	now := testEpoch
	empty := Snapshot{Plan: emptyPlan()}
	triggers := EvaluateExpansion(empty, now)
	if !hasTrigger(triggers, ExpansionPlanEmpty) {
		t.Fatalf("triggers = %v, want plan_empty", triggers)
	}
}

func TestEvaluateExpansionNotTriggeredWhileRunning(t *testing.T) {
	now := testEpoch
	snapshot := Snapshot{
		Plan: Plan{Revision: 1, Responsibilities: []Responsibility{{
			ID: "r1", AssistantID: "a", Objective: "do", Status: RespActive, Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		}}},
		Runs: []Run{{State: RunRunning, ID: "run-1", AssistantID: "a"}},
	}
	if triggers := EvaluateExpansion(snapshot, now); len(triggers) != 0 {
		t.Fatalf("triggers = %v, want none while running", triggers)
	}
}

func TestEvaluateExpansionStalled(t *testing.T) {
	now := testEpoch
	old := now.Add(-2 * expansionStalledThreshold)
	snapshot := Snapshot{
		Plan: Plan{Revision: 1, Responsibilities: []Responsibility{{
			ID: "r1", AssistantID: "a", Objective: "do", Status: RespReady, Revision: 1,
			CreatedAt: old, UpdatedAt: old,
		}}},
	}
	triggers := EvaluateExpansion(snapshot, now)
	if !hasTrigger(triggers, ExpansionStalled) {
		t.Fatalf("triggers = %v, want stalled", triggers)
	}
}

func TestEvaluateExpansionRepeatedFailure(t *testing.T) {
	now := testEpoch
	snapshot := Snapshot{
		Plan: Plan{Revision: 1, Responsibilities: []Responsibility{{
			ID: "r1", AssistantID: "a", Objective: "do", Status: RespReady, Revision: 1,
			CreatedAt: now, UpdatedAt: now,
		}}},
		Runs: []Run{
			{ID: "run-1", AssistantID: "a", ResponsibilityID: "r1", State: RunFailed},
			{ID: "run-2", AssistantID: "a", ResponsibilityID: "r1", State: RunFailed},
		},
	}
	triggers := EvaluateExpansion(snapshot, now)
	if !hasTrigger(triggers, ExpansionRepeatedFailure) {
		t.Fatalf("triggers = %v, want repeated_failure", triggers)
	}
}
