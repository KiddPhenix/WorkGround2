package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// ── Five-scenario Blueprint fixture tests ────────────────────────────────────
//
// Production call chain:
//   BlueprintRegistry (lookup) → Service.BeginWorkPlanning (V2 Work creation)
//   → Service.CreateCandidateRevision (V2DefinitionTemplate)
//   → Service.ApplyDefinition (activation + run)
//   → V2Coordinator.ContinueDefinition → V2Scheduler → fixtureExecutor
//   → CommitV2Event → FileWorkStore (persistent projection)
//
// ── Test harness ────────────────────────────────────────────────────────────

type fixtureHarness struct {
	t      *testing.T
	store  *FileWorkStore
	svc    *Service
	exec   *fixtureExecutor
	workID string
	runID  string
	def    *WorkDefinitionRevision
}

func requireBlueprintIntegration(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("blueprint filesystem integration; run without -short")
	}
}

type blueprintPlanningController interface {
	BeginBlueprintPlanning(context.Context, BeginBlueprintPlanningInput) (*BeginBlueprintPlanningResult, error)
}

type fixtureExecutor struct {
	mu        sync.Mutex
	calls     []TaskExecuteInput
	failNodes map[string]bool
}

func newFixtureExecutor() *fixtureExecutor {
	return &fixtureExecutor{failNodes: make(map[string]bool)}
}

func (e *fixtureExecutor) ExecuteTask(ctx context.Context, input TaskExecuteInput) (*Attempt, error) {
	e.mu.Lock()
	e.calls = append(e.calls, input)
	e.mu.Unlock()
	nodeID := extractNodeID(input.TaskID)
	if e.failNodes[nodeID] {
		return &Attempt{State: RunFailed, Error: "injected failure for " + nodeID}, nil
	}
	result := &Attempt{
		State:                  RunCompleted,
		SessionRef:             SessionRef{SessionPath: "sessions/" + input.AttemptID + ".jsonl"},
		LastAssistantText:      "fixture delivery for " + input.Operation,
		SuccessfulCapabilities: append([]string(nil), input.RequiredCapabilities...),
	}
	if V2ReceiptRequired(input.SideEffectClass) {
		result.Receipt = &AttemptReceipt{
			RequestID:       input.RequestID,
			Operation:       input.Operation,
			Outcome:         "succeeded",
			SideEffectClass: input.SideEffectClass,
			ConfirmedAt:     time.Now().UTC(),
		}
	}
	return result, nil
}

func (e *fixtureExecutor) CancelTask(ctx context.Context, input TaskCancelInput) error { return nil }

func (e *fixtureExecutor) TaskArtifacts(_ context.Context, input TaskExecuteInput, _ *Attempt) ([]TaskArtifactOutput, error) {
	outputs := make([]TaskArtifactOutput, 0, len(input.ProducesSlotIDs))
	for _, slotID := range input.ProducesSlotIDs {
		slot := fixtureSlotDef(slotID)
		if slot == nil {
			return nil, fmt.Errorf("unknown fixture slot %q", slotID)
		}
		refs := make([]ArtifactRef, 0, slot.ExpectedCount)
		for i := 0; i < slot.ExpectedCount; i++ {
			refs = append(refs, ArtifactRef{
				ID:           fmt.Sprintf("%s-%d", slotID, i+1),
				Name:         fmt.Sprintf("%s-%d", slotID, i+1),
				Type:         slot.Kind,
				Status:       ArtifactRefStatusAvailable,
				RelativePath: fmt.Sprintf("artifacts/%s-%d", slotID, i+1),
				SourceRunID:  input.RunID,
			})
		}
		outputs = append(outputs, TaskArtifactOutput{
			SlotID: slotID, Refs: refs, Summary: "fixture output for " + input.Operation,
		})
	}
	return outputs, nil
}

func fixtureSlotDef(slotID string) *ArtifactSlotDef {
	for _, bpID := range V2BlueprintIDs() {
		def := V2DefinitionTemplate(bpID)
		for i := range def.ArtifactSlots {
			if def.ArtifactSlots[i].ID == slotID {
				slot := def.ArtifactSlots[i]
				return &slot
			}
		}
	}
	return nil
}

func (e *fixtureExecutor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

func (e *fixtureExecutor) nodeCallCount(nodeID string) int {
	count := 0
	for _, call := range e.callsSnapshot() {
		if extractNodeID(call.TaskID) == nodeID {
			count++
		}
	}
	return count
}

func (e *fixtureExecutor) callsSnapshot() []TaskExecuteInput {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]TaskExecuteInput, len(e.calls))
	copy(out, e.calls)
	return out
}

func extractNodeID(taskID string) string {
	idx := strings.Index(taskID, "/")
	if idx < 0 {
		return taskID
	}
	after := taskID[idx+1:]
	colonIdx := strings.Index(after, ":")
	if colonIdx < 0 {
		return after
	}
	return after[colonIdx+1:]
}

func newFixtureHarness(t *testing.T, bpID string) *fixtureHarness {
	t.Helper()
	store := newTestFileWorkStore(t)
	reg := NewBlueprintRegistry()
	svc := NewService(store, reg, nil)
	exec := newFixtureExecutor()
	svc.SetTaskExecutor(exec)

	var controller blueprintPlanningController = svc
	result, err := controller.BeginBlueprintPlanning(context.Background(), BeginBlueprintPlanningInput{
		BlueprintID: bpID,
		SessionID:   "session-" + t.Name(),
		RequestID:   "begin-blueprint-" + t.Name(),
	})
	if err != nil {
		t.Fatalf("BeginBlueprintPlanning: %v", err)
	}
	if result.BlueprintRef.ID != bpID {
		t.Fatalf("BlueprintRef.ID=%q, want %q", result.BlueprintRef.ID, bpID)
	}
	projection, err := store.LoadProjection(result.WorkID)
	if err != nil {
		t.Fatalf("LoadProjection: %v", err)
	}
	if projection.BlueprintRef != result.BlueprintRef {
		t.Fatalf("authoritative BlueprintRef=%+v, result=%+v", projection.BlueprintRef, result.BlueprintRef)
	}
	def, err := store.LoadRevision(result.WorkID, result.DefinitionRevision)
	if err != nil {
		t.Fatalf("LoadRevision: %v", err)
	}

	runID := ""
	if result.Apply != nil && result.Apply.Intent != nil {
		runID = result.Apply.Intent.RunID
	}
	if runID == "" {
		t.Fatal("BeginBlueprintPlanning returned no run intent")
	}
	return &fixtureHarness{t: t, store: store, svc: svc, exec: exec, workID: result.WorkID, runID: runID, def: def}
}

func (h *fixtureHarness) loadWork() *Work {
	proj, err := h.store.LoadProjection(h.workID)
	if err != nil {
		h.t.Fatalf("LoadProjection: %v", err)
	}
	return proj
}

func (h *fixtureHarness) submitInput(specID, value string, requestID string) *SubmitInputResult {
	_, state, err := h.store.LoadState(h.workID, "")
	if err != nil {
		h.t.Fatalf("LoadState: %v", err)
	}
	spec := findInputSpec(h.def.InputSpecs, specID)
	if spec == nil {
		h.t.Fatalf("InputSpec %q not found", specID)
	}
	taskIDs := findTasksForInputSpec(h.def.Nodes, specID)
	if len(taskIDs) == 0 {
		h.t.Fatalf("no task needs input spec %q", specID)
	}
	var input *WorkInput
	inputSvc := NewInputService(h.store, nil)
	for _, nodeID := range taskIDs {
		stableID, err := DeriveTaskID(h.runID, nodeID)
		if err != nil {
			h.t.Fatalf("DeriveTaskID: %v", err)
		}
		req, err := inputSvc.RequestInput(context.Background(), RequestInputRequest{
			WorkID: h.workID, RunID: h.runID, TaskID: stableID, BlockID: "block-" + specID,
			InputID: "input-" + specID + "-" + requestID, SpecID: specID,
			DefinitionRev: h.def.Revision, ExpectedRevision: state.Revision,
			RequestID: "request-input-" + specID + "-" + requestID,
		})
		if err != nil {
			h.t.Fatalf("RequestInput(%s): %v", specID, err)
		}
		input = req
		_, state, err = h.store.LoadState(h.workID, "")
		if err != nil {
			h.t.Fatalf("LoadState: %v", err)
		}
	}
	result, err := h.svc.SubmitV2Input(context.Background(), SubmitInputRequest{
		WorkID: h.workID, InputID: input.ID, Value: json.RawMessage(value),
		DefinitionRev: h.def.Revision, InputRevision: input.Revision, ExpectedRevision: state.Revision,
		RequestID: "submit-" + specID + "-" + requestID,
	})
	if err != nil {
		h.t.Fatalf("SubmitV2Input(%s): %v", specID, err)
	}
	return result
}

func findInputSpec(specs []InputSpec, id string) *InputSpec {
	for i := range specs {
		if specs[i].ID == id {
			return &specs[i]
		}
	}
	return nil
}
func findTasksForInputSpec(nodes []NodeDef, specID string) []string {
	var ids []string
	for _, n := range nodes {
		for _, sid := range n.InputSpecIDs {
			if sid == specID {
				ids = append(ids, n.ID)
				break
			}
		}
	}
	return ids
}
func defaultValueForSpec(spec InputSpec) string {
	if spec.DefaultValue != nil {
		return string(spec.DefaultValue)
	}
	switch spec.Kind {
	case InputText:
		return `"test-value"`
	case InputNumber:
		return `100`
	case InputDate:
		return `"2026-12-31"`
	case InputChoice, InputMultiChoice:
		if spec.ValueSchema != nil {
			var s struct {
				Options []struct {
					Value string `json:"value"`
				} `json:"options"`
			}
			if json.Unmarshal(spec.ValueSchema, &s) == nil && len(s.Options) > 0 {
				return fmt.Sprintf(`"%s"`, s.Options[0].Value)
			}
		}
		return `"default"`
	case InputRoster:
		return `[{"name":"张三","department":"技术部"}]`
	case InputFile:
		return `"fake-file-ref"`
	case InputForm:
		return `{}`
	case InputApproval:
		return `"approved"`
	default:
		return `"default"`
	}
}

// ── Test 1: All five scenarios execute through full production chain ────────

func TestFiveBlueprints_FullChainExecution(t *testing.T) {
	requireBlueprintIntegration(t)
	scenarios := []struct {
		bpID         string
		minNodes     int
		minSlots     int
		minInputs    int
		hasApproval  bool
		approvalNode string
	}{
		{"blueprint:image-compile", 4, 5, 4, false, ""},
		{"blueprint:script-writing", 6, 4, 4, false, ""},
		{"blueprint:financial-budget", 4, 4, 4, false, ""},
		{"blueprint:git-release", 5, 5, 5, true, "release-publish"},
		{"blueprint:annual-event", 5, 5, 5, false, ""},
	}
	for _, sc := range scenarios {
		t.Run(sc.bpID, func(t *testing.T) {
			h := newFixtureHarness(t, sc.bpID)
			proj := h.loadWork()
			if proj.SchemaVersion != SchemaVersionV2 {
				t.Errorf("SchemaVersion = %d, want %d", proj.SchemaVersion, SchemaVersionV2)
			}
			if len(h.def.Nodes) < sc.minNodes {
				t.Errorf("Nodes = %d < %d", len(h.def.Nodes), sc.minNodes)
			}
			if len(h.def.ArtifactSlots) < sc.minSlots {
				t.Errorf("Slots = %d < %d", len(h.def.ArtifactSlots), sc.minSlots)
			}
			if len(h.def.InputSpecs) < sc.minInputs {
				t.Errorf("Inputs = %d < %d", len(h.def.InputSpecs), sc.minInputs)
			}
			if sc.hasApproval {
				found := false
				for _, n := range h.def.Nodes {
					if n.ID == sc.approvalNode && n.GlobalGate != "" {
						found = true
					}
				}
				if !found {
					t.Errorf("expected GlobalGate on %q", sc.approvalNode)
				}
			}
			for _, spec := range h.def.InputSpecs {
				if spec.Kind == InputApproval || !spec.Required {
					continue
				}
				h.submitInput(spec.ID, defaultValueForSpec(spec), t.Name())
			}
			if h.exec.callCount() == 0 {
				t.Error("executor never called")
			}
			proj = h.loadWork()
			t.Logf("%s: calls=%d runtimes=%d", sc.bpID, h.exec.callCount(), len(proj.V2TaskRuntimes))
			for _, rt := range proj.V2TaskRuntimes {
				t.Logf("  %s=%s", rt.NodeID, rt.State)
				if rt.State != TaskCompleted {
					continue
				}
				node := findNodeDef(h.def.Nodes, rt.NodeID)
				for _, slotID := range node.ProducesSlotIDs {
					slot, _ := FindArtifactSlotRevision(proj, h.def.Revision, slotID)
					if slot == nil || slot.State != SlotReady || len(slot.ArtifactRefs) == 0 {
						t.Fatalf("completed node %s has no ready durable artifact in slot %s: %+v", rt.NodeID, slotID, slot)
					}
					if slot.ArtifactRefs[0].Type != slot.Kind {
						t.Fatalf("slot %s ref type=%q, want %q", slotID, slot.ArtifactRefs[0].Type, slot.Kind)
					}
				}
			}
		})
	}
}

// ── Test 2: Git release — approval gate blocks publishing ───────────────────

func TestGitRelease_ApprovalGateBlocksPublishing(t *testing.T) {
	requireBlueprintIntegration(t)
	h := newFixtureHarness(t, "blueprint:git-release")
	for _, spec := range h.def.InputSpecs {
		if spec.Kind == InputApproval || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), t.Name())
	}
	proj := h.loadWork()
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "release-publish" && rt.State == TaskCompleted {
			t.Fatal("release-publish completed without approval — violates zero-publish-before-approval invariant")
		}
	}
	found := false
	publishRisk := ""
	for _, spec := range h.def.InputSpecs {
		if spec.ID == "release-approval" {
			found = true
			if spec.Kind != InputApproval {
				t.Errorf("release-approval kind=%s want approval", spec.Kind)
			}
			if !spec.Required {
				t.Error("release-approval must be required")
			}
		}
	}
	if !found {
		t.Error("release-approval InputSpec not found")
	}
	for _, node := range h.def.Nodes {
		if node.ID == "release-publish" {
			publishRisk = DeriveV2SideEffectClass(node.ToolHints)
		}
	}
	if publishRisk != "external_write" || !V2ReceiptRequired(publishRisk) {
		t.Fatalf("release-publish risk=%q, want receipt-gated external_write", publishRisk)
	}
	// build-sign is workspace_write and should be able to complete without approval.
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "build-sign" && rt.State != TaskCompleted {
			t.Errorf("build-sign state=%s, expected completed", rt.State)
		}
	}

	h.submitInput("release-approval", `"rejected"`, t.Name()+"-rejected")
	if calls := h.exec.nodeCallCount("release-publish"); calls != 0 {
		t.Fatalf("rejected approval executed release-publish %d times", calls)
	}
	proj = h.loadWork()
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID != "release-publish" {
			continue
		}
		switch rt.State {
		case TaskWaitingInput, TaskWaitingApproval:
		default:
			t.Fatalf("release-publish state=%s after rejection, want waiting state", rt.State)
		}
	}

	store2, err := NewFileWorkStore(h.store.WorkDir(), -1)
	if err != nil {
		t.Fatalf("NewFileWorkStore after rejection: %v", err)
	}
	svc2 := NewService(store2, NewBlueprintRegistry(), nil)
	exec2 := newFixtureExecutor()
	svc2.SetTaskExecutor(exec2)
	if err := svc2.RecoverV2Scheduling(context.Background(), h.workID); err != nil {
		t.Fatalf("RecoverV2Scheduling after rejection: %v", err)
	}
	if calls := exec2.nodeCallCount("release-publish"); calls != 0 {
		t.Fatalf("restart after rejection executed release-publish %d times", calls)
	}
}

func TestGitRelease_ApprovedPublishesExactlyOnceWithReceipt(t *testing.T) {
	requireBlueprintIntegration(t)
	h := newFixtureHarness(t, "blueprint:git-release")
	for _, spec := range h.def.InputSpecs {
		if spec.Kind == InputApproval || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), t.Name())
	}
	h.submitInput("release-approval", `"approved"`, t.Name()+"-approved")
	if calls := h.exec.nodeCallCount("release-publish"); calls != 1 {
		t.Fatalf("approved publish calls=%d, want 1", calls)
	}
	projection := h.loadWork()
	taskID, _ := DeriveTaskID(h.runID, "release-publish")
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil || runtime.State != TaskCompleted || len(runtime.Attempts) != 1 {
		t.Fatalf("approved publish runtime=%+v", runtime)
	}
	attempt := runtime.Attempts[0]
	if attempt.Receipt == nil ||
		attempt.Receipt.RequestID != attempt.RequestID ||
		attempt.Receipt.Operation != "release-publish" ||
		attempt.Receipt.Outcome != "succeeded" {
		t.Fatalf("publish receipt does not bind request and operation: %+v", attempt.Receipt)
	}
	slot, _ := FindArtifactSlotRevision(projection, h.def.Revision, "release-version")
	if slot == nil || slot.State != SlotReady || len(slot.ArtifactRefs) != 1 {
		t.Fatalf("release-version slot=%+v", slot)
	}

	store2, err := NewFileWorkStore(h.store.WorkDir(), -1)
	if err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(store2, NewBlueprintRegistry(), nil)
	exec2 := newFixtureExecutor()
	svc2.SetTaskExecutor(exec2)
	if err := svc2.RecoverV2Scheduling(context.Background(), h.workID); err != nil {
		t.Fatalf("restart recovery: %v", err)
	}
	if got := h.exec.nodeCallCount("release-publish") + exec2.nodeCallCount("release-publish"); got != 1 {
		t.Fatalf("publish total after restart=%d, want 1", got)
	}
}

// ── Test 3: Annual event roster must be Block typed input ───────────────────

func TestAnnualEvent_RosterRequiredBlockInput(t *testing.T) {
	requireBlueprintIntegration(t)
	h := newFixtureHarness(t, "blueprint:annual-event")
	var rosterSpec *InputSpec
	for i := range h.def.InputSpecs {
		if h.def.InputSpecs[i].ID == "event-roster" {
			rosterSpec = &h.def.InputSpecs[i]
			break
		}
	}
	if rosterSpec == nil {
		t.Fatal("event-roster InputSpec not found")
	}
	if rosterSpec.Kind != InputRoster {
		t.Fatalf("event-roster kind=%s want roster", rosterSpec.Kind)
	}
	if !rosterSpec.Required {
		t.Fatal("event-roster must be required")
	}

	for _, spec := range h.def.InputSpecs {
		if spec.ID == "event-roster" || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), t.Name()+"-no-roster")
	}
	proj := h.loadWork()
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "roster-collect" {
			if rt.State != TaskWaitingInput {
				t.Errorf("roster-collect state=%s without roster; want waiting_input", rt.State)
			}
			hasRoster := false
			for _, wid := range rt.WaitingInputIDs {
				if wid == "event-roster" {
					hasRoster = true
				}
			}
			if !hasRoster {
				t.Error("roster-collect waiting but event-roster not in WaitingInputIDs")
			}
		}
	}
	h.submitInput("event-roster", `[{"name":"张三","department":"技术部"},{"name":"李四","department":"市场部"}]`, t.Name()+"-with-roster")
	proj = h.loadWork()
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "roster-collect" && rt.State == TaskWaitingInput {
			t.Error("roster-collect still waiting_input after roster submitted")
		}
	}
}

// ── Test 4: Office preview contract — no high-fidelity forgery ──────────────

func TestOfficePreviewContract_DeclaresGradedPreview(t *testing.T) {
	checks := []struct{ bpID, slotID, slotKind string }{
		{"blueprint:script-writing", "character-bios", "docx"},
		{"blueprint:script-writing", "final-script", "docx"},
		{"blueprint:script-writing", "scene-table", "docx"},
		{"blueprint:financial-budget", "budget-spreadsheet", "xlsx"},
		{"blueprint:financial-budget", "scenario-analysis", "xlsx"},
		{"blueprint:financial-budget", "budget-narrative", "docx"},
		{"blueprint:annual-event", "event-plan", "docx"},
		{"blueprint:annual-event", "program-list", "docx"},
		{"blueprint:annual-event", "event-budget", "xlsx"},
		{"blueprint:image-compile", "final-image", "image"},
		{"blueprint:git-release", "release-notes", "markdown"},
	}
	for _, c := range checks {
		t.Run(c.bpID+"/"+c.slotID, func(t *testing.T) {
			def := V2DefinitionTemplate(c.bpID)
			found := false
			for _, slot := range def.ArtifactSlots {
				if slot.ID == c.slotID {
					found = true
					if slot.Kind != c.slotKind {
						t.Errorf("kind=%q want %q", slot.Kind, c.slotKind)
					}
					if (slot.Kind == "docx" || slot.Kind == "xlsx") &&
						GradeArtifact("fixture."+slot.Kind, "") != PreviewFileCard {
						t.Errorf("%s must declare the file-card preview grade", slot.Kind)
					}
				}
			}
			if !found {
				t.Errorf("slot %q not found", c.slotID)
			}
		})
	}
	for _, bpID := range V2BlueprintIDs() {
		def := V2DefinitionTemplate(bpID)
		for _, slot := range def.ArtifactSlots {
			switch slot.Kind {
			case "rendered", "html", "webp", "svg":
				t.Errorf("%s slot %q kind=%q — must use docx/xlsx for Office", bpID, slot.ID, slot.Kind)
			}
		}
	}
}

// ── Test 5: Fixture offline deterministic — HARD FAIL on mismatch ───────────

func TestFiveBlueprints_DeterministicRepeatable(t *testing.T) {
	requireBlueprintIntegration(t)
	for _, bpID := range V2BlueprintIDs() {
		t.Run(bpID, func(t *testing.T) {
			h1 := newFixtureHarness(t, bpID)
			for _, spec := range h1.def.InputSpecs {
				if spec.Kind == InputApproval || !spec.Required {
					continue
				}
				h1.submitInput(spec.ID, defaultValueForSpec(spec), "run1")
			}
			c1 := h1.exec.callCount()
			h2 := newFixtureHarness(t, bpID)
			for _, spec := range h2.def.InputSpecs {
				if spec.Kind == InputApproval || !spec.Required {
					continue
				}
				h2.submitInput(spec.ID, defaultValueForSpec(spec), "run2")
			}
			c2 := h2.exec.callCount()
			if c1 != c2 {
				t.Fatalf("deterministic invariant violated: run1 calls=%d run2 calls=%d", c1, c2)
			}
			if c1 == 0 {
				t.Fatal("no tasks executed")
			}
			s1 := stableFixtureSummary(t, h1)
			s2 := stableFixtureSummary(t, h2)
			if s1 != s2 {
				t.Fatalf("stable Definition/Input/Artifact/approval/event summary differs:\nrun1=%s\nrun2=%s", s1, s2)
			}
		})
	}
}

func stableFixtureSummary(t *testing.T, h *fixtureHarness) string {
	t.Helper()
	def := *h.def
	def.WorkID, def.Digest, def.CreatedBy = "", "", ""
	def.CreatedAt = time.Time{}
	projection := h.loadWork()
	type slotSummary struct {
		ID       string
		Kind     string
		State    ArtifactSlotState
		RefTypes []string
	}
	type inputSummary struct {
		SpecID string
		Kind   InputKind
		State  InputState
		Value  string
	}
	type callSummary struct {
		Operation string
		Risk      string
		Slots     string
	}
	slots := make([]slotSummary, 0, len(projection.V2ArtifactSlots))
	for _, slot := range projection.V2ArtifactSlots {
		item := slotSummary{ID: slot.ID, Kind: slot.Kind, State: slot.State}
		for _, ref := range slot.ArtifactRefs {
			item.RefTypes = append(item.RefTypes, ref.Type)
		}
		sort.Strings(item.RefTypes)
		slots = append(slots, item)
	}
	sort.Slice(slots, func(i, j int) bool { return slots[i].ID < slots[j].ID })
	inputs := make([]inputSummary, 0, len(projection.V2Inputs))
	for _, input := range projection.V2Inputs {
		spec := findInputSpec(h.def.InputSpecs, input.SpecID)
		inputs = append(inputs, inputSummary{
			SpecID: input.SpecID, Kind: spec.Kind, State: input.State, Value: string(input.Value),
		})
	}
	sort.Slice(inputs, func(i, j int) bool {
		if inputs[i].SpecID == inputs[j].SpecID {
			return inputs[i].Value < inputs[j].Value
		}
		return inputs[i].SpecID < inputs[j].SpecID
	})
	calls := make([]callSummary, 0, h.exec.callCount())
	for _, call := range h.exec.callsSnapshot() {
		slots := append([]string(nil), call.ProducesSlotIDs...)
		sort.Strings(slots)
		calls = append(calls, callSummary{Operation: call.Operation, Risk: call.SideEffectClass, Slots: strings.Join(slots, ",")})
	}
	sort.Slice(calls, func(i, j int) bool { return calls[i].Operation < calls[j].Operation })
	workPath, err := h.store.workPath(h.workID)
	if err != nil {
		t.Fatal(err)
	}
	replay, _, err := ReplayWithReducer(workPath, DefaultReducer())
	if err != nil {
		t.Fatal(err)
	}
	eventCounts := make(map[WorkEventType]int)
	for _, event := range replay.Events {
		eventCounts[event.Type]++
	}
	summary := struct {
		Definition  WorkDefinitionRevision
		Slots       []slotSummary
		Inputs      []inputSummary
		Calls       []callSummary
		EventCounts map[WorkEventType]int
	}{
		Definition: def, Slots: slots, Inputs: inputs, Calls: calls, EventCounts: eventCounts,
	}
	raw, err := json.Marshal(summary)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// ── Test 6: Blueprint identity conflict — production intent conflict ───────

func TestFiveBlueprints_IdentityConflict(t *testing.T) {
	reg := NewBlueprintRegistry()
	for _, bpID := range V2BlueprintIDs() {
		if _, err := reg.LookupLatest(bpID); err != nil {
			t.Fatalf("LookupLatest(%s): %v", bpID, err)
		}
	}
	defs := make(map[string]*WorkDefinitionRevision)
	for _, bpID := range V2BlueprintIDs() {
		defs[bpID] = V2DefinitionTemplate(bpID)
	}
	// Each must have a distinct Goal (production intent).
	goals := make(map[string]string)
	for bpID, def := range defs {
		if existing, ok := goals[def.Goal]; ok {
			t.Errorf("duplicate Goal between %s and %s: %q", bpID, existing, def.Goal)
		}
		goals[def.Goal] = bpID
	}
	// Each must have unique internal IDs.
	for bpID, def := range defs {
		seenN, seenS, seenI := make(map[string]bool), make(map[string]bool), make(map[string]bool)
		for _, n := range def.Nodes {
			if seenN[n.ID] {
				t.Errorf("%s: dup node %q", bpID, n.ID)
			}
			seenN[n.ID] = true
		}
		for _, s := range def.ArtifactSlots {
			if seenS[s.ID] {
				t.Errorf("%s: dup slot %q", bpID, s.ID)
			}
			seenS[s.ID] = true
		}
		for _, s := range def.InputSpecs {
			if seenI[s.ID] {
				t.Errorf("%s: dup input %q", bpID, s.ID)
			}
			seenI[s.ID] = true
		}
	}
	// Duplicate registration must fail.
	if err := reg.Register(builtinImageCompile()); err == nil {
		t.Fatal("duplicate blueprint registration should fail")
	}
	// Production intent conflict: replaying the same typed planning request with
	// a different Blueprint must fail instead of replacing the persisted
	// Definition.
	store := newTestFileWorkStore(t)
	svc := NewService(store, reg, nil)
	svc.SetTaskExecutor(newFixtureExecutor())
	base := BeginBlueprintPlanningInput{
		BlueprintID: "blueprint:git-release",
		SessionID:   "identity-conflict",
		RequestID:   "identity-conflict",
	}
	if _, err := svc.BeginBlueprintPlanning(context.Background(), base); err != nil {
		t.Fatalf("first BeginBlueprintPlanning: %v", err)
	}
	reopened, err := NewFileWorkStore(store.WorkDir(), -1)
	if err != nil {
		t.Fatalf("NewFileWorkStore identity replay: %v", err)
	}
	restarted := NewService(reopened, NewBlueprintRegistry(), nil)
	restarted.SetTaskExecutor(newFixtureExecutor())
	base.BlueprintID = "blueprint:annual-event"
	if _, err := restarted.BeginBlueprintPlanning(context.Background(), base); err == nil {
		t.Fatal("same requestID with a different Blueprint unexpectedly succeeded")
	}
	projection, err := reopened.LoadProjection(workIDForRequest(blueprintChildRequestID(base.RequestID, "begin")))
	if err != nil {
		t.Fatal(err)
	}
	if projection.BlueprintRef.ID != "blueprint:git-release" {
		t.Fatalf("conflicting replay changed authoritative BlueprintRef to %q", projection.BlueprintRef.ID)
	}
}

func TestBeginBlueprintPlanning_IdempotentRestart(t *testing.T) {
	store1 := newTestFileWorkStore(t)
	svc1 := NewService(store1, NewBlueprintRegistry(), nil)
	exec1 := newFixtureExecutor()
	svc1.SetTaskExecutor(exec1)
	input := BeginBlueprintPlanningInput{
		BlueprintID: "blueprint:image-compile",
		SessionID:   "planning-restart",
		RequestID:   "planning-restart",
	}

	first, err := svc1.BeginBlueprintPlanning(context.Background(), input)
	if err != nil {
		t.Fatalf("first BeginBlueprintPlanning: %v", err)
	}
	replay, err := svc1.BeginBlueprintPlanning(context.Background(), input)
	if err != nil {
		t.Fatalf("same-instance replay: %v", err)
	}
	if replay.WorkID != first.WorkID || replay.DefinitionRevision != first.DefinitionRevision {
		t.Fatalf("same-instance replay changed identity: first=%s/%d replay=%s/%d",
			first.WorkID, first.DefinitionRevision, replay.WorkID, replay.DefinitionRevision)
	}
	if replay.Apply == nil || !replay.Apply.Duplicate {
		t.Fatal("same-instance replay did not report duplicate apply")
	}

	store2, err := NewFileWorkStore(store1.WorkDir(), -1)
	if err != nil {
		t.Fatalf("NewFileWorkStore store2: %v", err)
	}
	svc2 := NewService(store2, NewBlueprintRegistry(), nil)
	exec2 := newFixtureExecutor()
	svc2.SetTaskExecutor(exec2)
	restarted, err := svc2.BeginBlueprintPlanning(context.Background(), input)
	if err != nil {
		t.Fatalf("restart replay: %v", err)
	}
	if restarted.WorkID != first.WorkID || restarted.DefinitionRevision != first.DefinitionRevision {
		t.Fatalf("restart replay changed identity: first=%s/%d restart=%s/%d",
			first.WorkID, first.DefinitionRevision, restarted.WorkID, restarted.DefinitionRevision)
	}
	projection, err := store2.LoadProjection(first.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	if projection.BlueprintRef != first.BlueprintRef {
		t.Fatalf("restart BlueprintRef=%+v, want %+v", projection.BlueprintRef, first.BlueprintRef)
	}
	if calls := exec2.callCount(); calls != 0 {
		t.Fatalf("restart replay caused %d executor calls before inputs", calls)
	}
}

func TestBeginBlueprintPlanning_ConcurrentInstances(t *testing.T) {
	requireFileStoreIntegration(t)
	root := t.TempDir()
	store1, err := NewFileWorkStore(root, -1)
	if err != nil {
		t.Fatalf("NewFileWorkStore store1: %v", err)
	}
	store2, err := NewFileWorkStore(root, -1)
	if err != nil {
		t.Fatalf("NewFileWorkStore store2: %v", err)
	}
	svc1 := NewService(store1, NewBlueprintRegistry(), nil)
	svc2 := NewService(store2, NewBlueprintRegistry(), nil)
	exec1, exec2 := newFixtureExecutor(), newFixtureExecutor()
	svc1.SetTaskExecutor(exec1)
	svc2.SetTaskExecutor(exec2)
	input := BeginBlueprintPlanningInput{
		BlueprintID: "blueprint:financial-budget",
		SessionID:   "planning-concurrent",
		RequestID:   "planning-concurrent",
	}

	var first, second *BeginBlueprintPlanningResult
	var err1, err2 error
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		first, err1 = svc1.BeginBlueprintPlanning(context.Background(), input)
	}()
	go func() {
		defer wg.Done()
		second, err2 = svc2.BeginBlueprintPlanning(context.Background(), input)
	}()
	wg.Wait()

	// A loser may observe an optimistic revision conflict. Retrying the exact
	// same intent must converge on the winner's persisted receipts.
	if err1 != nil {
		first, err1 = svc1.BeginBlueprintPlanning(context.Background(), input)
	}
	if err2 != nil {
		second, err2 = svc2.BeginBlueprintPlanning(context.Background(), input)
	}
	if err1 != nil {
		t.Fatalf("store1 retry: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("store2 retry: %v", err2)
	}
	if first.WorkID != second.WorkID || first.DefinitionRevision != second.DefinitionRevision {
		t.Fatalf("instances diverged: store1=%s/%d store2=%s/%d",
			first.WorkID, first.DefinitionRevision, second.WorkID, second.DefinitionRevision)
	}
	if calls := exec1.callCount() + exec2.callCount(); calls != 0 {
		t.Fatalf("planning without inputs caused %d executor calls across instances", calls)
	}
}

// ── Test 7: Idempotent requestID — HARD FAIL on conflict ────────────────────

func TestFiveBlueprints_IdempotentRequestID(t *testing.T) {
	requireBlueprintIntegration(t)
	for _, bpID := range V2BlueprintIDs() {
		t.Run(bpID, func(t *testing.T) {
			h := newFixtureHarness(t, bpID)
			var ts *InputSpec
			for i := range h.def.InputSpecs {
				if h.def.InputSpecs[i].Kind != InputApproval && h.def.InputSpecs[i].Required {
					ts = &h.def.InputSpecs[i]
					break
				}
			}
			if ts == nil {
				t.Skip("no required non-approval spec")
			}
			val := defaultValueForSpec(*ts)
			_, state, err := h.store.LoadState(h.workID, "")
			if err != nil {
				t.Fatal(err)
			}
			inputSvc := NewInputService(h.store, nil)
			var input *WorkInput
			for _, nodeID := range findTasksForInputSpec(h.def.Nodes, ts.ID) {
				sid, _ := DeriveTaskID(h.runID, nodeID)
				req, err := inputSvc.RequestInput(context.Background(), RequestInputRequest{
					WorkID: h.workID, RunID: h.runID, TaskID: sid, BlockID: "bk-" + ts.ID,
					InputID: "in-" + ts.ID + "-idem", SpecID: ts.ID,
					DefinitionRev: h.def.Revision, ExpectedRevision: state.Revision,
					RequestID: "req-" + ts.ID + "-idem",
				})
				if err != nil {
					t.Fatal(err)
				}
				input = req
				_, state, err = h.store.LoadState(h.workID, "")
				if err != nil {
					t.Fatal(err)
				}
			}
			sr := SubmitInputRequest{
				WorkID: h.workID, InputID: input.ID, Value: json.RawMessage(val),
				DefinitionRev: h.def.Revision, InputRevision: input.Revision, ExpectedRevision: state.Revision,
				RequestID: "sub-" + ts.ID + "-idem",
			}
			r1, err := h.svc.SubmitV2Input(context.Background(), sr)
			if err != nil {
				t.Fatalf("first: %v", err)
			}
			if r1.Error != "" {
				t.Fatalf("first error: %s", r1.Error)
			}
			callsAfterFirst := h.exec.callCount()
			r2, err := h.svc.SubmitV2Input(context.Background(), sr)
			if err != nil {
				t.Fatalf("replay: %v", err)
			}
			if r2.Error != "" {
				t.Fatalf("replay error: %s", r2.Error)
			}
			if !r2.Duplicate {
				t.Fatal("replay did not report Duplicate")
			}
			if r2.Revision != r1.Revision {
				t.Fatalf("replay revision=%d, first revision=%d", r2.Revision, r1.Revision)
			}
			if calls := h.exec.callCount(); calls != callsAfterFirst {
				t.Fatalf("replay added executor calls: before=%d after=%d", callsAfterFirst, calls)
			}
		})
	}
}

// ── Test 8: Fake executor failure — HARD FAIL on retry error ─────────────────

func TestFiveBlueprints_ExecutorFailureSafeRetry(t *testing.T) {
	requireBlueprintIntegration(t)
	bpID := "blueprint:image-compile"
	h := newFixtureHarness(t, bpID)
	h.exec.failNodes["batch-crop"] = true
	for _, spec := range h.def.InputSpecs {
		if spec.Kind == InputApproval || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), t.Name())
	}
	proj := h.loadWork()
	var batchTaskID string
	for tid, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "batch-crop" {
			batchTaskID = tid
			if rt.State != TaskFailedRetryable {
				t.Fatalf("batch-crop state=%s, want failed_retryable", rt.State)
			}
		}
		if (rt.NodeID == "color-unify" || rt.NodeID == "export-compress") && (rt.State == TaskRunning || rt.State == TaskCompleted) {
			t.Fatalf("%s state=%s after upstream failure", rt.NodeID, rt.State)
		}
	}
	if batchTaskID == "" {
		t.Fatal("batch-crop runtime not found")
	}
	failedSlot, _ := FindArtifactSlotRevision(proj, h.def.Revision, "cropped-previews")
	if failedSlot == nil || failedSlot.State == SlotReady || len(failedSlot.ArtifactRefs) != 0 {
		t.Fatalf("failed executor produced a false-ready artifact slot: %+v", failedSlot)
	}
	h.exec.failNodes["batch-crop"] = false
	batchRT := proj.V2TaskRuntimes[batchTaskID]
	result, err := h.svc.RetryWorkNode(context.Background(), RetryWorkNodeRequest{
		WorkID: h.workID, RunID: batchRT.RunID, TaskID: batchTaskID, ExpectedRevision: batchRT.Revision, RequestID: "retry-" + t.Name(),
	})
	if err != nil {
		t.Fatalf("RetryWorkNode: %v", err)
	}
	if result == nil || result.Result == nil {
		t.Fatal("RetryWorkNode returned nil result")
	}
	if result.Result.State != RunCompleted {
		t.Fatalf("retry result state=%s, want completed", result.Result.State)
	}
	proj = h.loadWork()
	completed := make(map[string]bool)
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "batch-crop" && rt.State != TaskCompleted {
			t.Fatalf("batch-crop state=%s after retry, want completed", rt.State)
		}
		if rt.State == TaskCompleted {
			completed[rt.NodeID] = true
		}
	}
	if calls := h.exec.nodeCallCount("batch-crop"); calls != 2 {
		t.Fatalf("batch-crop executor calls=%d, want exactly 2 (failure + retry)", calls)
	}
	for _, nodeID := range []string{"color-unify", "export-compress"} {
		if !completed[nodeID] {
			t.Fatalf("%s did not complete after retry", nodeID)
		}
	}
}

// ── Test 9: Late roster — HARD ASSERT unrelated tasks unchanged ─────────────

func TestAnnualEvent_LateRosterNoCrossPollution(t *testing.T) {
	requireBlueprintIntegration(t)
	h := newFixtureHarness(t, "blueprint:annual-event")
	for _, spec := range h.def.InputSpecs {
		if spec.ID == "event-roster" || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), "early-"+t.Name())
	}
	beforeProj := h.loadWork()
	beforeCalls := h.exec.callCount()
	beforeVenueCalls := h.exec.nodeCallCount("venue-filter")
	beforeAgendaCalls := h.exec.nodeCallCount("agenda-arrange")
	// venue-filter and agenda-arrange must be completed before roster.
	for _, rt := range beforeProj.V2TaskRuntimes {
		if rt.NodeID == "venue-filter" && rt.State != TaskCompleted {
			t.Fatalf("venue-filter state=%s before roster, want completed", rt.State)
		}
		if rt.NodeID == "agenda-arrange" && rt.State != TaskCompleted {
			t.Fatalf("agenda-arrange state=%s before roster, want completed", rt.State)
		}
	}
	// Verify event-roster is only consumed by roster-collect.
	for _, c := range findTasksForInputSpec(h.def.Nodes, "event-roster") {
		if c != "roster-collect" {
			t.Errorf("event-roster consumed by %s", c)
		}
	}
	h.submitInput("event-roster", `[{"name":"王五","department":"财务部"}]`, "late-roster-"+t.Name())
	afterProj := h.loadWork()
	afterCalls := h.exec.callCount()
	// Late roster must NOT re-execute venue-filter or agenda-arrange.
	for _, rt := range afterProj.V2TaskRuntimes {
		if rt.NodeID == "venue-filter" && rt.State != TaskCompleted {
			t.Errorf("venue-filter regressed to %s after late roster", rt.State)
		}
		if rt.NodeID == "agenda-arrange" && rt.State != TaskCompleted {
			t.Errorf("agenda-arrange regressed to %s after late roster", rt.State)
		}
	}
	// Executor call count must increase only by roster-collect + dependents, NOT re-executing venue/agenda.
	// Net new calls = afterCalls - beforeCalls. roster-collect (1) + material-design (1) + vendor-coordinate (1) = at most 3.
	newCalls := afterCalls - beforeCalls
	if newCalls < 1 || newCalls > 3 {
		t.Fatalf("late roster added %d calls, want 1..3 (before=%d after=%d)", newCalls, beforeCalls, afterCalls)
	}
	if calls := h.exec.nodeCallCount("venue-filter"); calls != beforeVenueCalls {
		t.Fatalf("late roster re-executed venue-filter: before=%d after=%d", beforeVenueCalls, calls)
	}
	if calls := h.exec.nodeCallCount("agenda-arrange"); calls != beforeAgendaCalls {
		t.Fatalf("late roster re-executed agenda-arrange: before=%d after=%d", beforeAgendaCalls, calls)
	}
}

// ── Test 10: Restart recovery — HARD ASSERTs, no duplicates ─────────────────

func TestFiveBlueprints_RestartRecoveryTwoStores(t *testing.T) {
	requireBlueprintIntegration(t)
	bpID := "blueprint:image-compile"
	h1 := newFixtureHarness(t, bpID)
	for _, spec := range h1.def.InputSpecs {
		if spec.Kind == InputApproval || !spec.Required {
			continue
		}
		h1.submitInput(spec.ID, defaultValueForSpec(spec), "recover-"+t.Name())
	}
	proj1 := h1.loadWork()
	calls1 := h1.exec.callCount()
	if calls1 == 0 {
		t.Fatal("store1 executor never called")
	}
	t.Logf("Store1: calls=%d runtimes=%d", calls1, len(proj1.V2TaskRuntimes))

	store2, err := NewFileWorkStore(h1.store.WorkDir(), -1)
	if err != nil {
		t.Fatalf("store2 NewFileWorkStore: %v", err)
	}
	reg2 := NewBlueprintRegistry()
	svc2 := NewService(store2, reg2, nil)
	exec2 := newFixtureExecutor()
	svc2.SetTaskExecutor(exec2)

	proj2, err := store2.LoadProjection(h1.workID)
	if err != nil {
		t.Fatalf("store2 LoadProjection: %v", err)
	}
	if len(proj2.V2TaskRuntimes) != len(proj1.V2TaskRuntimes) {
		t.Fatalf("store2 runtimes=%d != store1 runtimes=%d", len(proj2.V2TaskRuntimes), len(proj1.V2TaskRuntimes))
	}
	for tid, rt1 := range proj1.V2TaskRuntimes {
		rt2 := proj2.V2TaskRuntimes[tid]
		if rt2 == nil {
			t.Fatalf("store2 missing task %q", tid)
		}
		if rt2.State != rt1.State {
			t.Fatalf("task %q state mismatch: store1=%s store2=%s", tid, rt1.State, rt2.State)
		}
	}

	err = svc2.RecoverV2Scheduling(context.Background(), h1.workID)
	if err != nil && !errors.Is(err, context.Canceled) {
		t.Fatalf("RecoverV2Scheduling: %v", err)
	}
	calls2 := exec2.callCount()
	calls1Snap := h1.exec.callCount()
	// Recovery must not re-execute completed tasks from store1's executor.
	if calls2 != 0 {
		t.Fatalf("store2 recovery re-executed %d completed tasks", calls2)
	}
	if calls1Snap != calls1 {
		t.Fatalf("store1 executor calls changed during store2 recovery: before=%d after=%d", calls1, calls1Snap)
	}
}

// ── Test 11: All inputs typed and referenced ────────────────────────────────

func TestFiveBlueprints_AllInputsTypedAndReferenced(t *testing.T) {
	validKinds := map[InputKind]bool{InputText: true, InputNumber: true, InputDate: true, InputChoice: true, InputMultiChoice: true, InputFile: true, InputRoster: true, InputForm: true, InputApproval: true}
	for _, bpID := range V2BlueprintIDs() {
		t.Run(bpID, func(t *testing.T) {
			def := V2DefinitionTemplate(bpID)
			for _, spec := range def.InputSpecs {
				if spec.ID == "" {
					t.Error("empty InputSpec ID")
				}
				if spec.Label == "" {
					t.Errorf("InputSpec %s empty Label", spec.ID)
				}
				if !validKinds[spec.Kind] {
					t.Errorf("InputSpec %s unknown kind %q", spec.ID, spec.Kind)
				}
			}
			specIDs := make(map[string]bool)
			for _, s := range def.InputSpecs {
				specIDs[s.ID] = true
			}
			for _, n := range def.Nodes {
				for _, sid := range n.InputSpecIDs {
					if !specIDs[sid] {
						t.Errorf("Node %s refs unknown InputSpec %q", n.ID, sid)
					}
				}
			}
			slotIDs := make(map[string]bool)
			for _, s := range def.ArtifactSlots {
				slotIDs[s.ID] = true
			}
			for _, n := range def.Nodes {
				for _, sid := range n.ProducesSlotIDs {
					if !slotIDs[sid] {
						t.Errorf("Node %s refs unknown Slot %q", n.ID, sid)
					}
				}
			}
			nodeIDs := make(map[string]bool)
			for _, n := range def.Nodes {
				nodeIDs[n.ID] = true
			}
			for _, n := range def.Nodes {
				for _, dep := range n.DependsOn {
					if !nodeIDs[dep] {
						t.Errorf("Node %s depends on unknown %q", n.ID, dep)
					}
				}
			}
		})
	}
}

// ── Test 12: At least one required slot per scenario ────────────────────────

func TestFiveBlueprints_AtLeastOneRequiredSlot(t *testing.T) {
	for _, bpID := range V2BlueprintIDs() {
		t.Run(bpID, func(t *testing.T) {
			def := V2DefinitionTemplate(bpID)
			has := false
			for _, slot := range def.ArtifactSlots {
				if slot.Required {
					has = true
				}
				if slot.Title == "" {
					t.Errorf("slot %s empty Title", slot.ID)
				}
			}
			if !has {
				t.Errorf("%s has no required slot", bpID)
			}
		})
	}
}

// ── Test 13: DAG no cycles ──────────────────────────────────────────────────

func TestFiveBlueprints_ValidDAGNoCycles(t *testing.T) {
	for _, bpID := range V2BlueprintIDs() {
		t.Run(bpID, func(t *testing.T) {
			if err := validateDAG(V2DefinitionTemplate(bpID).Nodes); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func validateDAG(nodes []NodeDef) error {
	ids := make(map[string]bool)
	for _, n := range nodes {
		if ids[n.ID] {
			return fmt.Errorf("dup %q", n.ID)
		}
		ids[n.ID] = true
	}
	for _, n := range nodes {
		for _, d := range n.DependsOn {
			if !ids[d] {
				return fmt.Errorf("%q→%q missing", n.ID, d)
			}
		}
	}
	inDeg := make(map[string]int, len(nodes))
	for _, n := range nodes {
		inDeg[n.ID] = 0
	}
	for _, n := range nodes {
		for range n.DependsOn {
			inDeg[n.ID]++
		}
	}
	q := make([]string, 0)
	for id, d := range inDeg {
		if d == 0 {
			q = append(q, id)
		}
	}
	sorted := 0
	for len(q) > 0 {
		id := q[0]
		q = q[1:]
		sorted++
		for _, n := range nodes {
			for _, d := range n.DependsOn {
				if d == id {
					inDeg[n.ID]--
					if inDeg[n.ID] == 0 {
						q = append(q, n.ID)
					}
				}
			}
		}
	}
	if sorted != len(nodes) {
		var rem []string
		for id, d := range inDeg {
			if d > 0 {
				rem = append(rem, id)
			}
		}
		sort.Strings(rem)
		return fmt.Errorf("cycle: %v", rem)
	}
	return nil
}

// ── Test 14: Listable ──────────────────────────────────────────────────────

func TestFiveBlueprints_Listable(t *testing.T) {
	bps := NewBlueprintRegistry().List()
	found := make(map[string]bool)
	for _, bp := range bps {
		found[bp.ID] = true
	}
	for _, bpID := range V2BlueprintIDs() {
		if !found[bpID] {
			t.Errorf("%q not listed", bpID)
		}
	}
	for _, bp := range bps {
		for _, wantID := range V2BlueprintIDs() {
			if bp.ID == wantID && bp.Source != BlueprintSystem {
				t.Errorf("%s source=%q", bp.ID, bp.Source)
			}
		}
	}
}

// ── Test 15: Idempotent Create ──────────────────────────────────────────────

func TestFiveBlueprints_IdempotentCreate(t *testing.T) {
	store := newTestFileWorkStore(t)
	svc := NewService(store, NewBlueprintRegistry(), nil)
	bp, _ := NewBlueprintRegistry().LookupLatest("blueprint:image-compile")
	reqID := "ic-" + t.Name()
	in := CreateWorkInput{BlueprintRef: BlueprintRef{ID: bp.ID, SchemaVersion: bp.SchemaVersion, Version: bp.Version}, Name: "T", RequestID: reqID}
	w1, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.Create(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if w1.ID != w2.ID {
		t.Fatalf("idempotent Create: %q != %q", w1.ID, w2.ID)
	}
}

// ── Test 16: GlobalGate blocks ─────────────────────────────────────────────

func TestGitRelease_GlobalGateBlocksNode(t *testing.T) {
	requireBlueprintIntegration(t)
	h := newFixtureHarness(t, "blueprint:git-release")
	for _, spec := range h.def.InputSpecs {
		if spec.Kind == InputApproval || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), "gate-"+t.Name())
	}
	proj := h.loadWork()
	gateFound := false
	for _, rt := range proj.V2TaskRuntimes {
		if rt.NodeID == "release-publish" {
			gateFound = true
			if rt.State == TaskCompleted {
				t.Fatal("GlobalGate node completed without approval")
			}
		}
	}
	if !gateFound {
		t.Error("release-publish runtime not found")
	}
}

// ── Test 17: Unknown blueprint returns nil ─────────────────────────────────

func TestV2DefinitionTemplate_UnknownReturnsNil(t *testing.T) {
	if V2DefinitionTemplate("blueprint:nonexistent") != nil {
		t.Error("expected nil")
	}
	if V2DefinitionTemplate("") != nil {
		t.Error("expected nil")
	}
}

// ── Test 18: Concurrent scheduling — two real stores, two executors, total call assertion ──

func TestFiveBlueprints_ConcurrentInstanceSafety(t *testing.T) {
	requireBlueprintIntegration(t)
	h := newFixtureHarness(t, "blueprint:git-release")
	for _, spec := range h.def.InputSpecs {
		if spec.Kind == InputApproval || !spec.Required {
			continue
		}
		h.submitInput(spec.ID, defaultValueForSpec(spec), "race-"+t.Name())
	}
	h.submitInput("release-approval", `"rejected"`, "race-rejected-"+t.Name())
	if calls := h.exec.nodeCallCount("release-publish"); calls != 0 {
		t.Fatalf("rejected setup published %d times", calls)
	}
	projection := h.loadWork()
	publishTaskID, _ := DeriveTaskID(h.runID, "release-publish")
	var approvalInput *WorkInput
	for i := range projection.V2Inputs {
		if projection.V2Inputs[i].TaskID == publishTaskID &&
			projection.V2Inputs[i].SpecID == "release-approval" {
			approvalInput = &projection.V2Inputs[i]
			break
		}
	}
	if approvalInput == nil {
		t.Fatal("approval input not found")
	}
	_, state, err := h.store.LoadState(h.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	submitted, err := NewInputService(h.store, nil).SubmitInput(context.Background(), SubmitInputRequest{
		WorkID: h.workID, InputID: approvalInput.ID, Value: json.RawMessage(`"approved"`),
		DefinitionRev: h.def.Revision, InputRevision: approvalInput.Revision,
		ExpectedRevision: state.Revision, RequestID: "race-approved-" + t.Name(),
	})
	if err != nil || submitted.Error != "" {
		t.Fatalf("submit approved without wake: result=%+v err=%v", submitted, err)
	}
	projection = h.loadWork()
	publish := projection.V2TaskRuntimes[publishTaskID]
	if publish == nil || (publish.State != TaskWaitingInput && publish.State != TaskWaitingApproval) {
		t.Fatalf("publish setup runtime=%+v", publish)
	}
	if err := updateRuntime(storeEventEmitter(h.store, h.workID), publish, TaskReady, nil, time.Now().UTC(), func(next *V2TaskRuntime) {
		next.WaitingInputIDs = nil
		next.ApprovalToken = ""
	}); err != nil {
		t.Fatalf("make side-effect node ready: %v", err)
	}

	store2, err := NewFileWorkStore(h.store.WorkDir(), -1)
	if err != nil {
		t.Fatalf("store2 NewFileWorkStore: %v", err)
	}
	svc2 := NewService(store2, NewBlueprintRegistry(), nil)
	exec2 := newFixtureExecutor()
	svc2.SetTaskExecutor(exec2)

	var wg sync.WaitGroup
	var err1, err2 error
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err1 = h.svc.ScheduleV2Run(context.Background(), h.workID, h.runID, nil)
	}()
	go func() {
		defer wg.Done()
		_, err2 = svc2.ScheduleV2Run(context.Background(), h.workID, h.runID, nil)
	}()
	wg.Wait()
	if err1 != nil {
		t.Fatalf("store1 concurrent schedule: %v", err1)
	}
	if err2 != nil {
		t.Fatalf("store2 concurrent schedule: %v", err2)
	}

	publishCalls := h.exec.nodeCallCount("release-publish") + exec2.nodeCallCount("release-publish")
	if publishCalls != 1 {
		projection := h.loadWork()
		runtimeDetails := make([]V2TaskRuntime, 0, len(projection.V2TaskRuntimes))
		for _, runtime := range projection.V2TaskRuntimes {
			runtimeDetails = append(runtimeDetails, *runtime)
		}
		t.Fatalf("cross-instance ready side effect executed %d times, want 1; calls1=%+v calls2=%+v runtimes=%+v",
			publishCalls, h.exec.callsSnapshot(), exec2.callsSnapshot(), struct {
				Runtimes []V2TaskRuntime
				Inputs   []WorkInput
			}{runtimeDetails, projection.V2Inputs})
	}
	postProj, err := h.store.LoadProjection(h.workID)
	if err != nil {
		t.Fatalf("post LoadProjection: %v", err)
	}
	publish = postProj.V2TaskRuntimes[publishTaskID]
	if publish == nil || publish.State != TaskCompleted || len(publish.Attempts) != 1 {
		t.Fatalf("durable publish runtime=%+v", publish)
	}
	if receipt := publish.Attempts[0].Receipt; receipt == nil ||
		receipt.RequestID != publish.Attempts[0].RequestID ||
		receipt.Operation != "release-publish" {
		t.Fatalf("durable publish receipt=%+v", receipt)
	}

	store3, err := NewFileWorkStore(h.store.WorkDir(), -1)
	if err != nil {
		t.Fatal(err)
	}
	svc3 := NewService(store3, NewBlueprintRegistry(), nil)
	exec3 := newFixtureExecutor()
	svc3.SetTaskExecutor(exec3)
	if err := svc3.RecoverV2Scheduling(context.Background(), h.workID); err != nil {
		t.Fatalf("third-store replay: %v", err)
	}
	if got := publishCalls + exec3.nodeCallCount("release-publish"); got != 1 {
		t.Fatalf("third-store replay publish total=%d, want 1", got)
	}
}
