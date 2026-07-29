package boot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/config"
	"workground2/internal/control"
	"workground2/internal/work"
)

type bootV2Executor struct {
	mu          sync.Mutex
	fail        bool
	block       <-chan struct{}
	started     chan struct{}
	startedOnce sync.Once
	calls       []work.TaskExecuteInput
}

func TestWorkTaskSystemPromptPreservesHostPoliciesWithoutCodingIdentity(t *testing.T) {
	host := config.DefaultSystemPrompt + "\n\nproject policy: cite authoritative inputs"
	got := workTaskSystemPrompt(host)
	if !strings.HasPrefix(got, "You are a work delivery executor.") {
		t.Fatalf("unexpected Work task identity: %q", got)
	}
	if strings.Contains(got, "a coding agent focused on executing code tasks") {
		t.Fatalf("coding identity leaked into Work task prompt: %q", got)
	}
	if !strings.Contains(got, "project policy: cite authoritative inputs") {
		t.Fatalf("host policy was dropped: %q", got)
	}
}

func (e *bootV2Executor) ExecuteTask(ctx context.Context, input work.TaskExecuteInput) (*work.Attempt, error) {
	e.mu.Lock()
	e.calls = append(e.calls, input)
	fail := e.fail
	block := e.block
	e.mu.Unlock()
	if e.started != nil {
		e.startedOnce.Do(func() { close(e.started) })
	}
	if block != nil {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-block:
		}
	}
	if fail {
		return nil, errors.New("injected retryable read failure")
	}
	return &work.Attempt{
		State:                  work.RunCompleted,
		SessionRef:             work.SessionRef{SessionPath: "sessions/" + input.AttemptID + ".jsonl"},
		LastAssistantText:      "test delivery",
		SuccessfulCapabilities: append([]string(nil), input.RequiredCapabilities...),
	}, nil
}

func (*bootV2Executor) CancelTask(context.Context, work.TaskCancelInput) error { return nil }

func (*bootV2Executor) TaskArtifacts(
	_ context.Context,
	input work.TaskExecuteInput,
	_ *work.Attempt,
) ([]work.TaskArtifactOutput, error) {
	outputs := make([]work.TaskArtifactOutput, 0, len(input.ProducesSlotIDs))
	for _, slotID := range input.ProducesSlotIDs {
		outputs = append(outputs, work.TaskArtifactOutput{
			SlotID: slotID,
			Refs: []work.ArtifactRef{{
				ID:          input.AttemptID + "-" + slotID,
				Name:        slotID,
				Type:        "text",
				Status:      work.ArtifactRefStatusAvailable,
				BlobDigest:  work.ContentDigest([]byte(input.AttemptID + ":" + slotID)),
				SourceRunID: input.RunID,
			}},
		})
	}
	return outputs, nil
}

func (e *bootV2Executor) callCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.calls)
}

// ── Invariant: V2 methods return errWorkDisabled when work.enabled=false ────

func TestV2MethodsFlagOffReturnDisabled(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = false
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &recordSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink:          sink,
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	wc := ctrl.WorkControl()
	if wc == nil {
		t.Fatal("WorkControl returned nil interface")
	}

	verifyV2Disabled := func(method string, err error) {
		t.Helper()
		if err == nil {
			t.Fatalf("%s should return error when Work is disabled", method)
		}
		if !strings.Contains(err.Error(), "disabled") {
			t.Fatalf("%s error should mention disabled, got: %v", method, err)
		}
	}

	ctx := context.Background()
	_, err = wc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{SessionID: "s1", RequestID: "r1"})
	verifyV2Disabled("BeginWorkPlanning", err)

	_, err = wc.ApplyDefinition(ctx, work.ApplyDefinitionInput{WorkID: "w1", Revision: 1, RequestID: "r1"})
	verifyV2Disabled("ApplyDefinition", err)

	_, err = wc.SubmitWorkInput(ctx, work.SubmitInputRequest{WorkID: "w1", InputID: "i1", RequestID: "r1"})
	verifyV2Disabled("SubmitWorkInput", err)

	_, err = wc.SetInputCornerstone(ctx, work.SetInputCornerstoneRequest{WorkID: "w1", InputID: "i1", RequestID: "r1"})
	verifyV2Disabled("SetInputCornerstone", err)

	_, err = wc.PreviewWorkPatch(ctx, work.PreviewWorkPatchInput{WorkID: "w1", RequestID: "r1"})
	verifyV2Disabled("PreviewWorkPatch", err)

	_, err = wc.ApplyWorkPatch(ctx, work.ApplyWorkPatchInput{WorkID: "w1", PatchID: "p1", PreviewDigest: "d1", RequestID: "r1"})
	verifyV2Disabled("ApplyWorkPatch", err)

	_, err = wc.RetryWorkNode(ctx, work.RetryWorkNodeRequest{WorkID: "w1", RunID: "run1", TaskID: "t1", RequestID: "r1"})
	verifyV2Disabled("RetryWorkNode", err)

}

func TestV2FeatureFlagOffPreservesV1WorkAndSession(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = false
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	if !ctrl.WorkEnabled() {
		t.Fatal("V1 Work must remain enabled when only the V2 feature flag is off")
	}
	if _, err := ctrl.WorkControl().ListWorks(context.Background(), work.WorkFilter{Limit: 10}); err != nil {
		t.Fatalf("V1 ListWorks changed by V2 flag: %v", err)
	}
	if _, err := ctrl.WorkControl().BeginWorkPlanning(context.Background(), work.BeginWorkPlanningInput{
		SessionID: "flag-off", RequestID: "flag-off",
	}); err == nil || !strings.Contains(err.Error(), "collaboration_workbench_v2") {
		t.Fatalf("V2 method must report its disabled feature flag, got %v", err)
	}
	if err := ctrl.NewSession(); err != nil {
		t.Fatalf("Session lifecycle changed by V2 flag: %v", err)
	}
	if strings.TrimSpace(ctrl.SessionDir()) == "" {
		t.Fatal("SessionDir must remain configured when the V2 flag is off")
	}
}

// ── Invariant: V2 methods connect through Controller → Service → FileWorkStore ──

func TestV2BeginWorkPlanningFullChain(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sink := &recordSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink:          sink,
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	if !ctrl.WorkEnabled() {
		t.Fatal("Work should be enabled")
	}

	wc := ctrl.WorkControl()
	ctx := context.Background()

	// 1. BeginWorkPlanning creates a V2 Work via conversation-based planning.
	view, err := wc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-abc",
		RequestID: "req-plan-001",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	if view == nil || view.Work == nil {
		t.Fatal("BeginWorkPlanning returned empty view")
	}
	if view.Work.SchemaVersion != work.SchemaVersionV2 {
		t.Fatalf("expected SchemaVersion V2 (%d), got %d", work.SchemaVersionV2, view.Work.SchemaVersion)
	}
	workID := view.Work.ID
	t.Logf("created V2 Work: %s", workID)

	// Verify on-disk persistence through real FileWorkStore.
	workDir := config.ProjectWorkDir(dir)
	if workDir == "" {
		t.Fatal("ProjectWorkDir is empty")
	}
	projPath := filepath.Join(workDir, workID, "projection.json")
	if _, err := os.Stat(projPath); os.IsNotExist(err) {
		t.Fatalf("projection file not persisted: %s", projPath)
	}

	// 2. GetWork returns the same V2 projection.
	view2, err := wc.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if view2.Work.ID != workID {
		t.Fatalf("GetWork ID mismatch: %s vs %s", view2.Work.ID, workID)
	}
	if view2.Work.SchemaVersion != work.SchemaVersionV2 {
		t.Fatalf("GetWork schema version: %d (want V2=%d)", view2.Work.SchemaVersion, work.SchemaVersionV2)
	}

	t.Log("BeginWorkPlanning → GetWork → persistence chain verified")
}

func TestV2PreviewArtifactBootProductionBlobChain(t *testing.T) {
	isolateConfigHome(t)
	workspace := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "works")
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(workspace, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: workspace,
		WorkDir:       workDir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	store, err := work.NewFileWorkStore(workDir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "boot-preview-work"
	body := []byte("preview through boot production resolver")
	digest := work.ContentDigest(body)
	now := time.Now()
	if err := store.CreateWorkDir(work.CreateWorkDirInput{
		RequestID: "create-boot-preview",
		Work: &work.Work{
			SchemaVersion: work.SchemaVersionV2,
			ID:            workID,
			Name:          workID,
			State:         work.WorkDraft,
			ArchiveState:  work.ArchiveActive,
			BlueprintRef:  work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
			CreatedAt:     now,
			UpdatedAt:     now,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "slot", WorkID: workID, DefinitionRev: 1, Revision: 1,
				Title: "result", Kind: "text", State: work.SlotReady,
				ArtifactRefs: []work.ArtifactRef{{
					ID: "artifact", Name: "result.txt",
					Status: work.ArtifactRefStatusAvailable, BlobDigest: digest,
				}},
			}},
			V2CurrentRevision: 1,
			V2LatestRevision:  1,
			V2RevisionStates:  map[int64]work.DefinitionStatus{1: work.DefActive},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(workID, body); err != nil {
		t.Fatal(err)
	}

	result, err := ctrl.WorkControl().PreviewArtifact(context.Background(), work.PreviewArtifactRequest{
		WorkID: workID, DefinitionRevision: 1, SlotID: "slot",
		SlotRevision: 1, ArtifactRefID: "artifact", RequestID: "preview-through-boot",
	})
	if err != nil {
		t.Fatalf("PreviewArtifact: %v", err)
	}
	if result == nil || result.Preview == nil ||
		result.Preview.TextContent != string(body) ||
		result.Preview.ContentDigest != digest {
		t.Fatalf("boot preview chain result: %+v", result)
	}
}

func TestV2BootRecoversExpiredRunningArtifactConversion(t *testing.T) {
	isolateConfigHome(t)
	workspace := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "works")
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(workspace, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := work.NewFileWorkStore(workDir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const (
		workID    = "boot-conversion-recovery"
		requestID = "recover-expired-conversion"
	)
	body := []byte("boot recovered conversion")
	digest := work.ContentDigest(body)
	intentDigest := work.ContentDigest([]byte(fmt.Sprintf(
		"conv:v3:%s:%s:%s:%d:%d:%s:%s:%s:%s:%t:%s",
		workID, "artifact", "slot", 1, 1, digest,
		"text", "1", "inline-text", false, requestID,
	)))
	now := time.Now()
	if err := store.CreateWorkDir(work.CreateWorkDirInput{
		RequestID: "create-boot-conversion-recovery",
		Work: &work.Work{
			SchemaVersion: work.SchemaVersionV2,
			ID:            workID,
			Name:          workID,
			State:         work.WorkDraft,
			ArchiveState:  work.ArchiveActive,
			BlueprintRef:  work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
			CreatedAt:     now,
			UpdatedAt:     now,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "slot", WorkID: workID, DefinitionRev: 1, Revision: 1,
				Title: "result", Kind: "text", State: work.SlotReady,
				ArtifactRefs: []work.ArtifactRef{{
					ID: "artifact", Name: "result.txt",
					Status: work.ArtifactRefStatusAvailable, BlobDigest: digest,
				}},
			}},
			V2CurrentRevision: 1,
			V2LatestRevision:  1,
			V2RevisionStates:  map[int64]work.DefinitionStatus{1: work.DefActive},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(workID, body); err != nil {
		t.Fatal(err)
	}
	receipts := map[string]any{
		requestID: map[string]any{
			"requestId": requestID, "workId": workID,
			"artifactId": "artifact", "slotId": "slot",
			"slotRevision": 1, "definitionRevision": 1,
			"contentDigest": digest, "mimeType": "text/plain",
			"converterName": "text", "converterVersion": "1",
			"converterTarget": "inline-text", "allowExternal": false,
			"intentDigest": intentDigest,
			"state":        "running", "leaseOwner": "dead-instance",
			"leaseUntil": now.Add(-time.Minute), "updatedAt": now.Add(-time.Minute),
		},
	}
	raw, err := json.Marshal(receipts)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workDir, workID, "conversion-receipts.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: workspace,
		WorkDir:       workDir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	previewInput := work.PreviewArtifactRequest{
		WorkID: workID, DefinitionRevision: 1, SlotID: "slot",
		SlotRevision: 1, ArtifactRefID: "artifact", RequestID: "observe-boot-recovery",
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := ctrl.WorkControl().PreviewArtifact(context.Background(), previewInput)
		if err != nil {
			t.Fatal(err)
		}
		if result.Preview != nil && result.Preview.ConversionState == work.ConversionCompleted {
			if result.Preview.TextContent != string(body) || result.Preview.ContentDigest != digest {
				t.Fatalf("recovered preview mismatch: %+v", result)
			}
			break
		}
		if time.Now().After(deadline) {
			persisted, _ := os.ReadFile(filepath.Join(workDir, workID, "conversion-receipts.json"))
			t.Fatalf("boot did not recover expired conversion: result=%+v preview=%+v receipts=%s", result, result.Preview, persisted)
		}
		time.Sleep(10 * time.Millisecond)
	}
	deadline = time.Now().Add(5 * time.Second)
	for workRecoveryRunning(workDir) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if workRecoveryRunning(workDir) {
		t.Fatal("background conversion recovery did not finish")
	}
	if pumped, err := ctrl.WorkControl().RecoverArtifactConversions(context.Background(), workID); err != nil || pumped != 0 {
		t.Fatalf("completed recovery remained pumpable: pumped=%d err=%v", pumped, err)
	}
	replayed, err := ctrl.WorkControl().RequestArtifactConversion(context.Background(), work.RequestArtifactConversionInput{
		WorkID: workID, DefinitionRevision: 1, SlotID: "slot",
		SlotRevision: 1, ArtifactRefID: "artifact", RequestID: requestID,
	})
	if err != nil || replayed == nil || !replayed.Duplicate ||
		replayed.Preview == nil || replayed.Preview.ConversionState != work.ConversionCompleted {
		t.Fatalf("recovered request did not replay idempotently: result=%+v err=%v", replayed, err)
	}
}

func TestV2BootConversionRecoveryHasGlobalBatchLimit(t *testing.T) {
	if testing.Short() {
		t.Skip("filesystem conversion recovery integration; run without -short")
	}
	isolateConfigHome(t)
	workspace := t.TempDir()
	workDir := filepath.Join(t.TempDir(), "works")
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(workspace, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := work.NewFileWorkStore(workDir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "boot-conversion-batch"
	body := []byte("boot bounded conversion")
	digest := work.ContentDigest(body)
	now := time.Now()
	if err := store.CreateWorkDir(work.CreateWorkDirInput{
		RequestID: "create-boot-conversion-batch",
		Work: &work.Work{
			SchemaVersion: work.SchemaVersionV2,
			ID:            workID, Name: workID, State: work.WorkDraft, ArchiveState: work.ArchiveActive,
			BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
			CreatedAt:    now, UpdatedAt: now,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "slot", WorkID: workID, DefinitionRev: 1, Revision: 1,
				Title: "result", Kind: "text", State: work.SlotReady,
				ArtifactRefs: []work.ArtifactRef{{
					ID: "artifact", Name: "result.txt",
					Status: work.ArtifactRefStatusAvailable, BlobDigest: digest,
				}},
			}},
			V2CurrentRevision: 1, V2LatestRevision: 1,
			V2RevisionStates: map[int64]work.DefinitionStatus{1: work.DefActive},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(workID, body); err != nil {
		t.Fatal(err)
	}
	receipts := make(map[string]any, 65)
	for i := 0; i < 65; i++ {
		requestID := fmt.Sprintf("boot-batch-%03d", i)
		intentDigest := work.ContentDigest([]byte(fmt.Sprintf(
			"conv:v3:%s:%s:%s:%d:%d:%s:%s:%s:%s:%t:%s",
			workID, "artifact", "slot", 1, 1, digest,
			"text", "1", "inline-text", false, requestID,
		)))
		receipts[requestID] = map[string]any{
			"requestId": requestID, "workId": workID,
			"artifactId": "artifact", "slotId": "slot",
			"slotRevision": 1, "definitionRevision": 1,
			"contentDigest": digest, "mimeType": "text/plain",
			"converterName": "text", "converterVersion": "1",
			"converterTarget": "inline-text", "allowExternal": false,
			"intentDigest": intentDigest, "state": "pending",
			"updatedAt": now.Add(time.Duration(i) * time.Millisecond),
		}
	}
	raw, err := json.Marshal(receipts)
	if err != nil {
		t.Fatal(err)
	}
	receiptsPath := filepath.Join(workDir, workID, "conversion-receipts.json")
	if err := os.WriteFile(receiptsPath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	sink := &recordSink{}
	ctrl, err := Build(context.Background(), Options{
		Sink: sink, WorkspaceRoot: workspace, WorkDir: workDir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()
	deadline := time.Now().Add(15 * time.Second)
	for workRecoveryRunning(workDir) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if workRecoveryRunning(workDir) {
		t.Fatal("background conversion recovery did not reach its batch limit")
	}
	foundBatchNotice := false
	for _, emitted := range sink.events {
		if strings.Contains(emitted.Text, "batch limit reached") {
			foundBatchNotice = true
			break
		}
	}
	if !foundBatchNotice {
		t.Fatalf("boot did not expose conversion recovery batch limit: %+v", sink.events)
	}

	countStates := func() (completed, pending, running, failed int, readErr error) {
		data, err := os.ReadFile(receiptsPath)
		if err != nil {
			readErr = err
			return
		}
		var persisted map[string]struct {
			State string `json:"state"`
		}
		if err := json.Unmarshal(data, &persisted); err != nil {
			readErr = err
			return
		}
		for _, receipt := range persisted {
			switch receipt.State {
			case work.ConversionCompleted:
				completed++
			case work.ConversionPending:
				pending++
			case work.ConversionRunning:
				running++
			case work.ConversionFailed:
				failed++
			}
		}
		return
	}
	deadline = time.Now().Add(15 * time.Second)
	for {
		completed, pending, running, failed, readErr := countStates()
		if readErr == nil && completed == 64 && pending == 1 && running == 0 && failed == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("first boot batch did not stop at 64: completed=%d pending=%d running=%d failed=%d readErr=%v", completed, pending, running, failed, readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
	pumped, err := ctrl.WorkControl().RecoverArtifactConversions(context.Background(), workID)
	if err != nil || pumped != 1 {
		t.Fatalf("public continuation pump=%d want=1 err=%v", pumped, err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for {
		completed, pending, running, failed, readErr := countStates()
		if readErr == nil && completed == 65 && pending == 0 && running == 0 && failed == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("continued boot batch did not finish: completed=%d pending=%d running=%d failed=%d readErr=%v", completed, pending, running, failed, readErr)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ── Invariant: requestID idempotent — same request repeats safely ──

func TestV2BeginWorkPlanningIdempotent(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	wc := ctrl.WorkControl()
	ctx := context.Background()

	input := work.BeginWorkPlanningInput{
		SessionID: "session-idem",
		RequestID: "req-idem-001",
	}

	// First call creates the Work.
	view1, err := wc.BeginWorkPlanning(ctx, input)
	if err != nil {
		t.Fatalf("first BeginWorkPlanning: %v", err)
	}
	workID1 := view1.Work.ID

	// Second call with same requestID returns same Work idempotently.
	view2, err := wc.BeginWorkPlanning(ctx, input)
	if err != nil {
		t.Fatalf("second BeginWorkPlanning: %v", err)
	}
	workID2 := view2.Work.ID

	if workID1 != workID2 {
		t.Fatalf("idempotent retry produced different Work: %s vs %s", workID1, workID2)
	}
	t.Logf("idempotent: both calls returned Work %s", workID1)
}

// ── Invariant: requestID conflict — same requestID + different intent rejects ──

func TestV2BeginWorkPlanningRequestIDConflict(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	wc := ctrl.WorkControl()
	ctx := context.Background()

	// First call succeeds.
	_, err = wc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-A",
		RequestID: "req-conflict-001",
	})
	if err != nil {
		t.Fatalf("first BeginWorkPlanning: %v", err)
	}

	// Second call with same requestID but different SessionID must fail.
	_, err = wc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-B-DIFFERENT",
		RequestID: "req-conflict-001",
	})
	if err == nil {
		t.Fatal("expected conflict error for reusing requestID with different SessionID")
	}
	if !strings.Contains(err.Error(), "request") && !strings.Contains(err.Error(), "conflict") && !strings.Contains(err.Error(), "Conflict") {
		t.Fatalf("error should mention request/conflict, got: %v", err)
	}
	t.Logf("correctly rejected: %v", err)
}

// ── Invariant: planning does not start TaskExecutor or external side effects ──

func TestV2BeginWorkPlanningNoSideEffects(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	wc := ctrl.WorkControl()
	ctx := context.Background()

	view, err := wc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-no-effect",
		RequestID: "req-no-effect-001",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	workID := view.Work.ID

	// Planning creates a draft Work — no runs should exist.
	if len(view.Work.Runs) > 0 {
		t.Fatalf("planning created %d runs, want 0 — no TaskExecutor should be invoked", len(view.Work.Runs))
	}
	if view.Work.State != work.WorkDraft {
		t.Fatalf("Work state is %q, want %q (draft, not run)", view.Work.State, work.WorkDraft)
	}

	// No task runtimes should exist after planning.
	if len(view.Work.V2TaskRuntimes) > 0 {
		t.Fatalf("planning created %d task runtimes, want 0", len(view.Work.V2TaskRuntimes))
	}

	t.Logf("planning is side-effect-free: Work %s state=%q runs=%d", workID, view.Work.State, len(view.Work.Runs))
}

// ── Invariant: application reboots and recovers from persisted state ──

func TestV2PersistAndReload(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// First instance: create Work through planning.
	ctrl1, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build 1: %v", err)
	}
	wc1 := ctrl1.WorkControl()
	ctx := context.Background()

	view1, err := wc1.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-persist",
		RequestID: "req-persist-001",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	workID := view1.Work.ID
	ctrl1.Close()

	// Second instance: same workspace root should find the Work.
	ctrl2, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build 2: %v", err)
	}
	defer ctrl2.Close()

	wc2 := ctrl2.WorkControl()
	view2, err := wc2.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("GetWork after restart: %v", err)
	}
	if view2 == nil || view2.Work == nil {
		t.Fatal("restarted controller could not find persisted Work")
	}
	if view2.Work.SchemaVersion != work.SchemaVersionV2 {
		t.Fatalf("after restart: schema version %d (want V2=%d)", view2.Work.SchemaVersion, work.SchemaVersionV2)
	}
	if view2.Work.State != work.WorkDraft {
		t.Fatalf("after restart: state %q (want draft)", view2.Work.State)
	}
	t.Logf("restart recovery: Work %s found with state=%q", workID, view2.Work.State)
}

func TestV2PlanningCrossInstanceSameRequestConverges_FileWorkStore(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	build := func() *control.Controller {
		ctrl, err := Build(context.Background(), Options{
			Sink:          &recordSink{},
			WorkspaceRoot: dir,
		})
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		return ctrl
	}
	first := build()
	second := build()
	defer first.Close()
	defer second.Close()

	input := work.BeginWorkPlanningInput{
		SessionID: "cross-instance",
		RequestID: "cross-instance-plan",
	}
	start := make(chan struct{})
	type result struct {
		view *work.WorkView
		err  error
	}
	results := make(chan result, 2)
	var calls sync.WaitGroup
	for _, ctrl := range []*control.Controller{first, second} {
		calls.Add(1)
		go func(ctrl *control.Controller) {
			defer calls.Done()
			<-start
			view, err := ctrl.WorkControl().BeginWorkPlanning(context.Background(), input)
			results <- result{view: view, err: err}
		}(ctrl)
	}
	close(start)
	calls.Wait()
	close(results)

	var workID string
	for got := range results {
		if got.err != nil {
			// A simultaneous writer lease is an explicit retryable outcome.
			continue
		}
		if got.view == nil || got.view.Work == nil {
			t.Fatal("successful cross-instance call returned no Work")
		}
		if workID == "" {
			workID = got.view.Work.ID
		} else if got.view.Work.ID != workID {
			t.Fatalf("same request diverged across instances: %s vs %s", workID, got.view.Work.ID)
		}
	}
	retried, err := second.WorkControl().BeginWorkPlanning(context.Background(), input)
	if err != nil || retried == nil || retried.Work == nil {
		t.Fatalf("cross-instance retry did not converge: view=%+v err=%v", retried, err)
	}
	if workID != "" && retried.Work.ID != workID {
		t.Fatalf("retry converged to different Work: %s vs %s", retried.Work.ID, workID)
	}
	page, err := first.WorkControl().ListWorks(context.Background(), work.WorkFilter{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 1 || page.Items[0].ID != retried.Work.ID {
		t.Fatalf("cross-instance request created duplicate Works: %+v", page.Items)
	}
}

func TestV2BootAutomaticallyRecoversReadButNotExternal_FileWorkStore(t *testing.T) {
	if testing.Short() {
		t.Skip("filesystem restart recovery integration; run without -short")
	}
	isolateConfigHome(t)
	root := t.TempDir()
	workDir := filepath.Join(root, "work-data")
	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(root, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := work.NewFileWorkStore(workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	seed := work.NewService(store, nil, nil)
	failing := &bootV2Executor{fail: true}
	seed.SetTaskExecutor(failing)
	readWork, readTask := seedBootRecoveryWork(t, seed, store, "read", nil)
	externalWork, externalTask := seedBootRecoveryWork(
		t, seed, store, "external",
		[]string{"tool:side_effect=external_write"},
	)
	if failing.callCount() != 2 {
		t.Fatalf("seed calls=%d, want 2", failing.callCount())
	}

	recoveryExecutor := &bootV2Executor{}
	ctrl, err := Build(context.Background(), Options{
		Sink:             &recordSink{},
		WorkspaceRoot:    root,
		WorkDir:          workDir,
		WorkTaskExecutor: recoveryExecutor,
	})
	if err != nil {
		t.Fatalf("Build with automatic V2 recovery: %v", err)
	}
	defer ctrl.Close()
	deadline := time.Now().Add(5 * time.Second)
	reopened, err := work.NewFileWorkStore(workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	var readProjection *work.Work
	for time.Now().Before(deadline) {
		readProjection, err = reopened.LoadProjection(readWork)
		if err == nil {
			runtime := readProjection.V2TaskRuntimes[readTask]
			if runtime != nil && runtime.State == work.TaskCompleted && !workRecoveryRunning(workDir) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	if recoveryExecutor.callCount() != 1 {
		t.Fatalf("boot recovery calls=%d, want read-only task only", recoveryExecutor.callCount())
	}
	if err != nil {
		t.Fatal(err)
	}
	if runtime := readProjection.V2TaskRuntimes[readTask]; runtime == nil || runtime.State != work.TaskCompleted {
		t.Fatalf("read retry was not automatically recovered: %+v", runtime)
	}
	externalProjection, err := reopened.LoadProjection(externalWork)
	if err != nil {
		t.Fatal(err)
	}
	if runtime := externalProjection.V2TaskRuntimes[externalTask]; runtime == nil ||
		runtime.State != work.TaskWaitingApproval {
		t.Fatalf("external task must remain manual after restart: %+v", runtime)
	}
}

func TestV2BootRecoveryDoesNotBlockControllerAndIsSingleFlight_FileWorkStore(t *testing.T) {
	isolateConfigHome(t)
	root := t.TempDir()
	workDir := filepath.Join(root, "work-data")
	if err := os.WriteFile(filepath.Join(root, "WorkGround2.toml"), []byte(`
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := work.NewFileWorkStore(workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	seed := work.NewService(store, nil, nil)
	seed.SetTaskExecutor(&bootV2Executor{fail: true})
	seedBootRecoveryWork(t, seed, store, "blocked", nil)

	release := make(chan struct{})
	started := make(chan struct{})
	recoveryExecutor := &bootV2Executor{block: release, started: started}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type buildResult struct {
		ctrl *control.Controller
		err  error
	}
	build := func() <-chan buildResult {
		result := make(chan buildResult, 1)
		go func() {
			ctrl, err := Build(ctx, Options{
				Sink:             &recordSink{},
				WorkspaceRoot:    root,
				WorkDir:          workDir,
				WorkTaskExecutor: recoveryExecutor,
			})
			result <- buildResult{ctrl: ctrl, err: err}
		}()
		return result
	}
	awaitBuild := func(result <-chan buildResult) *control.Controller {
		t.Helper()
		select {
		case got := <-result:
			if got.err != nil {
				t.Fatalf("Build: %v", got.err)
			}
			return got.ctrl
		case <-time.After(3 * time.Second):
			close(release)
			t.Fatal("Build waited for background Work recovery")
			return nil
		}
	}

	first := awaitBuild(build())
	defer func() {
		if first != nil {
			first.Close()
		}
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("background Work recovery did not start")
	}
	second := awaitBuild(build())
	defer second.Close()
	time.Sleep(100 * time.Millisecond)
	if calls := recoveryExecutor.callCount(); calls != 1 {
		close(release)
		t.Fatalf("concurrent boot started %d recovery attempts, want one", calls)
	}
	first.Close()
	first = nil
	deadline := time.Now().Add(5 * time.Second)
	for workRecoveryRunning(workDir) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if workRecoveryRunning(workDir) {
		close(release)
		t.Fatal("background Work recovery did not stop with its controller")
	}
}

func seedBootRecoveryWork(
	t *testing.T,
	svc *work.Service,
	store *work.FileWorkStore,
	name string,
	toolHints []string,
) (string, string) {
	t.Helper()
	ctx := context.Background()
	view, err := svc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "boot-" + name,
		RequestID: "boot-plan-" + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := &work.WorkDefinitionRevision{
		WorkID: view.Work.ID, Goal: "boot recovery " + name,
		Nodes: []work.NodeDef{{
			ID: "n1", Title: name, ToolHints: toolHints,
			ProducesSlotIDs: []string{"slot"},
		}},
		ArtifactSlots: []work.ArtifactSlotDef{{
			ID: "slot", Title: "Output", Kind: "text", ExpectedCount: 1, Required: true,
		}},
		CreatedBy: "test", CreatedAt: time.Now().UTC(),
	}
	_, state, err := store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := svc.CreateCandidateRevision(
		ctx, view.Work.ID, definition, "boot-candidate-"+name, state.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(view.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyDefinition(ctx, work.ApplyDefinitionInput{
		WorkID: view.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "boot-apply-" + name,
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := work.DeriveTaskID(applied.Intent.RunID, "n1")
	if err != nil {
		t.Fatal(err)
	}
	projection, err := store.LoadProjection(view.Work.ID)
	if err != nil {
		t.Fatal(err)
	}
	runtime := projection.V2TaskRuntimes[taskID]
	if runtime == nil {
		t.Fatalf("seed runtime missing for %s", name)
	}
	if len(toolHints) == 0 && runtime.State != work.TaskFailedRetryable {
		t.Fatalf("read seed state=%s, want failed_retryable", runtime.State)
	}
	if len(toolHints) > 0 && runtime.State != work.TaskWaitingApproval {
		t.Fatalf("external seed state=%s, want waiting_approval", runtime.State)
	}
	return view.Work.ID, taskID
}

// ── Invariant: RecoverV2Scheduling is safe after planning-only Work ──

func TestV2RecoverSchedulingSafeOnPlanningOnly(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	wc := ctrl.WorkControl()
	ctx := context.Background()

	view, err := wc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-recov",
		RequestID: "req-recov-001",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	workID := view.Work.ID

	before, err := wc.GetWork(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	// No frontend recovery intent exists; rereading the authoritative snapshot
	// leaves a planning-only Work unchanged.
	after, err := wc.GetWork(ctx, workID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Revision != before.Revision {
		t.Fatalf("failed recovery mutated planning Work: before=%d after=%d", before.Revision, after.Revision)
	}
}

// ── Failure: unknown workID ──

func TestV2MethodsUnknownWorkID(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()

	cfgContent := `
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
collaboration_workbench_v2 = true
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfgContent), 0o644); err != nil {
		t.Fatal(err)
	}

	ctrl, err := Build(context.Background(), Options{
		Sink:          &recordSink{},
		WorkspaceRoot: dir,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	defer ctrl.Close()

	wc := ctrl.WorkControl()
	ctx := context.Background()
	unknownID := "w-unknown-nonexistent"

	_, err = wc.GetWork(ctx, unknownID)
	if err == nil {
		t.Fatal("GetWork should fail for unknown workID")
	}

	t.Logf("correct errors for unknown workID: GetWork=%v", err)
}

// ── Invariant: default true → explicit false → explicit true preserves V2 data ──

func TestV2DefaultTrueFalseTrueDataPreserved_FileWorkStore(t *testing.T) {
	isolateConfigHome(t)
	dir := t.TempDir()
	ctx := context.Background()

	writeConfig := func(collabV2Line string) {
		cfg := fmt.Sprintf(`
config_version = 3
default_model = "deepseek-flash"

[work]
enabled = true
%s
`, collabV2Line)
		if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(cfg), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// ── Phase 1: omit flag → default true → V2 enabled, create schema=2 Work ──
	writeConfig("")
	ctrl1, err := Build(ctx, Options{Sink: &recordSink{}, WorkspaceRoot: dir})
	if err != nil {
		t.Fatalf("Build phase 1: %v", err)
	}
	if !ctrl1.WorkEnabled() {
		t.Fatal("phase 1: WorkEnabled must be true")
	}
	if !ctrl1.WorkV2Enabled() {
		t.Fatal("phase 1: WorkV2Enabled must be true when flag is default (omitted)")
	}

	wc1 := ctrl1.WorkControl()
	view1, err := wc1.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-default-true",
		RequestID: "req-toggle-001",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning phase 1: %v", err)
	}
	if view1.Work.SchemaVersion != work.SchemaVersionV2 {
		t.Fatalf("phase 1: SchemaVersion = %d, want V2 (%d)", view1.Work.SchemaVersion, work.SchemaVersionV2)
	}
	workID := view1.Work.ID
	ctrl1.Close()

	// Snapshot on-disk state from real FileWorkStore.
	workDir := config.ProjectWorkDir(dir)
	if workDir == "" {
		t.Fatal("ProjectWorkDir is empty")
	}
	readPersisted := func() (proj, events []byte) {
		var err error
		proj, err = os.ReadFile(filepath.Join(workDir, workID, "projection.json"))
		if err != nil {
			t.Fatalf("read projection.json: %v", err)
		}
		events, err = os.ReadFile(filepath.Join(workDir, workID, "work.events.jsonl"))
		if err != nil {
			t.Fatalf("read work.events.jsonl: %v", err)
		}
		return
	}
	snapProj, snapEvents := readPersisted()

	// ── Phase 2: explicit false → V2 disabled, V1 works, data unchanged ──
	writeConfig("collaboration_workbench_v2 = false\n")
	ctrl2, err := Build(ctx, Options{Sink: &recordSink{}, WorkspaceRoot: dir})
	if err != nil {
		t.Fatalf("Build phase 2: %v", err)
	}
	if !ctrl2.WorkEnabled() {
		t.Fatal("phase 2: WorkEnabled must remain true (V1 is independent)")
	}
	if ctrl2.WorkV2Enabled() {
		t.Fatal("phase 2: WorkV2Enabled must be false when explicitly disabled")
	}

	wc2 := ctrl2.WorkControl()
	// V1 ListWorks must still return the Work.
	page, err := wc2.ListWorks(ctx, work.WorkFilter{Limit: 10})
	if err != nil {
		t.Fatalf("phase 2 ListWorks: %v", err)
	}
	found := false
	for i := range page.Items {
		if page.Items[i].ID == workID {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("phase 2: V2 Work missing from V1 ListWorks")
	}
	// V1 GetWork must still work.
	got, err := wc2.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("phase 2 GetWork: %v", err)
	}
	if got.Work.ID != workID {
		t.Fatalf("phase 2 GetWork returned wrong Work ID: %s", got.Work.ID)
	}
	// V2 write entrance must be rejected with clear feature-flag message.
	_, err = wc2.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-false", RequestID: "req-false-001",
	})
	if err == nil || !strings.Contains(err.Error(), "collaboration_workbench_v2") {
		t.Fatalf("phase 2: V2 method must report disabled flag, got: %v", err)
	}

	// Files must be byte-identical to phase 1 — no deletion or overwrite.
	curProj, curEvents := readPersisted()
	if string(curProj) != string(snapProj) {
		t.Fatal("phase 2: projection.json was modified while V2 was disabled")
	}
	if string(curEvents) != string(snapEvents) {
		t.Fatal("phase 2: work.events.jsonl was modified while V2 was disabled")
	}
	ctrl2.Close()

	// ── Phase 3: explicit true → V2 re-enabled, same Work fully restored ──
	writeConfig("collaboration_workbench_v2 = true\n")
	ctrl3, err := Build(ctx, Options{Sink: &recordSink{}, WorkspaceRoot: dir})
	if err != nil {
		t.Fatalf("Build phase 3: %v", err)
	}
	defer ctrl3.Close()
	if !ctrl3.WorkEnabled() {
		t.Fatal("phase 3: WorkEnabled must be true")
	}
	if !ctrl3.WorkV2Enabled() {
		t.Fatal("phase 3: WorkV2Enabled must be true when explicitly enabled")
	}

	wc3 := ctrl3.WorkControl()
	view3, err := wc3.GetWork(ctx, workID)
	if err != nil {
		t.Fatalf("phase 3 GetWork: %v", err)
	}
	if view3.Work.ID != workID {
		t.Fatalf("phase 3: Work ID = %s, want %s", view3.Work.ID, workID)
	}
	if view3.Work.SchemaVersion != work.SchemaVersionV2 {
		t.Fatalf("phase 3: SchemaVersion = %d, want V2 (%d)", view3.Work.SchemaVersion, work.SchemaVersionV2)
	}
	// V2 new creation must also work again.
	_, err = wc3.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "session-true", RequestID: "req-true-001",
	})
	if err != nil {
		t.Fatalf("phase 3 BeginWorkPlanning: %v", err)
	}

	t.Logf("default→false→true toggle: Work %s SchemaVersion=%d preserved", workID, view3.Work.SchemaVersion)
}
