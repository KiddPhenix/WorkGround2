package assistantdaemon

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/agent"
	"workground2/internal/agent/testutil"
	"workground2/internal/assistant"
	"workground2/internal/provider"
)

// daemonSupervisorTestProviderKind is a registered fake provider used by the
// real-Controller supervisor tests. The controller turns come from a script so
// the test observes exactly what the supervisor Session executed.
const daemonSupervisorTestProviderKind = "daemon-supervisor-test"

var (
	daemonSupervisorTestProviderOnce    sync.Once
	daemonSupervisorTestProviderCurrent *testutil.MockProvider
	daemonSupervisorTestProviderMu      sync.Mutex
)

func registerDaemonSupervisorTestProvider() {
	daemonSupervisorTestProviderOnce.Do(func() {
		provider.Register(daemonSupervisorTestProviderKind, func(provider.Config) (provider.Provider, error) {
			daemonSupervisorTestProviderMu.Lock()
			defer daemonSupervisorTestProviderMu.Unlock()
			if daemonSupervisorTestProviderCurrent == nil {
				return nil, os.ErrNotExist
			}
			return daemonSupervisorTestProviderCurrent, nil
		})
	})
}

func setDaemonSupervisorTestProvider(t *testing.T, p *testutil.MockProvider) {
	t.Helper()
	registerDaemonSupervisorTestProvider()
	daemonSupervisorTestProviderMu.Lock()
	daemonSupervisorTestProviderCurrent = p
	daemonSupervisorTestProviderMu.Unlock()
	t.Cleanup(func() {
		daemonSupervisorTestProviderMu.Lock()
		if daemonSupervisorTestProviderCurrent == p {
			daemonSupervisorTestProviderCurrent = nil
		}
		daemonSupervisorTestProviderMu.Unlock()
	})
}

// newDaemonSupervisorTestRuntime wires a minimal daemon Runtime with the fake
// provider and the shared supervisor executor, without touching network or the
// real user config. A temp WorkGround2.toml maps the provider entry to the
// registered fake kind.
func newDaemonSupervisorTestRuntime(t *testing.T) (*Runtime, *assistant.Store) {
	t.Helper()
	bootstrapDaemonSupervisorConfig(t)
	store, err := assistant.NewStore(filepath.Join(t.TempDir(), "assistants"))
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	ctrl := newDaemonSessionControl(daemonSupervisorTestModel, io.Discard, store, nil)
	t.Cleanup(func() { _ = ctrl.Close() })
	r := &Runtime{
		store: store, sessionControl: ctrl,
		opts: Options{Model: daemonSupervisorTestModel, Stderr: io.Discard},
	}
	events, err := assistant.NewSupervisorEventQueue(store.Root())
	if err != nil {
		t.Fatal(err)
	}
	executor, err := assistant.NewSupervisorExecutor(assistant.SupervisorExecutorOptions{
		Store:  store,
		Events: events,
		Host:   &daemonSupervisorHost{r: r},
		Control: func() assistant.SessionControl {
			return &daemonSupervisorSessionControl{inner: r.sessionControl}
		},
		TrialStatus: func() assistant.TrialStatusResolver {
			return r.daemonTrialSessionStatus
		},
		Diagnostic: func(operation string, err error) {
			t.Logf("supervisor diagnostic %s: %v", operation, err)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.executor = executor
	return r, store
}

// daemonSupervisorTestModel is the provider name the temp config maps to the
// registered fake kind.
const daemonSupervisorTestModel = "test-model"

// bootstrapDaemonSupervisorConfig isolates the config home and installs a temp
// WorkGround2.toml whose default model resolves to the fake provider kind.
// The config home is a manually-created temp dir (not t.TempDir): boot.Build
// starts background work-recovery goroutines that can still touch it when the
// test ends, and Windows t.TempDir cleanup fails loudly on that race. Cleanup
// here is best-effort with a short settle window.
func bootstrapDaemonSupervisorConfig(t *testing.T) {
	t.Helper()
	dir, err := os.MkdirTemp("", "daemon-supervisor-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		time.Sleep(200 * time.Millisecond)
		_ = os.RemoveAll(dir)
	})
	t.Setenv("HOME", dir)
	t.Setenv("USERPROFILE", dir)
	t.Setenv("APPDATA", dir)
	t.Setenv("LOCALAPPDATA", dir)
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("WorkGround2_HOME", dir)
	t.Setenv("WorkGround2_STATE_HOME", dir)
	t.Setenv("WorkGround2_CACHE_HOME", dir)
	t.Setenv("WorkGround2_CREDENTIALS_STORE", "file")
	t.Chdir(dir)
	toml := `default_model = "test-model"

[[providers]]
name = "test-model"
kind = "daemon-supervisor-test"
model = "x"
base_url = "http://localhost:11434"
`
	if err := os.WriteFile(filepath.Join(dir, "WorkGround2.toml"), []byte(toml), 0o600); err != nil {
		t.Fatal(err)
	}
}

func mustCreateDaemonAssistant(t *testing.T, store *assistant.Store, id string) assistant.Assistant {
	t.Helper()
	snapshot, err := store.Create(assistant.CreateInput{
		RequestID: "create-" + id,
		Assistant: assistant.Assistant{
			ID: id, Name: "Daemon helper", Mission: "Keep the project healthy",
			Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy(),
		},
		Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return snapshot.Assistant
}

// TestDaemonSupervisorHostEnsureAtomicConcurrent proves the real host guarantee:
// many concurrent EnsureSupervisorSession calls for the same assistant create
// exactly one supervisor Session file (the deterministic O_EXCL path), and all
// callers resolve to the same Session.
func TestDaemonSupervisorHostEnsureAtomicConcurrent(t *testing.T) {
	setDaemonSupervisorTestProvider(t, testutil.NewMock("daemon-supervisor-mock"))
	r, store := newDaemonSupervisorTestRuntime(t)
	a := mustCreateDaemonAssistant(t, store, "helper-concurrent")

	const n = 12
	refs := make([]assistant.SupervisorSessionRef, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			refs[i], errs[i] = r.executor.Host().EnsureSupervisorSession(a)
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("ensure %d: %v", i, errs[i])
		}
		if refs[i].ID != refs[0].ID || refs[i].Path != refs[0].Path {
			t.Fatalf("ensure %d resolved to %+v, want %+v", i, refs[i], refs[0])
		}
	}
	// The supervisor Session is durable, stamped and discoverable.
	if _, ok := agent.FindSupervisorSessionByMeta(filepath.Dir(refs[0].Path), a.ID); !ok {
		t.Fatal("supervisor session not discoverable after atomic create")
	}
	if got := agent.BranchID(refs[0].Path); got != refs[0].ID {
		t.Fatalf("ref id %q != BranchID %q", refs[0].ID, got)
	}
}

// TestDaemonSupervisorHostRealControllerTurn proves the supervisor reasoning
// really executes through the supervisor Session's Controller: the prompt is
// submitted, the model calls a read-only tool, the final decision is captured,
// and the full turn (user prompt, tool call, decision) is persisted in the same
// Session file — the restart resume point.
func TestDaemonSupervisorHostRealControllerTurn(t *testing.T) {
	mock := testutil.NewMock("daemon-supervisor-mock",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "call-1", Name: "session_list", Arguments: "{}"}}},
		testutil.Turn{Text: `{"action":"wait","rationale":"nothing to do"}`},
	)
	setDaemonSupervisorTestProvider(t, mock)
	r, store := newDaemonSupervisorTestRuntime(t)
	a := mustCreateDaemonAssistant(t, store, "helper-real")

	ref, err := r.executor.Host().EnsureSupervisorSession(a)
	if err != nil {
		t.Fatalf("EnsureSupervisorSession: %v", err)
	}

	prompt := "请基于当前状态决定下一步"
	outcome := r.executor.Host().RunSupervisorTurn(ref, prompt, 30*time.Second)
	if outcome.Err != nil {
		t.Fatalf("RunSupervisorTurn: %v", outcome.Err)
	}
	if outcome.Running || outcome.Pending {
		t.Fatalf("outcome running=%v pending=%v, want a settled turn", outcome.Running, outcome.Pending)
	}
	if strings.TrimSpace(outcome.Text) == "" {
		t.Fatal("supervisor turn produced no final message")
	}
	decision, err := assistant.ParseSupervisorDecision(outcome.Text)
	if err != nil {
		t.Fatalf("decision from session turn: %v (text=%.120s)", err, outcome.Text)
	}
	if decision.Action != assistant.ActionWait {
		t.Fatalf("decision = %+v, want wait", decision)
	}

	// The whole turn is durable in the Session file: user prompt, the tool
	// call/result, and the final assistant decision.
	sess, err := agent.LoadSession(ref.Path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	msgs := sess.Snapshot()
	var sawPrompt, sawTool, sawDecision bool
	for _, m := range msgs {
		if m.Role == provider.RoleUser && strings.Contains(m.Content, prompt) {
			sawPrompt = true
		}
		if m.Role == provider.RoleTool && m.Name == "session_list" {
			sawTool = true
		}
		if m.Role == provider.RoleAssistant && strings.Contains(m.Content, `"action":"wait"`) {
			sawDecision = true
		}
	}
	if !sawPrompt || !sawDecision {
		t.Fatalf("session file missing prompt=%v decision=%v (tool=%v)", sawPrompt, sawDecision, sawTool)
	}
	if !sawTool {
		t.Fatal("tool call did not land in the session file")
	}
	if !containsStr(outcome.ToolNames, "session_list") {
		t.Fatalf("outcome tool names = %v, want session_list", outcome.ToolNames)
	}
}

// TestDaemonSupervisorHostRestartContinuesSameSession proves restart recovery:
// a second host (fresh in-memory controller) restores the SAME durable session
// file, and the next supervisor turn appends to the same history instead of
// starting over.
func TestDaemonSupervisorHostRestartContinuesSameSession(t *testing.T) {
	mock := testutil.NewMock("daemon-supervisor-mock",
		testutil.Turn{Text: `{"action":"wait","rationale":"first"}`},
		testutil.Turn{Text: `{"action":"wait","rationale":"second"}`},
	)
	setDaemonSupervisorTestProvider(t, mock)
	r, store := newDaemonSupervisorTestRuntime(t)
	a := mustCreateDaemonAssistant(t, store, "helper-restart")

	ref, err := r.executor.Host().EnsureSupervisorSession(a)
	if err != nil {
		t.Fatal(err)
	}
	if out := r.executor.Host().RunSupervisorTurn(ref, "first turn", 30*time.Second); out.Err != nil {
		t.Fatalf("first turn: %v", out.Err)
	}

	// Simulated restart: a brand new runtime/control with no live controllers.
	ctrl2 := newDaemonSessionControl(daemonSupervisorTestModel, io.Discard, store, nil)
	r2 := &Runtime{store: store, sessionControl: ctrl2, opts: Options{Model: daemonSupervisorTestModel, Stderr: io.Discard}}
	ref2, ok := (&daemonSupervisorHost{r: r2}).FindSupervisorSession(a.ID)
	if !ok {
		t.Fatal("restart could not find the supervisor session")
	}
	if ref2.ID != ref.ID || ref2.Path != ref.Path {
		t.Fatalf("restart resolved %+v, want %+v", ref2, ref)
	}
	if out := (&daemonSupervisorHost{r: r2}).RunSupervisorTurn(ref2, "second turn", 30*time.Second); out.Err != nil {
		t.Fatalf("second turn: %v", out.Err)
	}
	sess, err := agent.LoadSession(ref.Path)
	if err != nil {
		t.Fatal(err)
	}
	msgs := sess.Snapshot()
	userCount := 0
	for _, m := range msgs {
		if m.Role == provider.RoleUser {
			userCount++
		}
	}
	if userCount != 2 {
		t.Fatalf("session history has %d user turns, want 2 (restart appended to the same session)", userCount)
	}
}

// TestDaemonSupervisorExecutorRunTurnsEndToEnd proves the full loop through the
// shared executor with a real controller: a user input event wakes a supervisor
// turn, the decision routes an advance through the Session subsystem, and the
// trigger event is consumed.
func TestDaemonSupervisorExecutorRunTurnsEndToEnd(t *testing.T) {
	mock := testutil.NewMock("daemon-supervisor-mock",
		testutil.Turn{Text: `{"action":"advance","target":"scan","rationale":"scan is ready"}`},
		// The advance creates a managed Session through daemonSessionControl,
		// whose Create submits the work prompt: one more model turn.
		testutil.Turn{Text: "scanning done"},
	)
	setDaemonSupervisorTestProvider(t, mock)
	r, store := newDaemonSupervisorTestRuntime(t)
	a := mustCreateDaemonAssistant(t, store, "helper-e2e")
	now := time.Now()
	if err := store.RecordProgress(assistant.RecordProgressInput{
		RequestID: "plan-e2e", AssistantID: a.ID,
		Progress: assistant.ProgressBlock{
			PlanRevision:     1,
			Responsibilities: []assistant.RespDecl{{Alias: "scan", Objective: "Scan", NextAction: "run the scan"}},
		},
		Now: now,
	}); err != nil {
		t.Fatal(err)
	}

	if err := r.executor.EnqueueUserInput(a.ID, "req-e2e", "scan now"); err != nil {
		t.Fatal(err)
	}
	r.executor.RunTurns(context.Background(), now)

	ref, ok := r.executor.Host().FindSupervisorSession(a.ID)
	if !ok {
		t.Fatal("supervisor session was not created")
	}
	managed := r.executor.Host().ManagedSessions(a.ID)
	if len(managed) != 1 {
		t.Fatalf("managed sessions = %d, want 1 (advance created it)", len(managed))
	}
	if hasPending, err := r.executor.Events().HasPending(a.ID); err != nil || hasPending {
		t.Fatalf("trigger events still pending after routed turn: %v err=%v", hasPending, err)
	}
	_ = ref
}

func containsStr(items []string, want string) bool {
	for _, it := range items {
		if it == want {
			return true
		}
	}
	return false
}

var _ = context.Background
