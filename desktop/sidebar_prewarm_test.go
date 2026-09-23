package main

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"workground2/internal/agent"
)

// The group list is project discovery, not session indexing. These tests pin the
// decoupling: the first paint answers from the published snapshot and the cheap
// plan metadata, the library scan runs in the background, a publish converges the
// counts through the existing sidebar-change notification, and a failed round
// stays observable and retryable.

// testSidebarSlowSidecar returns an index whose first sidecar decode blocks until
// the returned release func runs. It is the controlled slow dependency: with it
// held, any caller that waits for the session index cannot return.
func testSidebarSlowSidecar(t *testing.T, dir, groupID string) (*sidebarBoltIndex, *App, func()) {
	t.Helper()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic-1", TopicTitle: "Live",
		Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	plan := testSidebarProjectPlan(groupID, "Project", dir, []string{dir})
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{groupID: "v1"}}
	app := &App{}

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		once.Do(func() { close(entered) })
		<-release
		return agent.LoadBranchMeta(path)
	}
	releaseOnce := sync.Once{}
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		unblock()
		_ = index.close(app)
	})
	t.Cleanup(func() {
		select {
		case <-entered:
		case <-time.After(5 * time.Second):
			t.Error("background prewarm never reached the slow sidecar dependency")
		}
	})
	return index, app, unblock
}

// TestSidebarFirstPaintDoesNotDecodeSessions is the deterministic counterpart of
// the slow-dependency test: project discovery must not read a single session
// sidecar, while the background round still covers the plan.
func TestSidebarFirstPaintDoesNotDecodeSessions(t *testing.T) {
	dir := t.TempDir()
	for i := range 5 {
		testSidebarWriteSessionMeta(t, dir, fmt.Sprintf("session-%d.jsonl", i), agent.BranchMeta{
			ID: fmt.Sprintf("session-%d", i), Scope: "project", WorkspaceRoot: dir,
			TopicID: fmt.Sprintf("topic-%d", i), TopicTitle: "Live", Turns: 1,
			SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
	}
	plan := testSidebarProjectPlan("project_noscan", "Project", dir, []string{dir})
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_noscan": "v1"}}
	app := &App{}
	gate := make(chan struct{})
	openGate := sync.OnceFunc(func() { close(gate) })
	var decodes atomic.Int32
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		<-gate
		decodes.Add(1)
		return agent.LoadBranchMeta(path)
	}
	t.Cleanup(func() {
		openGate()
		_ = index.close(app)
	})

	groups, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(groups) != 1 || groups[0].ID != "project_noscan" {
		t.Fatalf("first paint groups=%+v err=%v", groups, err)
	}
	// The background round is parked before its first sidecar decode, so any decoded
	// sidecar here would mean the group list did the session work itself.
	if got := decodes.Load(); got != 0 {
		t.Fatalf("project discovery decoded %d session sidecars", got)
	}
	openGate()
	testSidebarWaitPrewarm(t, index, app)
	if got := decodes.Load(); got != 5 {
		t.Fatalf("background prewarm decoded %d of 5 sessions", got)
	}
	converged, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(converged) != 1 || converged[0].SessionCount != 5 {
		t.Fatalf("converged groups=%+v err=%v", converged, err)
	}
}

func TestSidebarGroupsDoNotWaitForSessionIndex(t *testing.T) {
	dir := t.TempDir()
	index, app, unblock := testSidebarSlowSidecar(t, dir, "project_slow")
	var notified atomic.Int32
	index.notify = func(*App) { notified.Add(1) }

	done := make(chan struct{})
	var groups []SidebarGroup
	var err error
	go func() {
		defer close(done)
		groups, err = index.listGroups(app, SidebarProjects)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("listGroups blocked behind the session index instead of answering project discovery")
	}
	if err != nil || len(groups) != 1 {
		t.Fatalf("first paint groups=%+v err=%v", groups, err)
	}
	// Project identity, visibility and order come from cheap plan metadata; only
	// the exact visible count waits for the index.
	if groups[0].ID != "project_slow" || groups[0].Label != "Project" || groups[0].Root != dir || groups[0].Kind != "project" {
		t.Fatalf("first paint lost project identity: %+v", groups[0])
	}
	if groups[0].SessionCount != 0 {
		t.Fatalf("cold first paint reported an unpublished count %d", groups[0].SessionCount)
	}

	unblock()
	testSidebarWaitPrewarm(t, index, app)
	if notified.Load() == 0 {
		t.Fatal("a published prewarm round never notified the frontend")
	}
	converged, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(converged) != 1 || converged[0].SessionCount != 1 {
		t.Fatalf("converged groups=%+v err=%v", converged, err)
	}
}

func TestSidebarGroupsServePublishedSnapshotAcrossModes(t *testing.T) {
	dir := t.TempDir()
	index, app, unblock := testSidebarSlowSidecar(t, dir, "project_modes")
	// The plan carries a stale, inflated summary exactly like the real Desktop
	// project file does; it is the first-paint fallback until a publish lands.
	index.source.(*sidebarTestSource).plansValue[0].group.SessionCount = 19_222

	roomsBefore, err := index.listGroups(app, SidebarRooms)
	if err != nil {
		t.Fatal(err)
	}
	if len(roomsBefore) != 0 {
		t.Fatalf("ROOM view advertised an unpublished group: %+v", roomsBefore)
	}
	projectsBefore, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(projectsBefore) != 1 || projectsBefore[0].SessionCount != 19_222 {
		t.Fatalf("projects first paint=%+v err=%v", projectsBefore, err)
	}

	unblock()
	testSidebarWaitPrewarm(t, index, app)

	converged, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(converged) != 1 || converged[0].SessionCount != 1 {
		t.Fatalf("projects converged=%+v err=%v", converged, err)
	}
}

// TestSidebarPrewarmCoalescesRequests proves the asynchronous path cannot pile up
// one library sync per group-list call: while a round is stuck on the slow
// dependency, any number of requests fold into a single follow-up round.
func TestSidebarPrewarmCoalescesRequests(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic-1", TopicTitle: "Live",
		Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	plan := testSidebarProjectPlan("project_coalesce", "Project", dir, []string{dir})
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })
	var stamps atomic.Int32
	index.source = &sidebarTestSource{
		plansValue: []sidebarGroupPlan{plan},
		stampFunc:  func(sidebarGroupPlan) string { stamps.Add(1); return "v1" },
	}
	app := &App{}

	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		once.Do(func() { close(entered) })
		<-release
		return agent.LoadBranchMeta(path)
	}
	unblock := sync.OnceFunc(func() { close(release) })
	t.Cleanup(func() {
		unblock()
		_ = index.close(app)
	})

	if _, err := index.listGroups(app, SidebarProjects); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("prewarm never started")
	}
	for range 20 {
		if _, err := index.listGroups(app, SidebarProjects); err != nil {
			t.Fatal(err)
		}
	}
	unblock()
	testSidebarWaitPrewarm(t, index, app)
	// Round one plus the single coalesced round: 20 concurrent requests must not
	// become 20 library scans.
	if got := stamps.Load(); got > 3 {
		t.Fatalf("prewarm ran %d sync rounds for 21 group requests, want the coalesced minimum", got)
	}
}

// TestSidebarPrewarmFailureStaysObservableAndRetryable covers the failure path:
// the group list still answers, the failure is reported instead of swallowed, and
// a later request retries the sync that failed.
func TestSidebarPrewarmFailureStaysObservableAndRetryable(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic-1", TopicTitle: "Live",
		Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	plan := testSidebarProjectPlan("project_retry", "Project", dir, []string{dir})
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_retry": "v1"}}
	app := &App{}

	var buffer bytes.Buffer
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })

	fail := atomic.Bool{}
	fail.Store(true)
	index.scanBatchFault = func() error {
		if fail.Load() {
			return errors.New("injected prewarm failure")
		}
		return nil
	}
	t.Cleanup(func() { _ = index.close(app) })

	groups, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(groups) != 1 || groups[0].ID != "project_retry" {
		t.Fatalf("groups=%+v err=%v", groups, err)
	}
	testSidebarWaitPrewarm(t, index, app)
	if !strings.Contains(buffer.String(), "injected prewarm failure") {
		t.Fatalf("failed prewarm round was swallowed: log=%q", buffer.String())
	}
	// The failed round must not have published the rows...
	stillCold, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(stillCold) != 1 || stillCold[0].SessionCount != 0 {
		t.Fatalf("failed round published rows: %+v err=%v", stillCold, err)
	}
	// ...and the next request must retry it.
	fail.Store(false)
	if _, err := index.listGroups(app, SidebarProjects); err != nil {
		t.Fatal(err)
	}
	testSidebarWaitPrewarm(t, index, app)
	converged, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(converged) != 1 || converged[0].SessionCount != 1 {
		t.Fatalf("retry did not converge: %+v err=%v", converged, err)
	}
}

func TestSidebarPrewarmPublishesPartialSuccess(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic-1", TopicTitle: "Live",
		Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	broken := t.TempDir()
	testSidebarWriteSessionMeta(t, broken, "second.jsonl", agent.BranchMeta{
		ID: "second", Scope: "project", WorkspaceRoot: dir, TopicID: "topic-2", TopicTitle: "Second",
		Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	plan := testSidebarProjectPlan("project_partial", "Partial", dir, []string{dir, broken})
	dbPath := filepath.Join(t.TempDir(), "sidebar.db")
	index := newSidebarBoltIndex(func(*App) string { return dbPath })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{plan.group.ID: "v1"}}
	var failed atomic.Bool
	failed.Store(true)
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		if filepath.Dir(path) == broken && failed.Load() {
			return agent.BranchMeta{}, false, errors.New("injected I/O failure")
		}
		return agent.LoadBranchMeta(path)
	}
	app := &App{}
	t.Cleanup(func() { _ = index.close(app) })
	var notifications atomic.Int32
	index.notify = func(*App) { notifications.Add(1) }
	// The first directory publishes; the next one fails within the same plan.
	published, err := index.syncPlansMode(app, []sidebarGroupPlan{plan}, true, nil)
	if !published || err == nil {
		t.Fatalf("partial sync: published=%v err=%v", published, err)
	}
	if _, err := index.listGroups(app, SidebarProjects); err != nil {
		t.Fatal(err)
	}
	index.waitPrewarm(app)
	if notifications.Load() == 0 {
		t.Fatal("background failure did not notify the issue banner")
	}
	if _, err := index.listIssues(app, SidebarProjects); err == nil {
		t.Fatal("background failure was hidden from the issue banner")
	}
	failed.Store(false)
	if _, err := index.refreshIssues(app, SidebarProjects); err != nil {
		t.Fatalf("issue retry did not recover the background failure: %v", err)
	}
	groups, err := index.listGroups(app, SidebarProjects)
	if err != nil || len(groups) != 1 || groups[0].SessionCount != 2 {
		t.Fatalf("recovered groups=%+v err=%v", groups, err)
	}
}

// TestSidebarPrewarmYieldsOnClose covers the shutdown path: closing the index
// stops the background round cooperatively, so plans that were never started are
// abandoned instead of holding shutdown behind the whole library.
func TestSidebarPrewarmYieldsOnClose(t *testing.T) {
	const projects = 6
	root := t.TempDir()
	plans := make([]sidebarGroupPlan, 0, projects)
	for p := range projects {
		dir := filepath.Join(root, fmt.Sprintf("p%02d", p))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
			ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic", TopicTitle: "Live",
			Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
		})
		plans = append(plans, testSidebarProjectPlan(fmt.Sprintf("project_p%02d", p), fmt.Sprintf("P%02d", p), dir, []string{dir}))
	}
	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })
	index.source = &sidebarTestSource{plansValue: plans, stamps: map[string]string{}}
	for _, plan := range plans {
		index.source.(*sidebarTestSource).stamps[plan.group.ID] = "v1"
	}
	app := &App{}

	release := make(chan struct{})
	entered := make(chan struct{})
	var once sync.Once
	var decodes atomic.Int32
	index.loadBranchMeta = func(path string) (agent.BranchMeta, bool, error) {
		decodes.Add(1)
		once.Do(func() { close(entered) })
		<-release
		return agent.LoadBranchMeta(path)
	}

	if _, err := index.listGroups(app, SidebarProjects); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("prewarm never started")
	}

	closed := make(chan error, 1)
	go func() { closed <- index.close(app) }()
	state := index.lookupState(app)
	if state == nil {
		t.Fatal("index state disappeared while closing")
	}
	deadline := time.Now().Add(5 * time.Second)
	for !state.warm.isStopped() {
		if time.Now().After(deadline) {
			t.Fatal("close did not stop the background prewarm")
		}
		time.Sleep(time.Millisecond)
	}
	close(release)

	select {
	case err := <-closed:
		if err != nil {
			t.Fatalf("close err=%v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("close did not finish after the running plan was released")
	}
	// Only the plans already in flight may have been scanned; the queued ones must
	// have been abandoned.
	if got := decodes.Load(); got > maxSidebarLoadConcurrency {
		t.Fatalf("close scanned %d of %d session plans, want the in-flight wave at most", got, projects)
	}
}

// TestSidebarRuntimeStateDoesNotInvalidateIndex pins the other half of the
// decoupling: live runtime state is read-time decoration, so it must not change
// the persisted class signature, force a re-scan or bump the page snapshot.
func TestSidebarRuntimeStateDoesNotInvalidateIndex(t *testing.T) {
	dir := t.TempDir()
	testSidebarWriteSessionMeta(t, dir, "session.jsonl", agent.BranchMeta{
		ID: "session", Scope: "project", WorkspaceRoot: dir, TopicID: "topic-1", TopicTitle: "Live",
		Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	})
	path := filepath.Join(dir, "session.jsonl")
	tab := &WorkspaceTab{
		ID: "tab-1", SessionID: "session", SessionPath: path, Scope: "project", WorkspaceRoot: dir,
		TopicTitle: "Live", ActivityStatus: topicStatusStreaming,
	}
	app := &App{detachedSessions: map[string]*WorkspaceTab{tab.ID: tab}}
	source := sidebarDiskIndexSource{}
	plan := testSidebarProjectPlan("project_runtime", "Project", dir, []string{dir})

	running := source.stamp(app, plan)
	tab.ActivityStatus = ""
	if idle := source.stamp(app, plan); idle != running {
		t.Fatal("live runtime state invalidated the persisted class signature")
	}

	index := newSidebarBoltIndex(func(*App) string { return filepath.Join(t.TempDir(), "sidebar.db") })
	index.source = &sidebarTestSource{plansValue: []sidebarGroupPlan{plan}, stamps: map[string]string{"project_runtime": running}}
	index.prewarm = func(*App, []sidebarGroupPlan) {}
	t.Cleanup(func() { _ = index.close(app) })
	tab.ActivityStatus = topicStatusStreaming
	first, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_runtime"})
	if err != nil || len(first.Items) != 1 || first.Items[0].Status != topicStatusStreaming {
		t.Fatalf("running page=%+v err=%v", first, err)
	}
	published := testSidebarReadGeneration(t, index, app, "project_runtime")

	tab.ActivityStatus = ""
	second, err := index.listSessions(app, SidebarSessionQuery{Mode: SidebarProjects, GroupID: "project_runtime"})
	if err != nil || len(second.Items) != 1 {
		t.Fatalf("idle page=%+v err=%v", second, err)
	}
	if second.Items[0].Status != "" || second.Items[0].Running {
		t.Fatalf("idle row kept stale runtime decoration: %+v", second.Items[0])
	}
	// The read projection must refresh (a new in-memory query revision) while the
	// persisted index stays untouched: no re-scan, no generation rebuild.
	if second.Snapshot == first.Snapshot {
		t.Fatal("runtime-only change did not refresh the read projection")
	}
	if after := testSidebarReadGeneration(t, index, app, "project_runtime"); after != published {
		t.Fatalf("runtime-only change published a new generation %d -> %d", published, after)
	}
}

// BenchmarkSidebarGroupFirstPaint measures the two shapes on the same synthetic
// library: the previous synchronous behaviour (the group list drives the library
// sync itself) and the decoupled behaviour (project discovery only, index work in
// the background prewarm).
func BenchmarkSidebarGroupFirstPaint(b *testing.B) {
	const projects, perProject = 24, 80
	root := b.TempDir()
	plans := make([]sidebarGroupPlan, 0, projects)
	for p := range projects {
		dir := filepath.Join(root, fmt.Sprintf("p%02d", p))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			b.Fatal(err)
		}
		base := time.Now().UTC().Add(-time.Duration(p) * time.Hour)
		for s := range perProject {
			path := filepath.Join(dir, fmt.Sprintf("s%04d.jsonl", s))
			if err := os.WriteFile(path, []byte("\n"), 0o644); err != nil {
				b.Fatal(err)
			}
			if err := agent.SaveBranchMetaPreserveUpdated(path, agent.BranchMeta{
				ID: fmt.Sprintf("p%02d-s%04d", p, s), Scope: "project", WorkspaceRoot: dir,
				TopicID: fmt.Sprintf("topic-%d-%d", p, s), TopicTitle: "Session",
				Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion, CreatedAt: base, UpdatedAt: base,
			}); err != nil {
				b.Fatal(err)
			}
		}
		plans = append(plans, testSidebarProjectPlan(fmt.Sprintf("project_p%02d", p), fmt.Sprintf("P%02d", p), dir, []string{dir}))
	}
	source := &sidebarTestSource{plansValue: plans, stamps: map[string]string{}}
	for _, plan := range plans {
		source.stamps[plan.group.ID] = "v1"
	}

	b.Run("decoupled_first_paint_cold_index", func(b *testing.B) {
		root := b.TempDir()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			index := newSidebarBoltIndex(func(*App) string { return filepath.Join(root, fmt.Sprintf("decoupled-%d.db", i)) })
			index.source = source
			app := &App{}
			b.StartTimer()
			_, err := index.listGroups(app, SidebarProjects)
			b.StopTimer()
			if err != nil {
				b.Fatal(err)
			}
			// The background round and the close are outside the timed region: the
			// measurement is what the group list itself costs its caller.
			index.quiescePrewarm(app)
			if err := index.close(app); err != nil {
				b.Fatal(err)
			}
		}
	})

	b.Run("synchronous_library_scan_cold_index", func(b *testing.B) {
		root := b.TempDir()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			index := newSidebarBoltIndex(func(*App) string { return filepath.Join(root, fmt.Sprintf("sync-%d.db", i)) })
			index.source = source
			// Reproduces the previous behaviour: the group list only answers after
			// the whole library has been scanned into the index.
			index.prewarm = func(app *App, plans []sidebarGroupPlan) {
				if _, err := index.syncPlansMode(app, plans, true, nil); err != nil {
					b.Fatal(err)
				}
			}
			app := &App{}
			b.StartTimer()
			_, err := index.listGroups(app, SidebarProjects)
			b.StopTimer()
			if err != nil {
				b.Fatal(err)
			}
			if err := index.close(app); err != nil {
				b.Fatal(err)
			}
		}
	})
}
