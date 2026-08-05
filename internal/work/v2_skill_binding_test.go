package work

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// ── Skill binding event validation ─────────────────────────────────────────

func TestValidateNodeSkillBoundPayload(t *testing.T) {
	// Valid payload.
	valid := NodeSkillBoundPayload{
		WorkID:    "w1",
		NodeID:    "n1",
		SkillName: "my-skill",
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV2WorkEventPayload(EventNodeSkillBound, raw); err != nil {
		t.Fatalf("valid payload should pass: %v", err)
	}

	// Missing skillName — should fail validation.
	invalid := NodeSkillBoundPayload{WorkID: "w1", NodeID: "n1"}
	raw2, _ := json.Marshal(invalid)
	err2 := ValidateV2WorkEventPayload(EventNodeSkillBound, raw2)
	if err2 == nil {
		t.Fatal("expected error for missing skillName")
	}
	if !strings.Contains(err2.Error(), "skillName") {
		t.Fatalf("expected skillName error, got: %v", err2)
	}
}

func TestValidateNodeSkillClearedPayload(t *testing.T) {
	valid := NodeSkillClearedPayload{WorkID: "w1", NodeID: "n1"}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateV2WorkEventPayload(EventNodeSkillCleared, raw); err != nil {
		t.Fatalf("valid payload should pass: %v", err)
	}

	invalid := NodeSkillClearedPayload{WorkID: "", NodeID: "n1"}
	raw2, _ := json.Marshal(invalid)
	if err := ValidateV2WorkEventPayload(EventNodeSkillCleared, raw2); err == nil {
		t.Fatal("expected error for missing workId")
	}
}

func TestV2EventTypesIncludeSkillBinding(t *testing.T) {
	if !v2EventTypes[EventNodeSkillBound] {
		t.Fatal("EventNodeSkillBound not in v2EventTypes")
	}
	if !v2EventTypes[EventNodeSkillCleared] {
		t.Fatal("EventNodeSkillCleared not in v2EventTypes")
	}
	if !IsV2EventType(EventNodeSkillBound) {
		t.Fatal("IsV2EventType(EventNodeSkillBound) should be true")
	}
	if !knownWorkEventTypes[EventNodeSkillBound] {
		t.Fatal("EventNodeSkillBound not in knownWorkEventTypes")
	}
}

func TestV2NodeSkillContextRules(t *testing.T) {
	rule, ok := v2ContextRules[EventNodeSkillBound]
	if !ok {
		t.Fatal("missing context rule for EventNodeSkillBound")
	}
	if rule.Kind != ObjectNode {
		t.Fatalf("expected ObjectNode kind, got %s", rule.Kind)
	}
	if rule.PrimaryID != "nodeID" {
		t.Fatalf("expected nodeID primary, got %s", rule.PrimaryID)
	}
}

// ── Skill resolver interface ────────────────────────────────────────────────

type fakeSkillResolver struct {
	skills map[string]SkillBody
}

func (r *fakeSkillResolver) Resolve(ctx context.Context, name string) (SkillBody, error) {
	sk, ok := r.skills[name]
	if !ok {
		return SkillBody{}, context.DeadlineExceeded // dummy error
	}
	return sk, nil
}

func TestSkillResolverInterface(t *testing.T) {
	resolver := &fakeSkillResolver{
		skills: map[string]SkillBody{
			"test": {Name: "test", Description: "desc", Body: "body", Enabled: true},
		},
	}
	sk, err := resolver.Resolve(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	if sk.Name != "test" || sk.Body != "body" {
		t.Fatalf("unexpected skill: %+v", sk)
	}
	if !sk.Enabled {
		t.Fatal("expected enabled=true")
	}

	_, err = resolver.Resolve(context.Background(), "missing")
	if err == nil {
		t.Fatal("expected error for missing skill")
	}
}

// ── V2NodeSkillBindings projection ──────────────────────────────────────────

func TestWorkV2NodeSkillBindings(t *testing.T) {
	w := &Work{ID: "w1", SchemaVersion: SchemaVersionV2}
	if w.V2NodeSkillBindings != nil {
		t.Fatal("should be nil initially")
	}

	w.V2NodeSkillBindings = make(map[string]string)
	w.V2NodeSkillBindings["n1"] = "my-skill"
	if w.V2NodeSkillBindings["n1"] != "my-skill" {
		t.Fatal("binding not set")
	}

	delete(w.V2NodeSkillBindings, "n1")
	if len(w.V2NodeSkillBindings) > 0 {
		t.Fatal("map should be empty after clearing last binding")
	}
	// Nil it out as the coordinator does.
	w.V2NodeSkillBindings = nil
	if w.V2NodeSkillBindings != nil {
		t.Fatal("should be nil after explicit nil")
	}
}

// ── TaskV2View skillName ────────────────────────────────────────────────────

func TestTaskV2ViewSkillName(t *testing.T) {
	view := TaskV2View{
		ID:        "t1",
		NodeID:    "n1",
		State:     TaskPending,
		SkillName: "my-skill",
		UpdatedAt: time.Now(),
	}
	if view.SkillName != "my-skill" {
		t.Fatal("SkillName not preserved")
	}

	// Backward compat: empty SkillName is valid.
	view2 := TaskV2View{ID: "t2", UpdatedAt: time.Now()}
	if view2.SkillName != "" {
		t.Fatal("empty SkillName should be empty string, not omitted")
	}
}

// ── Scheduler skill injection ──────────────────────────────────────────────

func TestSchedulerSkillResolverPropagation(t *testing.T) {
	s := NewV2Scheduler(nil)
	if s.skillResolver != nil {
		t.Fatal("should be nil initially")
	}

	resolver := &fakeSkillResolver{skills: map[string]SkillBody{}}
	s.SetSkillResolver(resolver)
	if s.skillResolver == nil {
		t.Fatal("SetSkillResolver not propagated")
	}

	// Nil receiver safety.
	var nilSched *V2Scheduler
	nilSched.SetSkillResolver(resolver) // should not panic
}

// ── Transport result types ──────────────────────────────────────────────────

func TestSetNodeSkillResultTransport(t *testing.T) {
	r := SetNodeSkillResult{Revision: 5, Committed: true}
	raw, _ := json.Marshal(r)
	var back SetNodeSkillResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Revision != 5 || !back.Committed {
		t.Fatalf("round-trip failed: %+v", back)
	}
}

func TestClearNodeSkillResultTransport(t *testing.T) {
	r := ClearNodeSkillResult{Revision: 3, Duplicate: true, Committed: true}
	raw, _ := json.Marshal(r)
	var back ClearNodeSkillResult
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Duplicate || back.Revision != 3 {
		t.Fatalf("round-trip failed: %+v", back)
	}
}

func TestSkillInfoTransport(t *testing.T) {
	si := SkillInfo{Name: "s", Description: "d", Scope: "project", Enabled: true, RunAs: "inline"}
	raw, _ := json.Marshal(si)
	var back SkillInfo
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Name != "s" || back.Scope != "project" || !back.Enabled {
		t.Fatalf("round-trip failed: %+v", back)
	}
}

// ── ObjectContext NodeID ────────────────────────────────────────────────────

func TestObjectContextNodeID(t *testing.T) {
	ctx := ObjectContext{
		Kind:   ObjectNode,
		ID:     "node-1",
		WorkID: "w1",
		NodeID: "node-1",
	}
	if ctx.NodeID != "node-1" {
		t.Fatal("NodeID not set")
	}

	raw, _ := json.Marshal(ctx)
	var back ObjectContext
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.NodeID != "node-1" {
		t.Fatal("NodeID not round-tripped")
	}
}

// ── Skill name validation in CreateSkillRequest ─────────────────────────────

func TestCreateSkillRequestValidation(t *testing.T) {
	valid := CreateSkillRequest{
		Name: "my-skill", Description: "desc", Body: "body", Scope: "project", RequestID: "r1",
	}
	raw, _ := json.Marshal(valid)
	var back CreateSkillRequest
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back.Name != "my-skill" || back.Body != "body" {
		t.Fatal("round-trip failed")
	}

	// Empty name should still serialize (validation is at service level).
	empty := CreateSkillRequest{Name: "", RequestID: "r2"}
	raw2, _ := json.Marshal(empty)
	var back2 CreateSkillRequest
	if err := json.Unmarshal(raw2, &back2); err != nil {
		t.Fatal(err)
	}
	if back2.Name != "" {
		t.Fatal("expected empty name")
	}
}

// ── Backward compat: V1 Work has nil V2NodeSkillBindings ────────────────────

func TestV1WorkBackwardCompat(t *testing.T) {
	w := &Work{ID: "v1-work", SchemaVersion: 1}
	if w.V2NodeSkillBindings != nil {
		t.Fatal("V1 work should have nil V2NodeSkillBindings")
	}
	raw, _ := json.Marshal(w)
	if strings.Contains(string(raw), "v2NodeSkillBindings") {
		t.Fatal("V1 work JSON should not contain v2NodeSkillBindings")
	}
}

func TestV2SchedulerAppendsBoundSkill(t *testing.T) {
	exec := newFakeV2Executor()
	authority := newMemoryV2Authority("w-skill", nil)
	authority.projection.V2NodeSkillBindings = map[string]string{"write": "editor"}
	scheduler := NewV2Scheduler(exec)
	scheduler.SetSkillResolver(&fakeSkillResolver{skills: map[string]SkillBody{
		"editor": {Name: "editor", Description: "edit", Body: "USE THE EDITOR CHECKLIST", Enabled: true},
	}})

	if _, err := scheduler.Schedule(
		context.Background(), "w-skill", "r-skill",
		[]NodeDef{{ID: "write", Title: "Write draft"}}, nil, 1, nil, nil, nil, authority,
	); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 1 {
		t.Fatalf("executor calls = %d, want 1", len(exec.calls))
	}
	prompt := exec.calls[0].Prompt
	if !strings.Contains(prompt, "USE THE EDITOR CHECKLIST") || !strings.Contains(prompt, "Write draft") {
		t.Fatalf("bound Skill must augment the original prompt:\n%s", prompt)
	}
}

func TestV2SchedulerMissingBoundSkillFailsRetryableWithoutPanic(t *testing.T) {
	exec := newFakeV2Executor()
	authority := newMemoryV2Authority("w-missing-skill", nil)
	authority.projection.V2NodeSkillBindings = map[string]string{"write": "missing"}
	scheduler := NewV2Scheduler(exec)
	scheduler.SetSkillResolver(&fakeSkillResolver{skills: map[string]SkillBody{}})

	if _, err := scheduler.Schedule(
		context.Background(), "w-missing-skill", "r-missing-skill",
		[]NodeDef{{ID: "write", Title: "Write draft"}}, nil, 1, nil, nil, nil, authority,
	); err != nil {
		t.Fatal(err)
	}
	if len(exec.calls) != 0 {
		t.Fatalf("executor calls = %d, want 0", len(exec.calls))
	}
	taskID, err := DeriveTaskID("r-missing-skill", "write")
	if err != nil {
		t.Fatal(err)
	}
	runtime := authority.projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.State != TaskFailedRetryable || len(runtime.Attempts) != 1 {
		t.Fatalf("missing Skill runtime = %+v, want one retryable failed attempt", runtime)
	}
	if !strings.Contains(runtime.Error, "missing") {
		t.Fatalf("missing Skill error = %q", runtime.Error)
	}
}

func TestSetAndClearNodeSkillPersistAndReplayIdempotently(t *testing.T) {
	h := newCoordinatorHarness(t, coordinatorDefinition([]NodeDef{
		{ID: "write", Title: "Write", ProducesSlotIDs: []string{"slot"}},
		{ID: "review", Title: "Review", DependsOn: []string{"write"}},
	}, nil))
	_, state, err := h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	setInput := SetNodeSkillRequest{
		WorkID: h.work, NodeID: "write", SkillName: "editor",
		ExpectedRevision: state.Revision, RequestID: "bind-editor",
	}
	setResult, err := h.svc.SetNodeSkill(context.Background(), setInput)
	if err != nil || !setResult.Committed || setResult.Duplicate {
		t.Fatalf("SetNodeSkill = %+v, %v", setResult, err)
	}
	projection, err := h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if projection.V2NodeSkillBindings["write"] != "editor" {
		t.Fatalf("bindings = %+v", projection.V2NodeSkillBindings)
	}
	replay, err := h.svc.SetNodeSkill(context.Background(), setInput)
	if err != nil || !replay.Committed || !replay.Duplicate {
		t.Fatalf("SetNodeSkill replay = %+v, %v", replay, err)
	}
	conflict := setInput
	conflict.SkillName = "another"
	if _, err := h.svc.SetNodeSkill(context.Background(), conflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("SetNodeSkill conflict = %v", err)
	}

	_, state, err = h.store.LoadState(h.work, "")
	if err != nil {
		t.Fatal(err)
	}
	clearInput := ClearNodeSkillRequest{
		WorkID: h.work, NodeID: "write", ExpectedRevision: state.Revision, RequestID: "clear-editor",
	}
	clearResult, err := h.svc.ClearNodeSkill(context.Background(), clearInput)
	if err != nil || !clearResult.Committed || clearResult.Duplicate {
		t.Fatalf("ClearNodeSkill = %+v, %v", clearResult, err)
	}
	projection, err = h.store.LoadProjection(h.work)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := projection.V2NodeSkillBindings["write"]; ok {
		t.Fatalf("binding was not cleared: %+v", projection.V2NodeSkillBindings)
	}
	clearReplay, err := h.svc.ClearNodeSkill(context.Background(), clearInput)
	if err != nil || !clearReplay.Committed || !clearReplay.Duplicate {
		t.Fatalf("ClearNodeSkill replay = %+v, %v", clearReplay, err)
	}
	clearConflict := clearInput
	clearConflict.NodeID = "review"
	if _, err := h.svc.ClearNodeSkill(context.Background(), clearConflict); !errors.Is(err, ErrWorkRequestIDConflict) {
		t.Fatalf("ClearNodeSkill conflict = %v", err)
	}
}
