package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	goruntime "runtime"
	"testing"
	"time"

	wailsruntime "github.com/wailsapp/wails/v2/pkg/runtime"

	"workground2/internal/control"
	"workground2/internal/work"
)

func TestRevealWorkspacePathRejectsTraversalOutsideAndSymlinkBeforeSideEffect(t *testing.T) {
	root := t.TempDir()
	inside := filepath.Join(root, "inside.txt")
	if err := os.WriteFile(inside, []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "outside.txt")
	if err := os.WriteFile(outside, []byte("no"), 0o600); err != nil {
		t.Fatal(err)
	}

	app := &App{ctx: context.Background()}
	app.setTestCtrl(control.New(control.Options{}), "")
	app.tabs["test"].WorkspaceRoot = root
	calls := 0
	oldReveal := revealPath
	revealPath = func(string) error {
		calls++
		return nil
	}
	t.Cleanup(func() { revealPath = oldReveal })

	for name, target := range map[string]string{
		"traversal":        filepath.Join("..", filepath.Base(outsideDir), "outside.txt"),
		"absolute-outside": outside,
	} {
		t.Run(name, func(t *testing.T) {
			if err := app.RevealWorkspacePathForTab("test", target); err == nil {
				t.Fatal("unsafe path was accepted")
			}
		})
	}

	t.Run("symlink-outside", func(t *testing.T) {
		link := filepath.Join(root, "escape")
		createTestDirectoryLink(t, link, outsideDir)
		if err := app.RevealWorkspacePathForTab("test", filepath.Join("escape", "outside.txt")); err == nil {
			t.Fatal("symlink escape was accepted")
		}
	})
	if calls != 0 {
		t.Fatalf("host reveal side effect ran %d times for rejected paths", calls)
	}
	if err := app.RevealWorkspacePathForTab("test", "inside.txt"); err != nil {
		t.Fatalf("safe workspace path rejected: %v", err)
	}
	if calls != 1 {
		t.Fatalf("safe reveal calls=%d, want 1", calls)
	}
}

func createTestDirectoryLink(t *testing.T, link, target string) {
	t.Helper()
	symlinkErr := os.Symlink(target, link)
	if symlinkErr != nil {
		if goruntime.GOOS != "windows" {
			t.Fatalf("create symlink evidence: %v", symlinkErr)
		}
		output, junctionErr := exec.Command(
			"cmd.exe", "/d", "/c", "mklink", "/J", link, target,
		).CombinedOutput()
		if junctionErr != nil {
			t.Fatalf(
				"create symlink evidence failed (%v); junction fallback failed (%v): %s",
				symlinkErr, junctionErr, output,
			)
		}
	}
	t.Cleanup(func() {
		if err := os.Remove(link); err != nil && !os.IsNotExist(err) {
			t.Errorf("remove test symlink/junction %q: %v", link, err)
		}
	})
}

func TestSelectWorkInputFileValidatesFullIdentityAndReturnsTypedArtifactRef(t *testing.T) {
	root := t.TempDir()
	selected := filepath.Join(root, "input.csv")
	if err := os.WriteFile(selected, []byte("a,b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := work.NewFileWorkStore(filepath.Join(t.TempDir(), "works"), 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := work.NewService(store, nil, nil)
	planning, err := svc.BeginWorkPlanning(context.Background(), work.BeginWorkPlanningInput{
		SessionID: "select-file", RequestID: "select-file-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := &work.WorkDefinitionRevision{
		WorkID: planning.Work.ID, Goal: "select file", CreatedBy: "test",
		Nodes: []work.NodeDef{{
			ID: "n1", Title: "file task", BlockIDs: []string{"b1"},
			InputSpecIDs: []string{"file-spec"},
		}},
		InputSpecs: []work.InputSpec{{
			ID: "file-spec", Label: "File", Kind: work.InputFile, Required: true,
		}},
	}
	candidate, err = svc.CreateCandidateRevision(
		context.Background(), planning.Work.ID, candidate, "select-file-candidate", planning.Revision,
	)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(planning.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := svc.ApplyDefinition(context.Background(), work.ApplyDefinitionInput{
		WorkID: planning.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "select-file-apply",
	})
	if err != nil {
		t.Fatal(err)
	}
	taskID, err := work.DeriveTaskID(applied.Intent.RunID, "n1")
	if err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(planning.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	input, err := work.NewInputService(store, nil).RequestInput(context.Background(), work.RequestInputRequest{
		WorkID: planning.Work.ID, RunID: applied.Intent.RunID, TaskID: taskID,
		BlockID: "b1", InputID: "file-input", SpecID: "file-spec",
		DefinitionRev: candidate.Revision, ExpectedRevision: state.Revision,
		RequestID: "select-file-input",
	})
	if err != nil {
		t.Fatal(err)
	}

	views := control.NewWorkViewBroadcaster()
	svc.SetV2TransportEnabled(true)
	ctrl := control.New(control.Options{Work: svc, WorkViews: views, WorkV2Enabled: true})
	app := &App{ctx: context.Background()}
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].WorkspaceRoot = root
	oldDialog := openWorkInputFileDialog
	dialogCalls := 0
	openWorkInputFileDialog = func(context.Context, wailsruntime.OpenDialogOptions) (string, error) {
		dialogCalls++
		return selected, nil
	}
	t.Cleanup(func() { openWorkInputFileDialog = oldDialog })

	request := SelectWorkInputFileRequest{
		WorkID: planning.Work.ID, RunID: input.RunID, TaskID: input.TaskID,
		BlockID: input.BlockID, InputID: input.ID, SpecID: input.SpecID,
	}
	bad := request
	bad.RunID = "historical-run"
	rejected, err := app.SelectWorkInputFile("test", bad)
	if err != nil || rejected == nil || rejected.Error == nil || dialogCalls != 0 {
		t.Fatalf("historical identity: result=%+v err=%v dialogCalls=%d", rejected, err, dialogCalls)
	}
	result, err := app.SelectWorkInputFile("test", request)
	if err != nil || result == nil || result.Error != nil || result.Canceled ||
		result.ArtifactRef == nil || result.ArtifactRef.RelativePath != "input.csv" ||
		result.ArtifactRef.Status != work.ArtifactRefStatusAvailable {
		t.Fatalf("selection result=%+v err=%v", result, err)
	}
	if dialogCalls != 1 {
		t.Fatalf("dialog calls=%d, want 1", dialogCalls)
	}
	droppedRequest := request
	droppedRequest.Path = selected
	dropped, err := app.SelectWorkInputFile("test", droppedRequest)
	if err != nil || dropped == nil || dropped.Error != nil || dropped.ArtifactRef == nil ||
		dropped.ArtifactRef.RelativePath != "input.csv" {
		t.Fatalf("dropped selection result=%+v err=%v", dropped, err)
	}
	if dialogCalls != 1 {
		t.Fatalf("native drop unexpectedly opened dialog: calls=%d", dialogCalls)
	}
	if result.ArtifactRef.LastVerifiedAt == nil ||
		time.Since(*result.ArtifactRef.LastVerifiedAt) > time.Minute {
		t.Fatalf("missing typed verification time: %+v", result.ArtifactRef)
	}
}
