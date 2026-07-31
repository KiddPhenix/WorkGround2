package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/control"
	"workground2/internal/work"
	"workground2/internal/work/worktest"
)

func TestBindWorkSessionPersistsIdempotencyAndWorkIdentity(t *testing.T) {
	sessionPath := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{SessionPath: sessionPath}
	app := &App{}

	if err := app.bindWorkSession(tab, "request-1", ""); err != nil {
		t.Fatalf("bind pending Work Session: %v", err)
	}
	if tab.sessionKind != agent.SessionKindWork || tab.workRequestID != "request-1" || tab.workID != "" {
		t.Fatalf("pending tab binding = kind %q, request %q, work %q", tab.sessionKind, tab.workRequestID, tab.workID)
	}

	if err := app.bindWorkSession(tab, "request-1", "work-1"); err != nil {
		t.Fatalf("bind committed Work Session: %v", err)
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok {
		t.Fatalf("LoadBranchMeta = (%+v, %v, %v)", meta, ok, err)
	}
	if meta.SessionKind != agent.SessionKindWork || meta.WorkRequestID != "request-1" || meta.WorkID != "work-1" {
		t.Fatalf("persisted binding = %+v", meta)
	}
	if err := agent.SaveBranchMetaPreserveUpdated(sessionPath, agent.BranchMeta{Preview: "updated elsewhere"}); err != nil {
		t.Fatalf("unrelated metadata update: %v", err)
	}
	meta, _, err = agent.LoadBranchMeta(sessionPath)
	if err != nil || meta.SessionKind != agent.SessionKindWork || meta.WorkRequestID != "request-1" || meta.WorkID != "work-1" {
		t.Fatalf("binding lost after unrelated metadata update: meta=%+v err=%v", meta, err)
	}

	infos, err := agent.ListSessionOrder(filepath.Dir(sessionPath))
	if err != nil || len(infos) != 1 {
		t.Fatalf("ListSessions = (%+v, %v)", infos, err)
	}
	if infos[0].SessionKind != agent.SessionKindWork || infos[0].WorkRequestID != "request-1" || infos[0].WorkID != "work-1" {
		t.Fatalf("listed binding = %+v", infos[0])
	}

	restored := &WorkspaceTab{
		ID:            "restored",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		SessionPath:   sessionPath,
	}
	restoredApp := &App{tabs: map[string]*WorkspaceTab{restored.ID: restored}}
	restoredApp.applySessionBindingToTab(restored, sessionBinding{
		scope:         "global",
		workspaceRoot: globalTabWorkspaceRoot(),
		path:          sessionPath,
	})
	if restored.sessionKind != agent.SessionKindWork || restored.workRequestID != "request-1" || restored.workID != "work-1" {
		t.Fatalf("restored binding = kind %q, request %q, work %q", restored.sessionKind, restored.workRequestID, restored.workID)
	}
}

func TestWorkSessionTabPromotesOnlyBlankOwnedSession(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspaceRoot := t.TempDir()
	sessionDir := desktopSessionDir(workspaceRoot)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	blankPath := filepath.Join(sessionDir, "blank.jsonl")
	if err := os.WriteFile(blankPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{
		ID:            "blank-tab",
		Scope:         "project",
		WorkspaceRoot: workspaceRoot,
		SessionPath:   blankPath,
		sessionKind:   agent.SessionKindNormal,
	}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}

	got, duplicate, err := app.workSessionTab(tab.ID, "project", normalizeProjectRoot(workspaceRoot), "request-1")
	if err != nil || duplicate || got != tab {
		t.Fatalf("blank Session selection = (%p, %v, %v), want original tab", got, duplicate, err)
	}

	if err := os.WriteFile(blankPath, []byte("not a session"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := app.workSessionTab(tab.ID, "project", normalizeProjectRoot(workspaceRoot), "request-1"); err == nil {
		t.Fatal("non-empty Session must not be promoted to Work")
	}
}

func TestWorkSessionTabRetryReusesSameBinding(t *testing.T) {
	tab := &WorkspaceTab{
		ID:            "work-tab",
		Scope:         "global",
		WorkspaceRoot: globalTabWorkspaceRoot(),
		sessionKind:   agent.SessionKindWork,
		workRequestID: "request-1",
	}
	app := &App{tabs: map[string]*WorkspaceTab{tab.ID: tab}}

	got, duplicate, err := app.workSessionTab(tab.ID, "global", "", "request-1")
	if err != nil || !duplicate || got != tab {
		t.Fatalf("same request retry = (%p, %v, %v), want duplicate original tab", got, duplicate, err)
	}
	if _, _, err := app.workSessionTab(tab.ID, "global", "", "request-2"); err == nil {
		t.Fatal("different request must not reuse an existing Work Session")
	}
}

func TestWorkSessionTitleStaysAutomaticAndSyncsIdempotently(t *testing.T) {
	isolateDesktopUserDirs(t)
	workspaceRoot := t.TempDir()
	sessionDir := desktopSessionDir(workspaceRoot)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "work-session.jsonl")
	if err := os.WriteFile(sessionPath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	tab := &WorkspaceTab{
		ID:            "work-tab",
		Scope:         "project",
		WorkspaceRoot: workspaceRoot,
		TopicID:       "work-topic",
		TopicTitle:    defaultTopicTitle,
		SessionPath:   sessionPath,
	}
	app := &App{
		tabs:                   map[string]*WorkspaceTab{tab.ID: tab},
		tabOrder:               []string{tab.ID},
		activeTabID:            tab.ID,
		projectTreeChangedHook: func() {},
	}
	if err := app.bindWorkSession(tab, "work-request", "work-1"); err != nil {
		t.Fatalf("bindWorkSession: %v", err)
	}
	if err := app.nameWorkSession(tab, workspaceRoot); err != nil {
		t.Fatalf("nameWorkSession: %v", err)
	}
	if got := loadTopicTitleSource(workspaceRoot, tab.TopicID); got != topicTitleSourceAuto {
		t.Fatalf("initial Work title source = %q, want auto", got)
	}

	const generated = "公司团建吃烤肉，KTV 周五晚上"
	tab.SessionPath = ""
	if err := app.syncWorkSessionTitle(tab.ID, "work-1", generated); err == nil {
		t.Fatal("missing session path must expose a retryable title sync failure")
	}
	tab.SessionPath = sessionPath
	if err := app.syncWorkSessionTitle(tab.ID, "work-1", generated); err != nil {
		t.Fatalf("retry syncWorkSessionTitle: %v", err)
	}
	if tab.TopicTitle != generated {
		t.Fatalf("open tab title = %q, want %q", tab.TopicTitle, generated)
	}
	if got := loadTopicTitle(workspaceRoot, tab.TopicID); got != generated {
		t.Fatalf("topic title = %q, want %q", got, generated)
	}
	if got := loadTopicTitleSource(workspaceRoot, tab.TopicID); got != topicTitleSourceAuto {
		t.Fatalf("synced Work title source = %q, want auto", got)
	}
	if got := loadSessionTitles(sessionDir)[filepath.Base(sessionPath)]; got != generated {
		t.Fatalf("session title = %q, want %q", got, generated)
	}
	meta, ok, err := agent.LoadBranchMeta(sessionPath)
	if err != nil || !ok || meta.TopicTitle != generated {
		t.Fatalf("branch meta = (%+v, %v, %v), want generated title", meta, ok, err)
	}

	if err := app.syncWorkSessionTitle(tab.ID, "work-1", generated); err != nil {
		t.Fatalf("idempotent title sync: %v", err)
	}
	if err := app.syncWorkSessionTitle(tab.ID, "other-work", generated); err == nil {
		t.Fatal("mismatched Work title sync must fail explicitly")
	}
}

func TestCreateWorkSessionRequestIDReusesOnlyTheSameIntent(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })

	first, err := app.CreateWorkSession(CreateWorkSessionInput{Scope: "global", RequestID: "request-1"})
	if err != nil {
		t.Fatalf("first CreateWorkSession: %v", err)
	}
	if first.TabMeta.ID == "" || first.TabMeta.SessionKind != string(agent.SessionKindWork) {
		t.Fatalf("first Work Session = %+v", first)
	}
	var listed *ProjectNode
	var findSession func([]ProjectNode)
	findSession = func(nodes []ProjectNode) {
		for i := range nodes {
			if nodes[i].SessionPath == first.TabMeta.SessionPath {
				listed = &nodes[i]
				return
			}
			findSession(nodes[i].Children)
			if listed != nil {
				return
			}
		}
	}
	findSession(app.ListProjectTree())
	if listed == nil || listed.Kind != "global_work_session" || listed.SessionKind != string(agent.SessionKindWork) {
		t.Fatalf("Work Session missing from project tree: node=%+v", listed)
	}

	retry, err := app.CreateWorkSession(CreateWorkSessionInput{Scope: "global", RequestID: "request-1"})
	if err != nil {
		t.Fatalf("retry CreateWorkSession: %v", err)
	}
	if retry.TabMeta.ID != first.TabMeta.ID || !retry.Duplicate {
		t.Fatalf("retry created another session: first=%q retry=%q duplicate=%v", first.TabMeta.ID, retry.TabMeta.ID, retry.Duplicate)
	}

	second, err := app.CreateWorkSession(CreateWorkSessionInput{Scope: "global", RequestID: "request-2"})
	if err != nil {
		t.Fatalf("second CreateWorkSession: %v", err)
	}
	if second.TabMeta.ID == "" || second.TabMeta.ID == first.TabMeta.ID {
		t.Fatalf("new intent reused old session: first=%q second=%q", first.TabMeta.ID, second.TabMeta.ID)
	}
}

func TestCreateWorkSessionPromotesRequestedBlankTabInPlace(t *testing.T) {
	isolateDesktopUserDirs(t)
	app := NewApp()
	t.Cleanup(func() { app.shutdown(context.Background()) })

	tab, err := app.ensureBlankBackgroundTab("global", "")
	if err != nil {
		t.Fatalf("ensure blank Session: %v", err)
	}
	result, err := app.CreateWorkSession(CreateWorkSessionInput{
		Scope:     "global",
		RequestID: "promote-request",
		TabID:     tab.ID,
	})
	if err != nil {
		t.Fatalf("CreateWorkSession: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("CreateWorkSession result error: %s", result.Error)
	}
	if result.TabMeta.ID != tab.ID {
		t.Fatalf("promoted tab = %q, want original %q", result.TabMeta.ID, tab.ID)
	}
	if result.TabMeta.SessionKind != string(agent.SessionKindWork) || result.TabMeta.WorkID == "" {
		t.Fatalf("promoted Work Session metadata = %+v", result.TabMeta)
	}
}

type repairBlobStore struct {
	content string
}

func (s *repairBlobStore) Put(_ string, data []byte) (string, error) {
	s.content = string(data)
	return work.ContentDigest(data), nil
}

func (*repairBlobStore) Get(string, string) ([]byte, error)   { return nil, work.ErrWorkNotFound }
func (*repairBlobStore) Exists(string, string) (bool, error)  { return false, nil }
func (*repairBlobStore) Delete(string, string) error          { return nil }
func (*repairBlobStore) ListDigests(string) ([]string, error) { return nil, nil }

func TestWorkViewWailsWatchRoutesOwningProjection(t *testing.T) {
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{
		Work:      work.NewService(&worktest.Store{}, work.NewBlueprintRegistry(), views),
		WorkViews: views,
	})
	emitted := make(chan work.WorkViewEvent, 1)
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"subscription-1" || len(payload) != 1 {
			return
		}
		if event, ok := payload[0].(work.WorkViewEvent); ok {
			emitted <- event
		}
	}
	app.setTestCtrl(ctrl, "test")
	if err := app.WatchWork("test", "work-1", "subscription-1"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-1") })

	views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "other-work", Revision: 1})
	views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "work-1", Revision: 2})
	select {
	case event := <-emitted:
		if event.WorkID != "work-1" || event.Revision != 2 {
			t.Fatalf("emitted event = %+v", event)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for WorkView Wails event")
	}

	app.UnwatchWork("subscription-1")
	app.UnwatchWork("subscription-1")
}

func TestWorkViewWailsWatchOverflowRecoversWithoutSuccessor(t *testing.T) {
	var loads atomic.Int64
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			loads.Add(1)
			return &work.Work{ID: workID, Name: "authoritative", State: work.WorkReady, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 77}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")

	recovered := make(chan work.WorkViewEvent, 1)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"subscription-overflow" || len(payload) != 1 {
			return
		}
		event, ok := payload[0].(work.WorkViewEvent)
		if !ok {
			return
		}
		if event.RequestID == "overflow-recovery" {
			select {
			case recovered <- event:
			default:
			}
		}
	}
	if err := app.WatchWork("test", "work-1", "subscription-overflow"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-overflow") })

	// A tight publisher burst outpaces the watcher and fills its fixed buffer.
	// Stop immediately after the first observed drop: the overflow signal must
	// recover without relying on any successor event.
	for i := 0; i < 100000 && views.OverflowCount() == 0; i++ {
		views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "work-1", EventID: fmt.Sprintf("burst-%d", i), Revision: int64(i + 1)})
	}
	if views.OverflowCount() == 0 {
		t.Fatal("burst did not exercise watcher overflow")
	}

	select {
	case event := <-recovered:
		if event.Revision != 77 || event.Resync == nil || event.Resync.Generation != 1 ||
			event.EventID != work.OverflowResyncEventID("work-1", 77, 1) {
			t.Fatalf("recovery event = %+v", event)
		}
		if err := event.Validate(); err != nil {
			t.Fatalf("recovery event contract: %v", err)
		}
		var snapshot work.WorkView
		if err := json.Unmarshal(event.Payload, &snapshot); err != nil || snapshot.Work == nil || snapshot.Work.Name != "authoritative" {
			t.Fatalf("recovery snapshot = %#v, err=%v", snapshot, err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("overflow with no successor did not trigger authoritative snapshot")
	}
	if loads.Load() == 0 || views.OverflowCount() == 0 {
		t.Fatalf("recovery evidence = loads %d, overflow %d", loads.Load(), views.OverflowCount())
	}
}

func TestWorkViewWailsOverflowResyncsExternalBlobAssessmentAtSameRevision(t *testing.T) {
	store, err := work.NewFileWorkStore(filepath.Join(t.TempDir(), "works"), 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	created, err := svc.Create(t.Context(), work.CreateWorkInput{
		BlueprintRef: work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: work.SchemaVersion, Version: 1},
		Name:         "same revision assessment", RequestID: "create-same-revision",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	beforePin, err := svc.Get(t.Context(), created.ID)
	if err != nil {
		t.Fatalf("Get before Pin: %v", err)
	}
	pinned, err := svc.PinCornerstone(t.Context(), created.ID, work.PinCornerstoneInput{
		Type: work.CornerstonePolicy, Title: "required blob",
		Content: strings.Repeat("authoritative external input ", 220),
		Ref:     work.CornerstoneRef{Kind: "inline"}, Mode: work.CornerstoneSnapshot, Required: true,
		ExpectedRevision: beforePin.Revision, RequestID: "pin-required-blob",
	})
	if err != nil || pinned == nil || pinned.Cornerstone == nil || pinned.Cornerstone.Ref.BlobDigest == "" {
		t.Fatalf("PinCornerstone = (%#v, %v)", pinned, err)
	}
	ready, err := svc.Get(t.Context(), created.ID)
	if err != nil || ready.Assessment.Blocking {
		t.Fatalf("ready projection = (%#v, %v)", ready, err)
	}
	if err := store.Delete(created.ID, pinned.Cornerstone.Ref.BlobDigest); err != nil {
		t.Fatalf("external blob delete: %v", err)
	}
	blocked, err := svc.Get(t.Context(), created.ID)
	if err != nil || blocked.Revision != ready.Revision || !blocked.Assessment.Blocking || blocked.RunBlock == nil {
		t.Fatalf("same-revision blocked projection = (%#v, %v), ready revision %d", blocked, err, ready.Revision)
	}

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")
	recovered := make(chan work.WorkViewEvent, 1)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"subscription-blob" || len(payload) != 1 {
			return
		}
		event, ok := payload[0].(work.WorkViewEvent)
		if ok && event.Resync != nil {
			select {
			case recovered <- event:
			default:
			}
		}
	}
	if err := app.WatchWork("test", created.ID, "subscription-blob"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-blob") })
	for i := 0; i < 100000 && views.OverflowCount() == 0; i++ {
		views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: created.ID, EventID: fmt.Sprintf("blob-burst-%d", i), Revision: int64(i + 1)})
	}
	if views.OverflowCount() == 0 {
		t.Fatal("burst did not exercise watcher overflow")
	}
	select {
	case event := <-recovered:
		var snapshot work.WorkView
		if err := json.Unmarshal(event.Payload, &snapshot); err != nil {
			t.Fatalf("decode recovery: %v", err)
		}
		if event.Revision != ready.Revision || snapshot.Revision != ready.Revision || !snapshot.Assessment.Blocking || snapshot.RunBlock == nil {
			t.Fatalf("overflow recovery did not carry same-revision blocked assessment: event=%+v snapshot=%#v", event, snapshot)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for same-revision authoritative resync")
	}
}

func TestWorkViewWailsOverflowGetFailureEmitsRetryableAttentionOnly(t *testing.T) {
	store := &worktest.Store{LoadStateFunc: func(string, string) (*work.Work, work.WorkEventState, error) {
		return nil, work.WorkEventState{}, errors.New("offline")
	}}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")
	events := make(chan work.WorkViewEvent, 4)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name == workViewEventPrefix+"subscription-failure" && len(payload) == 1 {
			if event, ok := payload[0].(work.WorkViewEvent); ok && (event.Type == work.ViewAttention || event.Resync != nil) {
				events <- event
			}
		}
	}
	if err := app.WatchWork("test", "work-1", "subscription-failure"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("subscription-failure") })
	for i := 0; i < 100000 && views.OverflowCount() == 0; i++ {
		views.EmitWorkView(work.WorkViewEvent{Type: work.ViewDelta, WorkID: "work-1", EventID: fmt.Sprintf("failure-burst-%d", i), Revision: int64(i + 1)})
	}
	select {
	case event := <-events:
		if event.Type != work.ViewAttention || event.Resync != nil || !strings.Contains(string(event.Payload), `"retryable":true`) {
			t.Fatalf("failure event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for retryable recovery attention")
	}
}

func TestRecoverWorkViewReturnsFreshTypedRetryResync(t *testing.T) {
	store := &worktest.Store{LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
		return &work.Work{
			ID: workID, Name: "same revision blocked", State: work.WorkReady, ArchiveState: work.ArchiveActive,
		}, work.WorkEventState{Revision: 77}, nil
	}}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")

	first, err := app.RecoverWorkView("test", "work-1", work.ViewRecoveryIntent{
		Reason: work.ViewResyncRetry, Generation: 8,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView first: %v", err)
	}
	second, err := app.RecoverWorkView("test", "work-1", work.ViewRecoveryIntent{
		Reason: work.ViewResyncRetry, Generation: 8,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView second: %v", err)
	}
	if first.Resync == nil || first.Resync.Reason != work.ViewResyncRetry || !first.Resync.Authoritative || first.Resync.Generation != 8 {
		t.Fatalf("first retry resync = %+v", first)
	}
	if second.Resync == nil || second.Resync.Generation != 9 || second.EventID == first.EventID {
		t.Fatalf("second retry resync = %+v; first EventID %q", second, first.EventID)
	}
	if first.EventID != work.ResyncEventID("work-1", 77, work.ViewResyncRetry, 8) ||
		second.EventID != work.ResyncEventID("work-1", 77, work.ViewResyncRetry, 9) {
		t.Fatalf("retry EventIDs = (%q, %q)", first.EventID, second.EventID)
	}
	if err := first.Validate(); err != nil {
		t.Fatalf("first retry contract: %v", err)
	}
	if err := second.Validate(); err != nil {
		t.Fatalf("second retry contract: %v", err)
	}
	var snapshot work.WorkView
	if err := json.Unmarshal(first.Payload, &snapshot); err != nil || snapshot.Revision != 77 || snapshot.Work == nil || snapshot.Work.Name != "same revision blocked" {
		t.Fatalf("retry snapshot = %#v, err=%v", snapshot, err)
	}
}

func TestV2WailsGetWatchRecoverUsesSchema2_FileWorkStore(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "works")
	store, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	svc.SetV2TransportEnabled(true)
	ctx := t.Context()
	planning, err := svc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "v2-wails", RequestID: "v2-wails-plan",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	definition := &work.WorkDefinitionRevision{
		WorkID: planning.Work.ID, Goal: "verify schema2 transport",
		Nodes: []work.NodeDef{{
			ID: "n1", Title: "read-only task", ProducesSlotIDs: []string{"slot"},
		}},
		ArtifactSlots: []work.ArtifactSlotDef{{
			ID: "slot", Title: "Output", Kind: "text", ExpectedCount: 1, Required: true,
		}},
		CreatedBy: "test", CreatedAt: time.Now().UTC(),
	}
	candidate, err := svc.CreateCandidateRevision(
		ctx, planning.Work.ID, definition, "v2-wails-candidate", planning.Revision,
	)
	if err != nil {
		t.Fatalf("CreateCandidateRevision: %v", err)
	}
	_, state, err := store.LoadState(planning.Work.ID, "")
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	ctrl := control.New(control.Options{
		Work: svc, WorkViews: views, WorkV2Enabled: true,
	})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")
	emitted := make(chan work.WorkViewEvent, 8)
	app.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"v2-wails-watch" || len(payload) != 1 {
			return
		}
		if event, ok := payload[0].(work.WorkViewEvent); ok {
			emitted <- event
		}
	}
	if err := app.WatchWork("test", planning.Work.ID, "v2-wails-watch"); err != nil {
		t.Fatalf("WatchWork: %v", err)
	}
	t.Cleanup(func() { app.UnwatchWork("v2-wails-watch") })

	if _, err := app.ApplyDefinition("test", work.ApplyDefinitionInput{
		WorkID: planning.Work.ID, Revision: candidate.Revision,
		ExpectedRevision: state.Revision, RequestID: "v2-wails-apply",
	}); err != nil {
		t.Fatalf("ApplyDefinition: %v", err)
	}
	select {
	case event := <-emitted:
		if event.SchemaVersion != work.WorkViewSchemaVersionV2 ||
			event.Type != work.ViewDelta ||
			event.Object.WorkID != planning.Work.ID {
			t.Fatalf("WatchWork event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("WatchWork did not receive schema2 production delta")
	}

	view, err := app.GetWork("test", planning.Work.ID)
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if view.SchemaVersion != work.WorkViewSchemaVersionV2 || view.Definition == nil ||
		len(view.ArtifactSlots) == 0 {
		t.Fatalf("GetWork schema2 projection = %+v", view)
	}
	recovered, err := app.RecoverWorkView("test", planning.Work.ID, work.ViewRecoveryIntent{
		Reason: work.ViewResyncRetry, Generation: 1,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView: %v", err)
	}
	if recovered.SchemaVersion != work.WorkViewSchemaVersionV2 ||
		recovered.Object.WorkID != planning.Work.ID {
		t.Fatalf("RecoverWorkView event = %+v", recovered)
	}
	var snapshot work.WorkView
	if err := json.Unmarshal(recovered.Payload, &snapshot); err != nil {
		t.Fatalf("RecoverWorkView payload: %v", err)
	}
	if snapshot.SchemaVersion != work.WorkViewSchemaVersionV2 ||
		snapshot.Definition == nil || len(snapshot.ArtifactSlots) == 0 {
		t.Fatalf("RecoverWorkView snapshot = %+v", snapshot)
	}

	app.UnwatchWork("v2-wails-watch")
	reopened, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart FileWorkStore: %v", err)
	}
	restartViews := control.NewWorkViewBroadcaster()
	restartService := work.NewService(reopened, work.NewBlueprintRegistry(), restartViews)
	restartService.SetV2TransportEnabled(true)
	restartCtrl := control.New(control.Options{
		Work: restartService, WorkViews: restartViews, WorkV2Enabled: true,
	})
	restartApp := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	restartApp.setTestCtrl(restartCtrl, "restart")
	removed := make(chan work.WorkViewEvent, 1)
	restartApp.runtimeEvents.emit = func(_ context.Context, name string, payload ...interface{}) {
		if name != workViewEventPrefix+"v2-wails-restart" || len(payload) != 1 {
			return
		}
		if event, ok := payload[0].(work.WorkViewEvent); ok && event.Type == work.ViewRemoved {
			removed <- event
		}
	}
	if err := restartApp.WatchWork("test", planning.Work.ID, "v2-wails-restart"); err != nil {
		t.Fatalf("restart WatchWork: %v", err)
	}
	t.Cleanup(func() { restartApp.UnwatchWork("v2-wails-restart") })
	if err := restartApp.DeleteWork("test", planning.Work.ID, "v2-wails-delete"); err != nil {
		t.Fatalf("DeleteWork: %v", err)
	}
	select {
	case event := <-removed:
		if event.SchemaVersion != work.WorkViewSchemaVersionV2 ||
			event.Object.WorkID != planning.Work.ID || event.RequestID != "v2-wails-delete" {
			t.Fatalf("removed event = %+v", event)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("restart WatchWork did not receive schema2 removed")
	}
}

// TestV2BlankDraftRestartGetAndRecover_FileWorkStore verifies that a blank
// draft Definition created via BeginWorkPlanning survives a full Desktop
// restart: a new FileWorkStore + Service + Controller + App pair re-reads the
// same blank draft through GetWork and RecoverWorkView.
func TestV2BlankDraftRestartGetAndRecover_FileWorkStore(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "works-blank-restart")
	store, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	svc.SetV2TransportEnabled(true)
	ctx := t.Context()

	// 1. BeginWorkPlanning → blank draft revision 1.
	planning, err := svc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "blank-restart", RequestID: "blank-restart-plan",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	// Verify blank draft body on disk.
	body, err := store.LoadRevision(planning.Work.ID, 1)
	if err != nil {
		t.Fatalf("LoadRevision: %v", err)
	}
	if body.Goal != "" || len(body.Nodes) != 0 || body.Status != work.DefDraft {
		t.Fatalf("blank draft body: goal=%q nodes=%d status=%s", body.Goal, len(body.Nodes), body.Status)
	}

	// 2. Restart: new FileWorkStore → Service → Controller → App.
	reopened, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart FileWorkStore: %v", err)
	}
	restartViews := control.NewWorkViewBroadcaster()
	restartService := work.NewService(reopened, work.NewBlueprintRegistry(), restartViews)
	restartService.SetV2TransportEnabled(true)
	restartCtrl := control.New(control.Options{
		Work: restartService, WorkViews: restartViews, WorkV2Enabled: true,
	})
	restartApp := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	restartApp.setTestCtrl(restartCtrl, "restart")

	// 2a. Restart GetWork must return blank draft with status=draft.
	view, err := restartApp.GetWork("test", planning.Work.ID)
	if err != nil {
		t.Fatalf("restart GetWork: %v", err)
	}
	if view.Definition == nil {
		t.Fatal("restart GetWork: definition is nil")
	}
	if view.Definition.Goal != "" || len(view.Definition.Nodes) != 0 {
		t.Fatalf("restart GetWork: goal=%q nodes=%d", view.Definition.Goal, len(view.Definition.Nodes))
	}
	if view.Definition.Status != work.DefDraft {
		t.Fatalf("restart GetWork: status=%s, want draft", view.Definition.Status)
	}
	if view.Definition.Revision != 1 {
		t.Fatalf("restart GetWork: revision=%d, want 1", view.Definition.Revision)
	}

	// 2b. Restart RecoverWorkView must return a snapshot event whose payload
	// contains the same blank draft.
	recovered, err := restartApp.RecoverWorkView("test", planning.Work.ID, work.ViewRecoveryIntent{
		Reason: work.ViewResyncRetry, Generation: 1,
	})
	if err != nil {
		t.Fatalf("restart RecoverWorkView: %v", err)
	}
	if recovered.Type != work.ViewSnapshot || recovered.Object.WorkID != planning.Work.ID {
		t.Fatalf("restart RecoverWorkView event: type=%s workID=%s", recovered.Type, recovered.Object.WorkID)
	}
	var snap work.WorkView
	if err := json.Unmarshal(recovered.Payload, &snap); err != nil {
		t.Fatalf("restart RecoverWorkView payload: %v", err)
	}
	if snap.Definition == nil {
		t.Fatal("restart RecoverWorkView: snapshot definition is nil")
	}
	if snap.Definition.Goal != "" || len(snap.Definition.Nodes) != 0 {
		t.Fatalf("restart RecoverWorkView: goal=%q nodes=%d", snap.Definition.Goal, len(snap.Definition.Nodes))
	}
	if snap.Definition.Status != work.DefDraft {
		t.Fatalf("restart RecoverWorkView: status=%s, want draft", snap.Definition.Status)
	}
}

func TestV2WailsCommittedRecoveryBindingsReturnTypedResult(t *testing.T) {
	for _, operation := range []string{
		"BeginWorkPlanning", "ApplyDefinition", "SubmitWorkInput",
		"SetInputCornerstone", "PreviewWorkPatch", "ApplyWorkPatch", "RetryWorkNode",
	} {
		t.Run(operation, func(t *testing.T) {
			err := fmt.Errorf("wrapped: %w", &work.ErrWorkCommittedRecovery{
				Operation: operation, WorkID: "w1", RequestID: "req1",
				Revision: 42, Committed: true, Recoverable: true, Cause: "injected",
			})
			var revision int64
			var committed, recoverable bool
			var transport *work.WorkTransportError
			bindWorkTransportError(&revision, &committed, &recoverable, &transport, err)
			if transport == nil || transport.Code != "committed_recovery" ||
				transport.Operation != operation || transport.RequestID != "req1" ||
				revision != 42 || !committed || !recoverable {
				t.Fatalf("typed binding = %+v revision=%d committed=%v recoverable=%v",
					transport, revision, committed, recoverable)
			}
		})
	}
}

func TestSubmitWorkInputMapsPublicDefinitionRevision(t *testing.T) {
	input := work.SubmitWorkInputRequest{
		WorkID:             "work-1",
		RunID:              "run-1",
		TaskID:             "task-1",
		BlockID:            "block-1",
		InputID:            "input-1",
		Value:              json.RawMessage(`20000`),
		DefinitionRevision: 7,
		InputRevision:      3,
		ExpectedRevision:   11,
		RequestID:          "submit-1",
	}
	mapped := submitInputRequest(input)
	if mapped.DefinitionRev != input.DefinitionRevision ||
		mapped.WorkID != input.WorkID ||
		mapped.InputID != input.InputID ||
		mapped.InputRevision != input.InputRevision ||
		mapped.ExpectedRevision != input.ExpectedRevision ||
		mapped.RequestID != input.RequestID ||
		string(mapped.Value) != string(input.Value) {
		t.Fatalf("mapped submit input = %+v", mapped)
	}
}

func TestV2WailsBeginPlanningTypedDuplicateAndConflict_FileWorkStore(t *testing.T) {
	store, err := work.NewFileWorkStore(filepath.Join(t.TempDir(), "works"), 0)
	if err != nil {
		t.Fatal(err)
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	svc.SetV2TransportEnabled(true)
	ctrl := control.New(control.Options{
		Work: svc, WorkViews: views, WorkV2Enabled: true,
	})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")
	input := work.BeginWorkPlanningInput{SessionID: "wails-begin", RequestID: "wails-begin"}
	first, err := app.BeginWorkPlanning("test", input)
	if err != nil || first.Result == nil || first.Duplicate || !first.Committed {
		t.Fatalf("first = %+v, err=%v", first, err)
	}
	replay, err := app.BeginWorkPlanning("test", input)
	if err != nil || replay.Result == nil || !replay.Duplicate ||
		replay.Revision != first.Revision {
		t.Fatalf("replay = %+v, err=%v", replay, err)
	}
	conflict, err := app.BeginWorkPlanning("test", work.BeginWorkPlanningInput{
		SessionID: "other", RequestID: input.RequestID,
	})
	if err != nil || conflict.TransportError == nil ||
		conflict.TransportError.Code != "request_conflict" ||
		conflict.TransportError.RequestID != input.RequestID {
		t.Fatalf("conflict = %+v, err=%v", conflict, err)
	}
}

func TestRecoverWorkViewRejectsInvalidIntent(t *testing.T) {
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	for _, tc := range []struct {
		name   string
		workID string
		intent work.ViewRecoveryIntent
	}{
		{name: "empty work", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncRetry, Generation: 1}},
		{name: "wrong reason", workID: "work-1", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncOverflow, Generation: 1}},
		{name: "zero generation", workID: "work-1", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncRetry}},
		{name: "unsafe generation", workID: "work-1", intent: work.ViewRecoveryIntent{Reason: work.ViewResyncRetry, Generation: 1 << 53}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := app.RecoverWorkView("test", tc.workID, tc.intent); err == nil || !strings.Contains(err.Error(), "valid recovery intent") {
				t.Fatalf("RecoverWorkView error = %v", err)
			}
		})
	}
}

func TestRecoverWorkViewAcceptsHydrateIntent(t *testing.T) {
	store := &worktest.Store{LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
		return &work.Work{
			ID: workID, Name: "hydrate work", State: work.WorkReady, ArchiveState: work.ArchiveActive,
		}, work.WorkEventState{Revision: 42}, nil
	}}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	app := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	app.setTestCtrl(ctrl, "test")

	event, err := app.RecoverWorkView("test", "work-hydrate", work.ViewRecoveryIntent{
		Reason: work.ViewResyncHydrate, Generation: 3,
	})
	if err != nil {
		t.Fatalf("RecoverWorkView hydrate: %v", err)
	}
	if event.Resync == nil || event.Resync.Reason != work.ViewResyncHydrate || !event.Resync.Authoritative || event.Resync.Generation != 3 {
		t.Fatalf("hydrate resync = %+v", event)
	}
	wantID := work.ResyncEventID("work-hydrate", 42, work.ViewResyncHydrate, 3)
	if event.EventID != wantID {
		t.Fatalf("hydrate EventID = %q, want %q", event.EventID, wantID)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("hydrate contract: %v", err)
	}
}

func TestWorkViewWailsWatchRejectsUnsafeSubscriptionID(t *testing.T) {
	app := &App{}
	if err := app.WatchWork("test", "work-1", "../../event"); err == nil {
		t.Fatal("expected unsafe subscription ID to fail")
	}
}

// TestResolveWorkControllerMissingTab verifies that resolveWorkController
// returns an error when the tab doesn't exist.
func TestResolveWorkControllerMissingTab(t *testing.T) {
	app := &App{}
	_, err := app.resolveWorkController("nonexistent-tab")
	if err == nil {
		t.Fatal("expected error for missing tab")
	}
}

// TestResolveWorkControllerDisabledWork verifies that a controller without
// Work service returns a clear error.
func TestResolveWorkControllerDisabledWork(t *testing.T) {
	// A Controller explicitly built without a Work service returns errors from WorkControl.
	c := control.New(control.Options{})
	if c == nil {
		t.Fatal("New returned nil")
	}
	wc := c.WorkControl()
	_, err := wc.CreateWork(nil, work.CreateWorkInput{})
	if err == nil {
		t.Fatal("expected error from disabled Work")
	}
}

func TestWorkEnabledUsesPerTabConfiguration(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	t.Setenv("WorkGround2_PREFER_USER_CONFIG", "")
	if err := os.WriteFile(filepath.Join(home, "config.toml"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	defaultRoot := t.TempDir()
	disabledRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(disabledRoot, "WorkGround2.toml"), []byte("[work]\nenabled = false\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	brokenRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(brokenRoot, "WorkGround2.toml"), []byte("[work\nenabled = true\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := &App{tabs: map[string]*WorkspaceTab{
		"default":  {ID: "default", WorkspaceRoot: defaultRoot},
		"disabled": {ID: "disabled", WorkspaceRoot: disabledRoot},
		"broken":   {ID: "broken", WorkspaceRoot: brokenRoot},
	}}
	for i := 0; i < 2; i++ {
		if enabled, err := app.WorkEnabled("default"); err != nil || !enabled {
			t.Fatalf("default WorkEnabled #%d = %v, %v; want true, nil", i+1, enabled, err)
		}
		if enabled, err := app.WorkEnabled("disabled"); err != nil || enabled {
			t.Fatalf("disabled WorkEnabled #%d = %v, %v; want false, nil", i+1, enabled, err)
		}
	}
	if enabled, err := app.WorkEnabled("missing"); err != nil || enabled {
		t.Fatalf("missing WorkEnabled = %v, %v; want false, nil", enabled, err)
	}
	if enabled, err := app.WorkEnabled("broken"); err == nil || enabled {
		t.Fatalf("broken WorkEnabled = %v, %v; want false, explicit error", enabled, err)
	}
}

func TestWorkCapableUsesTypedControllerAvailability(t *testing.T) {
	disabled := control.New(control.Options{})
	disabledApp := &App{}
	disabledApp.setTestCtrl(disabled, "disabled")
	if disabledApp.WorkCapable("test") {
		t.Fatal("WorkCapable = true for disabled Work controller")
	}
	var typedNil *work.Service
	typedNilApp := &App{}
	typedNilApp.setTestCtrl(control.New(control.Options{
		Work:      typedNil,
		WorkViews: control.NewWorkViewBroadcaster(),
	}), "typed-nil")
	if typedNilApp.WorkCapable("test") {
		t.Fatal("WorkCapable = true for typed-nil Work service")
	}

	views := control.NewWorkViewBroadcaster()
	enabled := control.New(control.Options{
		Work:      work.NewService(&worktest.Store{}, work.NewBlueprintRegistry(), views),
		WorkViews: views,
	})
	enabledApp := &App{}
	enabledApp.setTestCtrl(enabled, "enabled")
	if !enabledApp.WorkCapable("test") {
		t.Fatal("WorkCapable = false for enabled Work controller")
	}
	if enabledApp.WorkCapable("missing") {
		t.Fatal("WorkCapable = true for missing tab")
	}
}

func TestWorkCollaborationV2EnabledPreservesV1Gate(t *testing.T) {
	newApp := func(v2 bool) *App {
		views := control.NewWorkViewBroadcaster()
		ctrl := control.New(control.Options{
			Work:          work.NewService(&worktest.Store{}, work.NewBlueprintRegistry(), views),
			WorkViews:     views,
			WorkV2Enabled: v2,
		})
		app := &App{}
		app.setTestCtrl(ctrl, "v2-gate")
		return app
	}

	off := newApp(false)
	if !off.WorkCapable("test") {
		t.Fatal("V1 Work capability must remain available when V2 is disabled")
	}
	if off.WorkCollaborationV2Enabled("test") {
		t.Fatal("V2 planning entry must stay hidden when its explicit gate is disabled")
	}

	on := newApp(true)
	if !on.WorkCollaborationV2Enabled("test") {
		t.Fatal("V2 planning entry must be available when Work and its V2 gate are enabled")
	}
	if on.WorkCollaborationV2Enabled("missing") {
		t.Fatal("missing controller must not expose the V2 planning entry")
	}
}

// TestWorkMethodsNilService verifies the nil-safe delegation.
func TestWorkMethodsNilService(t *testing.T) {
	c := control.New(control.Options{})
	wc := c.WorkControl()

	// All delegated methods should return error.
	if _, err := wc.GetWork(nil, "w-1"); err == nil {
		t.Fatal("GetWork: expected error")
	}
	if _, err := wc.ListWorks(nil, work.WorkFilter{}); err == nil {
		t.Fatal("ListWorks: expected error")
	}
	if _, err := wc.ArchiveWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("ArchiveWork: expected error")
	}
	if _, err := wc.RestoreWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("RestoreWork: expected error")
	}
	if err := wc.DeleteWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("DeleteWork: expected error")
	}

	// Not-yet-implemented methods should also return error.
	if _, err := wc.RunWork(nil, "w-1", "r-1"); err == nil {
		t.Fatal("RunWork: expected error")
	}
	if _, err := wc.RetryTask(nil, work.RetryTaskInput{}); err == nil {
		t.Fatal("RetryTask: expected error")
	}
}

func TestWorkBindingRoutesToTabController(t *testing.T) {
	var gotID string
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			gotID = workID
			return &work.Work{ID: workID, Name: "routed"}, work.WorkEventState{Revision: 7}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{
		Work:      work.NewService(store, work.NewBlueprintRegistry(), views),
		WorkViews: views,
	})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	view, err := app.GetWork("test", "work-routed")
	if err != nil {
		t.Fatalf("GetWork: %v", err)
	}
	if gotID != "work-routed" || view == nil || view.Work == nil || view.Work.Name != "routed" || view.Revision != 7 {
		t.Fatalf("tab -> Controller route = gotID %q, view %+v", gotID, view)
	}
}

// TestImportDirection verifies internal/work does not import control or agent.
// This is a compile-time guarantee; the test just documents it.
func TestImportDirection(t *testing.T) {
	// internal/work.ViewSink is consumed by control.WorkViewBroadcaster.
	// internal/work.WorkController is embedded by control.WorkControl.
	// internal/work never imports control or agent.
	// Verified by: grep -r "workground2/internal/control\|workground2/internal/agent" internal/work/
	// Result: no matches.
}

// ── Cornerstone routing tests ────────────────────────────────────────────────

// TestCornerstoneMethodsDisabledWork verifies cornerstone methods return
// errWorkDisabled when no Work service is wired.
func TestCornerstoneMethodsDisabledWork(t *testing.T) {
	c := control.New(control.Options{})
	wc := c.WorkControl()

	if _, err := wc.PinCornerstone(nil, "w-1", work.PinCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("PinCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.RefreshCornerstone(nil, "w-1", work.RefreshCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("RefreshCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.RemoveCornerstone(nil, "w-1", work.RemoveCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("RemoveCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.UndoCornerstone(nil, "w-1", work.UndoCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("UndoCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.AcceptCornerstone(nil, "w-1", work.AcceptCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("AcceptCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.FreezeCornerstone(nil, "w-1", work.FreezeCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("FreezeCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.RepairCornerstone(nil, "w-1", work.RepairCornerstoneInput{RequestID: "r-1"}); err == nil {
		t.Fatal("RepairCornerstone: expected errWorkDisabled")
	}
	if _, err := wc.ResumeRun(nil, work.ResumeRunInput{WorkID: "w-1", RunID: "run-1", RequestID: "r-1"}); err == nil {
		t.Fatal("ResumeRun: expected errWorkDisabled")
	}
}

func TestResumeRunWailsBindingRoutesToController(t *testing.T) {
	app := &App{}
	app.setTestCtrl(control.New(control.Options{}), "resume-binding")
	_, err := app.ResumeRun("test", work.ResumeRunInput{WorkID: "w-1", RunID: "run-1", RequestID: "resume-1"})
	if err == nil || !strings.Contains(err.Error(), "feature is disabled") {
		t.Fatalf("ResumeRun binding error = %v, want disabled controller error", err)
	}
}

func TestResumeRunWailsBindingRejectsNeedsConfirmation(t *testing.T) {
	const workID = "work-needs-confirmation"
	const runID = "run-needs-confirmation"
	var executeCalls atomic.Int32
	store := &worktest.Store{LoadStateFunc: func(gotWorkID, requestID string) (*work.Work, work.WorkEventState, error) {
		if gotWorkID != workID || requestID != "resume-needs-confirmation/resume" {
			t.Fatalf("LoadState context = (%q, %q)", gotWorkID, requestID)
		}
		return &work.Work{
			ID:           workID,
			State:        work.WorkWaitingUser,
			ArchiveState: work.ArchiveActive,
			Runs: []work.WorkflowRun{{
				ID: runID, WorkID: workID, State: work.RunNeedsConfirmation,
			}},
		}, work.WorkEventState{}, nil
	}}
	svc := work.NewService(store, work.NewBlueprintRegistry(), control.NewWorkViewBroadcaster())
	executor := &worktest.TaskExecutor{ExecuteFunc: func(context.Context, work.TaskExecuteInput) (*work.Attempt, error) {
		executeCalls.Add(1)
		return nil, errors.New("unexpected execution")
	}}
	app := &App{}
	app.setTestCtrl(control.New(control.Options{Work: svc, TaskExecutor: executor}), "resume-confirmation-binding")

	_, err := app.ResumeRun("test", work.ResumeRunInput{
		WorkID: workID, RunID: runID, RequestID: "resume-needs-confirmation",
	})
	if err == nil || !strings.Contains(err.Error(), "only waiting runs can be resumed") {
		t.Fatalf("ResumeRun needs_confirmation error = %v, want waiting-only rejection", err)
	}
	if executeCalls.Load() != 0 {
		t.Fatalf("ResumeRun needs_confirmation executed %d tasks", executeCalls.Load())
	}
}

// TestCornerstoneWailsBindingRoutesPin verifies the PinCornerstone Wails binding
// routes through tabID → Controller → Service → CornerstoneManager.
func TestCornerstoneWailsBindingRoutesPin(t *testing.T) {
	var committedWorkID string
	var committedEventType work.WorkEventType
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return &work.Work{ID: workID, Name: "cs-routed", ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
		CommitEventFunc: func(workID string, event work.WorkEvent) (int64, error) {
			committedWorkID = workID
			committedEventType = event.Type
			return 2, nil
		},
		LoadProjectionFunc: func(workID string) (*work.Work, error) {
			// Return nil to trigger post-commit recovery — this proves routing
			// reached CornerstoneManager.Pin and the event was committed.
			return nil, work.ErrWorkNotFound
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	cm := work.NewCornerstoneManager(store, nil, nil)
	svc.SetCornerstoneManager(cm)

	ctrl := control.New(control.Options{
		Work:      svc,
		WorkViews: views,
	})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	input := work.PinCornerstoneInput{
		Type:             work.CornerstoneInstruction,
		Title:            "测试基石",
		Content:          "内容",
		Ref:              work.CornerstoneRef{Kind: "inline"},
		Mode:             work.CornerstoneSnapshot,
		Required:         true,
		ExpectedRevision: 1,
		RequestID:        "pin-req-1",
	}

	_, err := app.PinCornerstone("test", "work-cs-routed", input)
	// We expect an error because the projection recovery fails, but the
	// important thing is the event was committed through the full stack.
	if committedWorkID != "work-cs-routed" {
		t.Fatalf("PinCornerstone did not route to store: committedWorkID = %q", committedWorkID)
	}
	if committedEventType != work.EventCornerstoneUpserted {
		t.Fatalf("PinCornerstone did not commit cornerstone event: got %q", committedEventType)
	}
	if err == nil {
		t.Fatal("expected error from projection recovery, got nil")
	}
	t.Logf("routing verified: PinCornerstone reached CornerstoneManager, event committed (err=%v)", err)
}

// TestCornerstoneWailsBindingRoutesRemove verifies the RemoveCornerstone Wails
// binding routes correctly.
func TestCornerstoneWailsBindingRoutesRemove(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			cs := work.Cornerstone{ID: "cs-1", WorkID: workID, Status: work.CornerstoneActive, Mode: work.CornerstoneSnapshot}
			return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{cs}, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
		CommitEventFunc: func(workID string, event work.WorkEvent) (int64, error) {
			return 2, nil
		},
		LoadProjectionFunc: func(workID string) (*work.Work, error) {
			cs := work.Cornerstone{ID: "cs-1", WorkID: workID, Status: work.CornerstoneActive, Tombstone: true}
			return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{cs}, ArchiveState: work.ArchiveActive}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	cm := work.NewCornerstoneManager(store, nil, nil)
	svc.SetCornerstoneManager(cm)

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	input := work.RemoveCornerstoneInput{
		CornerstoneID:    "cs-1",
		ExpectedRevision: 1,
		RequestID:        "remove-req-1",
	}
	result, err := app.RemoveCornerstone("test", "work-cs-routed", input)
	if err != nil {
		t.Fatalf("RemoveCornerstone Wails binding: %v", err)
	}
	if result == nil {
		t.Fatal("RemoveCornerstone returned nil result")
	}
}

func TestCornerstoneWailsBindingRoutesSnapshotRepairContent(t *testing.T) {
	const replacement = "replacement-content"
	digest := work.ContentDigest([]byte(replacement))
	missing := work.Cornerstone{
		ID: "cs-blob", WorkID: "work-cs-repair", Mode: work.CornerstoneSnapshot,
		Ref: work.CornerstoneRef{Kind: "inline", BlobDigest: digest}, Digest: digest,
		Status: work.CornerstoneInvalid, ResolveErrorKind: work.ResolveErrorInvalid,
	}
	blobs := &repairBlobStore{}
	var committed *work.WorkEvent
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			if committed != nil {
				active := missing
				active.Status = work.CornerstoneActive
				active.ResolveErrorKind = ""
				return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{active}, ArchiveState: work.ArchiveActive}, work.WorkEventState{
					Revision: 2, RequestRevision: 2, RequestType: committed.Type,
					RequestEventID: committed.ID, RequestFound: true,
				}, nil
			}
			return &work.Work{ID: workID, Cornerstones: []work.Cornerstone{missing}, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
		CommitEventFunc: func(workID string, event work.WorkEvent) (int64, error) {
			copy := event
			committed = &copy
			return 2, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	svc.SetCornerstoneManager(work.NewCornerstoneManager(store, blobs, nil))
	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	content := replacement
	result, err := app.RepairCornerstone("test", "work-cs-repair", work.RepairCornerstoneInput{
		CornerstoneID: "cs-blob", Content: &content, ExpectedRevision: 1, RequestID: "repair-content-1",
	})
	if err != nil {
		t.Fatalf("RepairCornerstone Wails binding: %v", err)
	}
	if blobs.content != replacement {
		t.Fatal("RepairCornerstone did not route replacement content to BlobStore")
	}
	if result == nil || !result.Repaired || result.Cornerstone == nil || result.Cornerstone.Status != work.CornerstoneActive {
		t.Fatalf("RepairCornerstone result = %+v, want active", result)
	}
}

// TestCornerstoneWailsBindingConflict verifies that a revision
// conflict returns a clear error (not a panic).
func TestCornerstoneWailsBindingConflict(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return &work.Work{ID: workID, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 5}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	cm := work.NewCornerstoneManager(store, nil, nil)
	svc.SetCornerstoneManager(cm)

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	// ExpectedRevision 1 conflicts with actual revision 5.
	input := work.PinCornerstoneInput{
		Type:             work.CornerstoneInstruction,
		Title:            "冲突测试",
		Content:          "内容",
		Ref:              work.CornerstoneRef{Kind: "inline"},
		Mode:             work.CornerstoneSnapshot,
		ExpectedRevision: 1,
		RequestID:        "conflict-req-1",
	}
	_, err := app.PinCornerstone("test", "work-conflict", input)
	if err == nil {
		t.Fatal("expected revision conflict error")
	}
	t.Logf("conflict error (expected): %v", err)
}

// ── Per-work resync gate linearization tests ─────────────────────────────────

// encBarrier blocks only the first marshal call. Later calls pass immediately,
// allowing a missing/narrowed per-work gate to sign the newer snapshot before
// the deliberately stalled older snapshot.
func encBarrier() (marshal func(any) ([]byte, error), entered <-chan struct{}, unblock func()) {
	ready := make(chan struct{})
	done := make(chan struct{})
	var calls atomic.Int32
	return func(v any) ([]byte, error) {
			if calls.Add(1) == 1 {
				close(ready)
				<-done
			}
			return json.Marshal(v)
		}, ready, func() {
			close(done)
		}
}

// makeWork returns a minimal ready Work for test projection.
func makeWork(id, name string, state work.WorkState, revision int64) (*work.Work, work.WorkEventState, error) {
	return &work.Work{ID: id, Name: name, State: state, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: revision}, nil
}

func makeAssessmentWork(id string, blocked bool, revision int64) (*work.Work, work.WorkEventState, error) {
	status := work.CornerstoneActive
	if blocked {
		status = work.CornerstoneInvalid
	}
	return &work.Work{
		ID: id, Name: "assessment", State: work.WorkReady, ArchiveState: work.ArchiveActive,
		Cornerstones: []work.Cornerstone{{
			ID: "cs-required", WorkID: id, Type: work.CornerstonePolicy, Title: "required",
			Ref: work.CornerstoneRef{Kind: "inline"}, Mode: work.CornerstoneSnapshot,
			Required: true, Status: status,
		}},
	}, work.WorkEventState{Revision: revision}, nil
}

func waitForResyncGateRefs(t *testing.T, app *App, workID string, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		got := resyncGateRefs(app, workID)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("gate refs for %s = %d, want %d", workID, got, want)
		}
		time.Sleep(time.Millisecond)
	}
}

func resyncGateRefs(app *App, workID string) int {
	app.workResyncGates.mu.Lock()
	defer app.workResyncGates.mu.Unlock()
	if gate := app.workResyncGates.gates[workID]; gate != nil {
		return gate.refs
	}
	return 0
}

// coordinateResyncPair releases the first marshal only after the second call
// is observably scheduled. With the correct gate, refs == 2 while LoadState is
// still 1. Without the gate (or with a gate narrowed past GetWork), the second
// call loads and signs its snapshot first; waiting for secondDone makes that
// broken ordering deterministic before the old snapshot is released.
func coordinateResyncPair(app *App, workID string, loads *atomic.Int32, secondDone <-chan struct{}, releaseFirst func()) (enteredEarly bool, err error) {
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()

	for {
		refs := resyncGateRefs(app, workID)
		loadCount := loads.Load()
		if refs >= 2 {
			releaseFirst()
			return loadCount >= 2, nil
		}
		if loadCount >= 2 {
			select {
			case <-secondDone:
				releaseFirst()
				return true, nil
			default:
			}
		}

		select {
		case <-ticker.C:
		case <-deadline.C:
			releaseFirst()
			return false, fmt.Errorf("second resync was not observable: loads=%d refs=%d", loadCount, refs)
		}
	}
}

func mustDecodePayload(t *testing.T, event *work.WorkViewEvent) work.WorkView {
	t.Helper()
	var v work.WorkView
	if err := json.Unmarshal(event.Payload, &v); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return v
}

func TestAuthoritativeResyncLinearizesSameWorkConcurrent(t *testing.T) {
	type step struct {
		name          string
		firstBlocked  bool
		secondBlocked bool
	}
	for _, tc := range []step{
		{name: "ready→blocked", firstBlocked: false, secondBlocked: true},
		{name: "blocked→ready", firstBlocked: true, secondBlocked: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Store returns firstState then secondState on successive calls.
			var callCount atomic.Int32
			store := &worktest.Store{
				LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
					n := callCount.Add(1)
					if n == 1 {
						return makeAssessmentWork(workID, tc.firstBlocked, 10)
					}
					return makeAssessmentWork(workID, tc.secondBlocked, 10)
				},
			}
			views := control.NewWorkViewBroadcaster()
			ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
			wc := ctrl.WorkControl()

			marshal, entered, unblock := encBarrier()
			app := &App{ctx: context.Background()}

			// A enters gate, calls GetWork (gets firstState), then blocks in encoder.
			// B tries to enter gate but is blocked behind A.
			var wg sync.WaitGroup
			var events [2]*work.WorkViewEvent
			var errs [2]error
			wg.Add(1)
			go func() {
				defer wg.Done()
				events[0], errs[0] = app.authoritativeWorkViewResyncWithMarshal(
					t.Context(), wc, "work-linear", work.ViewResyncOverflow, 0, marshal)
			}()

			// Wait until A reaches the encoder barrier.
			<-entered

			// Launch B — must block at per-work gate (A still holds it).
			wg.Add(1)
			secondDone := make(chan struct{})
			go func() {
				defer wg.Done()
				defer close(secondDone)
				events[1], errs[1] = app.authoritativeWorkViewResyncWithMarshal(
					t.Context(), wc, "work-linear", work.ViewResyncOverflow, 0, marshal)
			}()

			enteredEarly, coordinateErr := coordinateResyncPair(app, "work-linear", &callCount, secondDone, unblock)
			wg.Wait()
			if coordinateErr != nil {
				t.Fatal(coordinateErr)
			}

			for i, err := range errs {
				if err != nil {
					t.Fatalf("resync %d: %v", i, err)
				}
			}
			if enteredEarly {
				t.Errorf("B entered GetWork before A released the same-work gate")
			}

			genA := events[0].Resync.Generation
			genB := events[1].Resync.Generation
			if genA == genB {
				t.Errorf("generations must differ: both %d", genA)
			}
			if genA >= genB {
				t.Errorf("A generation %d must be < B generation %d", genA, genB)
			}

			viewA := mustDecodePayload(t, events[0])
			viewB := mustDecodePayload(t, events[1])
			if viewA.Assessment.Blocking != tc.firstBlocked || (viewA.RunBlock != nil) != tc.firstBlocked {
				t.Fatalf("A assessment = %#v, runBlock=%#v, want blocked=%v", viewA.Assessment, viewA.RunBlock, tc.firstBlocked)
			}
			if viewB.Assessment.Blocking != tc.secondBlocked || (viewB.RunBlock != nil) != tc.secondBlocked {
				t.Fatalf("B assessment = %#v, runBlock=%#v, want blocked=%v", viewB.Assessment, viewB.RunBlock, tc.secondBlocked)
			}
			latest := viewA
			if genB > genA {
				latest = viewB
			}
			if latest.Assessment.Blocking != tc.secondBlocked || (latest.RunBlock != nil) != tc.secondBlocked {
				t.Fatalf("highest generation retained blocked=%v, want newest blocked=%v", latest.Assessment.Blocking, tc.secondBlocked)
			}
		})
	}
}

func TestAuthoritativeResyncHydrateOverflowSharePerWorkGate(t *testing.T) {
	// Store returns "ready" then "blocked" on successive calls.
	var callCount atomic.Int32
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			n := callCount.Add(1)
			if n == 1 {
				return makeAssessmentWork(workID, false, 10)
			}
			return makeAssessmentWork(workID, true, 10)
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	wc := ctrl.WorkControl()

	marshal, entered, unblock := encBarrier()
	app := &App{ctx: context.Background()}

	// Hydrate enters first, blocks in encoder.
	var wg sync.WaitGroup
	var evHydrate, evOverflow *work.WorkViewEvent
	var errHydrate, errOverflow error
	wg.Add(1)
	go func() {
		defer wg.Done()
		evHydrate, errHydrate = app.authoritativeWorkViewResyncWithMarshal(
			t.Context(), wc, "work-shared", work.ViewResyncHydrate, 3, marshal)
	}()

	<-entered // hydrate is in encoder

	// Overflow starts, blocked behind hydrate's gate.
	wg.Add(1)
	secondDone := make(chan struct{})
	go func() {
		defer wg.Done()
		defer close(secondDone)
		evOverflow, errOverflow = app.authoritativeWorkViewResyncWithMarshal(
			t.Context(), wc, "work-shared", work.ViewResyncOverflow, 0, marshal)
	}()

	enteredEarly, coordinateErr := coordinateResyncPair(app, "work-shared", &callCount, secondDone, unblock)
	wg.Wait()
	if coordinateErr != nil {
		t.Fatal(coordinateErr)
	}

	if errHydrate != nil {
		t.Fatalf("hydrate: %v", errHydrate)
	}
	if errOverflow != nil {
		t.Fatalf("overflow: %v", errOverflow)
	}
	if enteredEarly {
		t.Errorf("overflow entered GetWork before hydrate released the same-work gate")
	}

	// Hydrate got in first → lower generation, first state.
	// Overflow got in second → higher generation, second state.
	if evHydrate.Resync.Generation >= evOverflow.Resync.Generation {
		t.Errorf("hydrate gen %d must be < overflow gen %d", evHydrate.Resync.Generation, evOverflow.Resync.Generation)
	}
	if evHydrate.Resync.Reason != work.ViewResyncHydrate {
		t.Fatalf("hydrate reason = %q", evHydrate.Resync.Reason)
	}
	if evOverflow.Resync.Reason != work.ViewResyncOverflow {
		t.Fatalf("overflow reason = %q", evOverflow.Resync.Reason)
	}
	if evHydrate.Resync.Generation < 3 {
		t.Fatalf("hydrate minGeneration 3 not honored: gen=%d", evHydrate.Resync.Generation)
	}

	viewHydrate := mustDecodePayload(t, evHydrate)
	viewOverflow := mustDecodePayload(t, evOverflow)
	if viewHydrate.Assessment.Blocking || viewHydrate.RunBlock != nil {
		t.Fatalf("hydrate payload = assessment %#v, runBlock %#v, want ready", viewHydrate.Assessment, viewHydrate.RunBlock)
	}
	if !viewOverflow.Assessment.Blocking || viewOverflow.RunBlock == nil {
		t.Fatalf("overflow payload = assessment %#v, runBlock %#v, want blocked", viewOverflow.Assessment, viewOverflow.RunBlock)
	}
	latest := viewHydrate
	if evOverflow.Resync.Generation > evHydrate.Resync.Generation {
		latest = viewOverflow
	}
	if !latest.Assessment.Blocking || latest.RunBlock == nil {
		t.Fatalf("highest generation must retain newest blocked projection: assessment %#v, runBlock %#v", latest.Assessment, latest.RunBlock)
	}
}

func TestAuthoritativeResyncEncoderErrorReleasesGate(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return makeWork(workID, "ok", work.WorkReady, 1)
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	wc := ctrl.WorkControl()

	injected := errors.New("encoder exploded")
	app := &App{ctx: context.Background()}

	// First call: encoder error → gate must be released.
	_, err := app.authoritativeWorkViewResyncWithMarshal(t.Context(), wc, "work-enc-err", work.ViewResyncOverflow, 0, func(any) ([]byte, error) {
		return nil, injected
	})
	if err == nil || !strings.Contains(err.Error(), "encoder exploded") {
		t.Fatalf("expected encoder error, got %v", err)
	}

	// Second call with real encoder must succeed — gate was released.
	event, err := app.authoritativeWorkViewResync(t.Context(), wc, "work-enc-err", work.ViewResyncRetry, 0)
	if err != nil {
		t.Fatalf("second resync after encoder error release: %v", err)
	}
	if event.Resync == nil || event.Resync.Generation == 0 {
		t.Fatalf("second resync event = %+v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("second event contract: %v", err)
	}
	t.Logf("encoder error released gate, second call gen=%d", event.Resync.Generation)
}

func TestAuthoritativeResyncEncoderPanicReleasesGate(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return makeWork(workID, "ok", work.WorkReady, 1)
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	wc := ctrl.WorkControl()

	app := &App{ctx: context.Background()}

	// Run the resync in a goroutine, recover the panic.
	panicCaught := make(chan struct{})
	go func() {
		defer func() {
			if r := recover(); r != nil {
				close(panicCaught)
			}
		}()
		_, _ = app.authoritativeWorkViewResyncWithMarshal(t.Context(), wc, "work-panic", work.ViewResyncOverflow, 0, func(any) ([]byte, error) {
			panic("encoder kaboom")
		})
	}()
	select {
	case <-panicCaught:
	case <-time.After(2 * time.Second):
		t.Fatal("encoder panic was not recovered in time")
	}

	// Second call with real encoder must succeed — gate was released by defer.
	event, err := app.authoritativeWorkViewResync(t.Context(), wc, "work-panic", work.ViewResyncRetry, 0)
	if err != nil {
		t.Fatalf("second resync after panic release: %v", err)
	}
	if event.Resync == nil || event.Resync.Generation == 0 {
		t.Fatalf("second resync after panic = %+v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("second event contract: %v", err)
	}
	t.Logf("encoder panic released gate, second call gen=%d", event.Resync.Generation)
}

func TestAuthoritativeResyncGetWorkErrorReleasesGate(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return nil, work.WorkEventState{}, errors.New("injected GetWork failure")
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	wc := ctrl.WorkControl()
	app := &App{ctx: context.Background()}

	_, err1 := app.authoritativeWorkViewResync(t.Context(), wc, "work-gwerr", work.ViewResyncOverflow, 0)
	if err1 == nil || !strings.Contains(err1.Error(), "injected GetWork failure") {
		t.Fatalf("first resync: expected injected error, got %v", err1)
	}

	store.LoadStateFunc = func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
		return makeWork(workID, "recovered", work.WorkReady, 1)
	}
	event, err2 := app.authoritativeWorkViewResync(t.Context(), wc, "work-gwerr", work.ViewResyncOverflow, 0)
	if err2 != nil {
		t.Fatalf("second resync after GetWork error: %v", err2)
	}
	if event.Resync == nil || event.Resync.Generation == 0 {
		t.Fatalf("second resync event = %+v", event)
	}
	if err := event.Validate(); err != nil {
		t.Fatalf("second event contract: %v", err)
	}
}

func TestAuthoritativeResyncDifferentWorkNoCrossBlock(t *testing.T) {
	ready := make(chan struct{})
	entered := make(chan string, 2)
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			entered <- workID
			<-ready
			return makeWork(workID, "work-"+workID, work.WorkReady, 1)
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	wc := ctrl.WorkControl()
	app := &App{ctx: context.Background()}

	var wg sync.WaitGroup
	errCh := make(chan error, 2)
	for _, wid := range []string{"work-A", "work-B"} {
		wg.Add(1)
		go func(workID string) {
			defer wg.Done()
			_, err := app.authoritativeWorkViewResync(t.Context(), wc, workID, work.ViewResyncOverflow, 0)
			errCh <- err
		}(wid)
	}

	// Both must enter their gates — different workIDs, no cross-block.
	seen := map[string]bool{}
	for range 2 {
		select {
		case id := <-entered:
			seen[id] = true
		case <-time.After(2 * time.Second):
			t.Fatalf("timeout waiting for work gates: saw %v", seen)
		}
	}
	if !seen["work-A"] || !seen["work-B"] {
		t.Fatalf("both workIDs must enter: %v", seen)
	}
	close(ready)
	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			t.Fatalf("different workID resync: %v", err)
		}
	}
}

func TestAuthoritativeResyncGateRefsWaiterRace(t *testing.T) {
	// Stress test: N goroutines on same workID, held behind a barrier.
	// The holder releases; waiters acquire in order; map must be empty after.
	const N = 8
	ready := make(chan struct{})
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			<-ready
			return makeWork(workID, "ok", work.WorkReady, 1)
		},
	}
	views := control.NewWorkViewBroadcaster()
	ctrl := control.New(control.Options{Work: work.NewService(store, work.NewBlueprintRegistry(), views), WorkViews: views})
	wc := ctrl.WorkControl()
	app := &App{ctx: context.Background()}

	var wg sync.WaitGroup
	errCh := make(chan error, N)
	for range N {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := app.authoritativeWorkViewResync(t.Context(), wc, "work-refs", work.ViewResyncOverflow, 0)
			errCh <- err
		}()

	}
	waitForResyncGateRefs(t, app, "work-refs", N)
	// Before release, gate must exist with refs=N.
	app.workResyncGates.mu.Lock()
	gate, exists := app.workResyncGates.gates["work-refs"]
	refsBefore := 0
	if exists {
		refsBefore = gate.refs
	}
	app.workResyncGates.mu.Unlock()
	if !exists {
		t.Fatal("gate must exist while waiters are pending")
	}
	if refsBefore != N {
		t.Fatalf("refs before release = %d, want %d", refsBefore, N)
	}

	close(ready)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("waiter error: %v", err)
		}
	}

	// After all done, map must be empty.
	app.workResyncGates.mu.Lock()
	_, leftover := app.workResyncGates.gates["work-refs"]
	app.workResyncGates.mu.Unlock()
	if leftover {
		t.Fatal("gate entry was not cleaned up after all waiters finished")
	}
}

// TestCornerstoneWithoutManager returns ErrCornerstoneDisabled.
func TestCornerstoneWithoutManager(t *testing.T) {
	store := &worktest.Store{
		LoadStateFunc: func(workID, requestID string) (*work.Work, work.WorkEventState, error) {
			return &work.Work{ID: workID, ArchiveState: work.ArchiveActive}, work.WorkEventState{Revision: 1}, nil
		},
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	// NewService owns a manager by default so Run/Resume preflight and the
	// intent API share one source of truth. Exercise the explicit disabled path.
	svc.SetCornerstoneManager(nil)

	ctrl := control.New(control.Options{Work: svc, WorkViews: views})
	app := &App{}
	app.setTestCtrl(ctrl, "test")

	_, err := app.PinCornerstone("test", "work-no-cm", work.PinCornerstoneInput{
		Type: work.CornerstoneInstruction, Title: "no manager", ExpectedRevision: 1, RequestID: "r-1",
	})
	if err == nil {
		t.Fatal("expected ErrCornerstoneDisabled")
	}
	if !errors.Is(err, work.ErrCornerstoneDisabled) {
		t.Fatalf("expected ErrCornerstoneDisabled, got %v", err)
	}
}

func TestV2WailsBindingsStayThin(t *testing.T) {
	source, err := os.ReadFile("works.go")
	if err != nil {
		t.Fatal(err)
	}
	section := string(source)
	marker := "// ── V2 Collaboration Controller Wails bindings"
	if index := strings.Index(section, marker); index >= 0 {
		section = section[index:]
	} else {
		t.Fatalf("V2 binding marker missing from works.go")
	}
	for _, forbidden := range []string{
		"NewFileWorkStore", "LoadProjection(", "LoadState(", "CommitEvent(",
		"NewService(", "NewV2Scheduler(", "ScheduleAffected(", "ExecuteTask(",
	} {
		if strings.Contains(section, forbidden) {
			t.Fatalf("V2 Wails bindings must not access domain/store/scheduler directly: found %q", forbidden)
		}
	}
	for method, delegate := range map[string]string{
		"BeginWorkPlanning": "BeginWorkPlanningWithResult",
		"ApplyDefinition":   "ApplyDefinition", "SubmitWorkInput": "SubmitWorkInput",
		"SetInputCornerstone": "SetInputCornerstone", "PreviewWorkPatch": "PreviewWorkPatch",
		"ApplyWorkPatch": "ApplyWorkPatch", "RetryWorkNode": "RetryWorkNode",
	} {
		signature := "func (a *App) " + method + "("
		start := strings.Index(section, signature)
		if start < 0 {
			t.Fatalf("V2 Wails binding %s missing", method)
		}
		body := section[start:]
		next := strings.Index(body[len(signature):], "\nfunc (a *App) ")
		if next >= 0 {
			body = body[:len(signature)+next]
		}
		if strings.Count(body, "resolveWorkController(") != 1 ||
			!strings.Contains(body, "wc."+delegate+"(") {
			t.Fatalf("%s must only resolve the Controller and delegate, body:\n%s", method, body)
		}
	}
	for _, forbiddenMethod := range []string{"ScheduleV2Run", "RecoverV2Scheduling"} {
		if strings.Contains(section, "func (a *App) "+forbiddenMethod+"(") {
			t.Fatalf("internal scheduler/recovery method %s must not be exported through Wails", forbiddenMethod)
		}
	}
}

// ── Historical zero-time recovery through production chain ──────────────────
// Commit a RunChanged event with Stage.StartedAt=time.Time{} into the real
// FileWorkStore event log. Close, reopen with new FileWorkStore + Service +
// Controller + App. Call production App.GetWork and verify the DTO natively
// contains "0001-01-01T00:00:00Z" without post-hoc string injection.

func TestV2HistoricalZeroTime_FileWorkStoreRestartAppGetWork(t *testing.T) {
	workDir := filepath.Join(t.TempDir(), "works-histzero")
	store, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("NewFileWorkStore: %v", err)
	}
	views := control.NewWorkViewBroadcaster()
	svc := work.NewService(store, work.NewBlueprintRegistry(), views)
	svc.SetV2TransportEnabled(true)
	ctx := t.Context()

	// 1. Normal production flow: BeginWorkPlanning → CreateCandidate → ApplyDefinition.
	planning, err := svc.BeginWorkPlanning(ctx, work.BeginWorkPlanningInput{
		SessionID: "histzero", RequestID: "histzero-plan",
	})
	if err != nil {
		t.Fatalf("BeginWorkPlanning: %v", err)
	}
	workID := planning.Work.ID

	now := time.Now().UTC()
	candidate := &work.WorkDefinitionRevision{
		WorkID: workID, Revision: 2, ParentRevision: 1, Status: work.DefDraft,
		Goal:  "Historical zero-time test",
		Nodes: []work.NodeDef{{ID: "n1", Title: "Frozen"}},
		ArtifactSlots: []work.ArtifactSlotDef{
			{ID: "slot1", Title: "Output", Kind: "text", ExpectedCount: 1, Required: true},
		},
		CreatedBy: "test", CreatedAt: now,
	}
	candidate.Digest, err = work.ComputeV2RevisionDigest(candidate)
	if err != nil {
		t.Fatalf("digest: %v", err)
	}
	candidate, err = svc.CreateCandidateRevision(ctx, workID, candidate, "histzero-cand", planning.Revision)
	if err != nil {
		t.Fatalf("CreateCandidateRevision: %v", err)
	}
	_, st, _ := store.LoadState(workID, "")
	result, err := svc.ApplyDefinition(ctx, work.ApplyDefinitionInput{
		WorkID: workID, Revision: candidate.Revision,
		RequestID: "histzero-apply", ExpectedRevision: st.Revision,
	})
	if err != nil {
		t.Fatalf("ApplyDefinition: %v", err)
	}
	if result.View == nil || len(result.View.Work.Runs) == 0 {
		t.Fatal("ApplyDefinition returned no runs")
	}

	// 2. Commit an EventRunChanged that overwrites the run with zero-time stages.
	//    This injects historical zero-time data into the real event log so that
	//    restart + event replay natively produces the zero-time projection.
	currentProj, currentState, err := store.LoadState(workID, "")
	if err != nil || currentProj == nil {
		t.Fatalf("LoadState: err=%v", err)
	}
	origRun := result.View.Work.Runs[0]
	zeroRun := origRun
	zeroRun.Stages = make([]work.Stage, len(origRun.Stages))
	copy(zeroRun.Stages, origRun.Stages)
	for si := range zeroRun.Stages {
		zeroRun.Stages[si].StartedAt = time.Time{} // pre-fix zero value
	}
	runPayload, err := json.Marshal(struct {
		Run       work.WorkflowRun `json:"run"`
		WorkState work.WorkState   `json:"workState"`
	}{Run: zeroRun, WorkState: work.WorkRunning})
	if err != nil {
		t.Fatalf("marshal runEventPayload: %v", err)
	}
	changedEvent := work.WorkEvent{
		SchemaVersion: work.SchemaVersion,
		ID:            "evt-histzero-zero-" + zeroRun.ID,
		WorkID:        workID,
		Type:          work.EventRunChanged,
		RequestID:     "histzero-zero-inject/changed",
		Payload:       runPayload,
		BaseRevision:  currentState.Revision,
		Revision:      currentState.Revision + 1,
		Object:        work.ObjectContext{Kind: work.ObjectRun, ID: zeroRun.ID, WorkID: workID},
		CreatedAt:     time.Now().UTC(),
	}
	_, err = store.CommitEvent(workID, changedEvent)
	if err != nil {
		t.Fatalf("CommitEvent EventRunChanged: %v", err)
	}

	// 3. Verify the projection now natively contains zero-time stages.
	proj, err := store.LoadProjection(workID)
	if err != nil || proj == nil || len(proj.Runs) == 0 {
		t.Fatalf("LoadProjection after CommitEvent: err=%v", err)
	}
	foundZero := false
	for _, run := range proj.Runs {
		for _, stage := range run.Stages {
			if stage.StartedAt.IsZero() {
				foundZero = true
			}
		}
	}
	if !foundZero {
		t.Fatal("projection does not contain zero-time stage after CommitEvent")
	}
	t.Log("zero-time stage committed to event log and reflected in projection: OK")

	// 4. Restart: new FileWorkStore → Service → Controller → App.
	reopened, err := work.NewFileWorkStore(workDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("restart FileWorkStore: %v", err)
	}
	restartViews := control.NewWorkViewBroadcaster()
	restartService := work.NewService(reopened, work.NewBlueprintRegistry(), restartViews)
	restartService.SetV2TransportEnabled(true)
	restartCtrl := control.New(control.Options{
		Work: restartService, WorkViews: restartViews, WorkV2Enabled: true,
	})
	restartApp := &App{ctx: context.Background(), workWatches: map[string]*workViewWatch{}}
	restartApp.setTestCtrl(restartCtrl, "restart")

	// 5. Production App.GetWork → DTO must natively contain "0001-01-01T00:00:00Z".
	view, err := restartApp.GetWork("test", workID)
	if err != nil {
		t.Fatalf("restart GetWork: %v", err)
	}
	if view == nil || view.Work == nil {
		t.Fatal("restart GetWork returned nil view")
	}
	dto, err := json.Marshal(view)
	if err != nil {
		t.Fatalf("marshal GetWork DTO: %v", err)
	}
	if !strings.Contains(string(dto), `"0001-01-01T00:00:00Z"`) {
		t.Fatalf("App.GetWork DTO does NOT natively contain zero-time serialization; DTO snippet: %.200s", string(dto))
	}
	t.Log("historical zero-time natively survives FileWorkStore restart + App.GetWork: OK")
}
