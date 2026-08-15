package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/control"
	"workground2/internal/work"
)

func TestFindWorkArtifactRefRequiresExactIdentity(t *testing.T) {
	view := &work.WorkView{ArtifactSlots: []work.ArtifactSlot{{
		ID:            "budget",
		WorkID:        "work-1",
		DefinitionRev: 3,
		Revision:      2,
		ArtifactRefs: []work.ArtifactRef{{
			ID:     "file-1",
			Name:   "预算表.xlsx",
			Status: work.ArtifactRefStatusAvailable,
		}},
	}}}
	input := WorkArtifactFileIntent{
		WorkID:             "work-1",
		DefinitionRevision: 3,
		SlotID:             "budget",
		SlotRevision:       2,
		ArtifactRefID:      "file-1",
	}
	if _, ok := findWorkArtifactRef(view, input); !ok {
		t.Fatal("exact artifact identity was not found")
	}
	input.SlotRevision++
	if _, ok := findWorkArtifactRef(view, input); ok {
		t.Fatal("stale slot revision must not resolve")
	}
}

func TestMaterializeWorkArtifactBlobIsNamedIdempotentAndRepairable(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "works")
	store, err := work.NewFileWorkStore(workDir, 0)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "work-1"
	body := []byte("xlsx-bytes")
	digest, err := store.Put(workID, body)
	if err != nil {
		t.Fatal(err)
	}
	ref := work.ArtifactRef{
		ID:         "file-1",
		Name:       "预算表.xlsx",
		Type:       "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		Status:     work.ArtifactRefStatusAvailable,
		BlobDigest: digest,
	}
	path, err := materializeWorkArtifactBlob(workDir, workID, ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
	if filepath.Ext(path) != ".xlsx" {
		t.Fatalf("materialized extension=%q", filepath.Ext(path))
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("materialized content=%q err=%v", got, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	firstMod := info.ModTime()
	time.Sleep(10 * time.Millisecond)
	repeated, err := materializeWorkArtifactBlob(workDir, workID, ref)
	if err != nil {
		t.Fatal(err)
	}
	if repeated != path {
		t.Fatalf("repeated path=%q want %q", repeated, path)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.ModTime().Equal(firstMod) {
		t.Fatal("idempotent materialization rewrote an unchanged file")
	}

	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("modified"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeWorkArtifactBlob(workDir, workID, ref); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(path); err != nil || string(got) != string(body) {
		t.Fatalf("repaired content=%q err=%v", got, err)
	}
}

func TestSafeArtifactFileNameBlocksTraversal(t *testing.T) {
	got := safeArtifactFileName(`..\..\预算:表?.xlsx`, "xlsx")
	if got != "预算_表_.xlsx" {
		t.Fatalf("safe name=%q", got)
	}
	if filepath.Base(got) != got {
		t.Fatalf("safe name escaped base: %q", got)
	}
}

// urlArtifactAppHarness builds a real V2 Work with a url/link artifact slot
// and returns an App whose controller serves it.
func urlArtifactAppHarness(t *testing.T) (*App, WorkArtifactFileIntent, string) {
	t.Helper()
	root := t.TempDir()
	store, err := work.NewFileWorkStore(filepath.Join(t.TempDir(), "works"), 0)
	if err != nil {
		t.Fatal(err)
	}
	svc := work.NewService(store, nil, nil)
	ctx := context.Background()
	planning, err := svc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "url-artifact-session", RequestID: "url-artifact-plan",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := &work.WorkDefinitionRevision{
		WorkID: planning.Work.ID, Goal: "release a build", CreatedBy: "test",
		Nodes: []work.NodeDef{{ID: "n1", Title: "release"}},
		ArtifactSlots: []work.ArtifactSlotDef{{
			ID: "release-url", Title: "发布链接", Kind: "url",
			ExpectedCount: 1, Required: true,
		}},
	}
	candidate, err = svc.CreateCandidateRevision(ctx, planning.Work.ID, candidate, "url-artifact-candidate", planning.Revision)
	if err != nil {
		t.Fatal(err)
	}
	_, state, err := store.LoadState(planning.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.ApplyDefinition(ctx, work.ApplyDefinitionInput{
		WorkID: planning.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "url-artifact-apply",
	}); err != nil {
		t.Fatal(err)
	}
	_, state, err = store.LoadState(planning.Work.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	const refID = "release-ref"
	if _, err := svc.UpdateArtifactSlot(ctx, work.UpdateArtifactSlotInput{
		WorkID: planning.Work.ID, SlotID: "release-url", RequestID: "url-artifact-ref",
		State: work.SlotReady,
		Refs: []work.ArtifactRef{{
			ID: refID, Name: "https://release.example.com/v1.2.0",
			Type: "text/uri-list", Status: work.ArtifactRefStatusAvailable,
			URL: "https://release.example.com/v1.2.0",
		}},
		ExpectedRevision: state.Revision, DefinitionRev: candidate.Revision, Revision: 2,
	}); err != nil {
		t.Fatal(err)
	}
	// Unsafe schemes are rejected at the authoritative write seam.
	if _, err := svc.UpdateArtifactSlot(ctx, work.UpdateArtifactSlotInput{
		WorkID: planning.Work.ID, SlotID: "release-url", RequestID: "url-artifact-unsafe",
		State: work.SlotReady,
		Refs: []work.ArtifactRef{{
			ID: "unsafe-ref", Name: "javascript:alert(1)", Type: "text/uri-list",
			Status: work.ArtifactRefStatusAvailable, URL: "javascript:alert(1)",
		}},
		ExpectedRevision: state.Revision, DefinitionRev: candidate.Revision, Revision: 2,
	}); err == nil || !strings.Contains(err.Error(), "not an absolute http(s) URL") {
		t.Fatalf("unsafe URL write accepted: %v", err)
	}

	views := control.NewWorkViewBroadcaster()
	svc.SetV2TransportEnabled(true)
	ctrl := control.New(control.Options{Work: svc, WorkViews: views, WorkV2Enabled: true})
	app := &App{ctx: context.Background()}
	app.setTestCtrl(ctrl, "")
	app.tabs["test"].WorkspaceRoot = root
	intent := WorkArtifactFileIntent{
		WorkID:             planning.Work.ID,
		DefinitionRevision: candidate.Revision,
		SlotID:             "release-url",
		SlotRevision:       2,
		ArtifactRefID:      refID,
	}
	return app, intent, "https://release.example.com/v1.2.0"
}

func TestOpenWorkArtifactURLForTabResolvesAuthoritativeURL(t *testing.T) {
	app, intent, want := urlArtifactAppHarness(t)
	var opened []string
	old := openExternalBrowser
	openExternalBrowser = func(_ context.Context, url string) error {
		opened = append(opened, url)
		return nil
	}
	t.Cleanup(func() { openExternalBrowser = old })

	if err := app.OpenWorkArtifactURLForTab("test", intent); err != nil {
		t.Fatalf("open valid URL artifact: %v", err)
	}
	if len(opened) != 1 || opened[0] != want {
		t.Fatalf("opened = %v, want [%s]", opened, want)
	}
}

func TestOpenWorkArtifactURLForTabRejectsStaleOrFileIdentity(t *testing.T) {
	app, intent, _ := urlArtifactAppHarness(t)
	opened := 0
	old := openExternalBrowser
	openExternalBrowser = func(_ context.Context, _ string) error {
		opened++
		return nil
	}
	t.Cleanup(func() { openExternalBrowser = old })

	// A stale slot revision must never resolve — the URL is never trusted.
	stale := intent
	stale.SlotRevision++
	if err := app.OpenWorkArtifactURLForTab("test", stale); err == nil {
		t.Fatal("stale identity opened")
	}
	// A file-only ref (no URL) must fail explicitly without opening.
	fileIntent := intent
	fileIntent.ArtifactRefID = "file-only-ref"
	if err := app.OpenWorkArtifactURLForTab("test", fileIntent); err == nil {
		t.Fatal("missing ref identity opened")
	}
	if opened != 0 {
		t.Fatalf("browser opened %d times for rejected identities", opened)
	}
}
