package assistant

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"workground2/internal/event"
)

// TestNewFlowEndToEnd exercises the converged supervisor flow across one store:
// WorkControl fence, supervisor cycle checkpoint, expansion trigger, single-turn
// reasoning, hard-gate + model-driven auto-answer, and late-result rejection.
func TestNewFlowEndToEnd(t *testing.T) {
	store := testStore(t, filepath.Join(t.TempDir(), "assistants"))
	mustCreate(t, store, "helper-a")
	now := testEpoch

	// WorkControl starts running.
	wc, _ := store.WorkControl()
	if wc.State != WorkRunning {
		t.Fatalf("initial work control = %+v", wc)
	}

	// Open + checkpoint a supervisor cycle.
	cycle, err := store.OpenCycle(OpenCycleInput{
		AssistantID: "helper-a", RequestID: "cycle-1",
		Observed: CycleObservation{PlanRevision: 1, AssistantRevision: 1, MemoryRevision: 1, WorkEpoch: wc.Epoch},
		Now:      now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.CheckpointCycle(CheckpointCycleInput{
		AssistantID: "helper-a", CycleID: cycle.ID, RequestID: "cp-1",
		Fence: cycle.Fence, NextStep: "advance scan", Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	// Empty plan triggers expansion.
	snapshot, _ := store.Get("helper-a")
	if triggers := EvaluateExpansion(snapshot, now); !hasExpansionTrigger(triggers, ExpansionPlanEmpty) {
		t.Fatalf("expansion triggers = %v, want plan_empty", triggers)
	}

	// Supervisor single-turn reasoning yields an advance decision. Reasoning now
	// runs through the supervisor Session's Controller (SupervisorExecutor); the
	// bounded decision vocabulary is extracted from the turn's final message.
	decision, err := ParseSupervisorDecision(`{"action":"advance","target":"scan","rationale":"scan is ready"}`)
	if err != nil || decision.Action != ActionAdvance || decision.Target != "scan" {
		t.Fatalf("supervisor decision = %+v err=%v", decision, err)
	}

	// Auto-answer: a hard gate is not auto-answered.
	gateDecision := RouteInteraction(RouteInteractionInput{Action: "answer_required", Prompt: "确认付款 99 元", Policy: Policy{Payment: AccessApprove}, Now: now})
	if gateDecision.Source != DecisionUser || gateDecision.HardGate != HardGateFundsLegalIdentity {
		t.Fatalf("gate decision = %+v", gateDecision)
	}

	// Auto-answer: an ordinary question is inferred.
	aa, _ := NewAutoAnswer(RoleModelFunc(func(_ context.Context, prompt string) (string, error) {
		return `["B"]`, nil
	}))
	answer, err := aa.InferAnswer(context.Background(), "grow the project", event.AskQuestion{
		ID: "q1", Prompt: "pick", Multi: false, Options: []event.AskOption{{Label: "A"}, {Label: "B"}},
	})
	if err != nil || len(answer.Selected) != 1 || answer.Selected[0] != "B" {
		t.Fatalf("auto answer = %+v err=%v", answer, err)
	}

	// Pause, then a late completion under the old epoch is rejected.
	if _, err := store.PauseAll("pause-1", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("pause-2", now); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeAll("resume-1", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteResume("resume-2", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	mustTrigger(t, store, "manual-1")
	run, ok, err := store.Claim("worker", now.Add(3*time.Second), time.Minute)
	if err != nil || !ok {
		t.Fatalf("claim after resume: ok=%v err=%v", ok, err)
	}
	if _, err := store.PauseAll("pause-3", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompletePause("pause-4", now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ResumeAll("resume-3", now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CompleteResume("resume-4", now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Finish(FinishInput{RequestID: "finish-late", RunID: run.ID, LeaseOwner: "worker", LeaseFence: run.LeaseFence, Now: now.Add(8 * time.Second)}); !errors.Is(err, ErrStaleFence) {
		t.Fatalf("late finish error = %v, want ErrStaleFence", err)
	}
}

func hasExpansionTrigger(triggers []ExpansionTrigger, want ExpansionTrigger) bool {
	for _, t := range triggers {
		if t == want {
			return true
		}
	}
	return false
}
