package dsh

// Opt-in smoke tests that drive the real dsh-v0.1.0-rc.8 `dsh-jsonrpc-agent`
// runtime over its newline-delimited JSON-RPC stdio transport. They are excluded
// from the default suite: without DSH_SMOKE_ROOT they skip with the exact enable
// instruction below.
//
// Enable them against a checked-out rc.8 tree (Node >= 24, deps installed):
//
//	DSH_SMOKE_ROOT=D:\Work\dsh go test ./internal/runhub/dsh/ -run 'TestDSHRealRuntime' -count=1 -v
//
// Nothing here calls a real model. initialize/shutdown is keyless; the prompt
// path points DEEPSEEK_BASE_URL at an in-process loopback mock that replays the
// DSH keyless-smoke fixture (a single "smoke ok" text delta with finish_reason
// "stop"), so no network or credential is ever consulted.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/runhub"
)

// dshSmokeRoot returns the DSH checkout root for the opt-in runtime smoke, or
// skips the test with the enable instruction.
func dshSmokeRoot(t *testing.T) string {
	t.Helper()
	root := strings.TrimSpace(os.Getenv("DSH_SMOKE_ROOT"))
	if root == "" {
		t.Skip("set DSH_SMOKE_ROOT to a dsh-v0.1.0-rc.8 checkout (e.g. D:\\Work\\dsh) to run the real DSH runtime smoke; the default suite stays keyless and hermetic")
	}
	return root
}

// dshSmokeConfig builds the rc.8 probe input over the compiled dsh-jsonrpc-agent
// entry and the keyless jsonrpc-agent composition that ships in the checkout.
func dshSmokeConfig(root string) Config {
	return Config{
		NodePath:    "", // resolve node via PATH
		EntryPath:   filepath.Join(root, "packages", "examples", "jsonrpc-demo", "lib", "bin.js"),
		ConfigPath:  filepath.Join(root, "examples", "jsonrpc-agent", "cordis.yml"),
		VersionPath: filepath.Join(root, "package.json"),
	}
}

// smokeTimeouts are deliberately generous for a cold node/cordis boot.
func smokeTimeouts() Timeouts {
	return Timeouts{Initialize: 30 * time.Second, Prompt: 30 * time.Second, Shutdown: 5 * time.Second, KillGrace: 3 * time.Second}
}

// smokeModelServer replays the DSH keyless-smoke fixture: one text token with a
// "stop" finish, which the runtime surfaces as turn/end reason "completed".
func smokeModelServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"smoke ok\"}}]}\n\n")
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":1}}\n\n")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestDSHRealRuntimeInitializeShutdown drives the real runtime keylessly:
// probe → LaunchProcess → initialize → shutdown → clean exit. It never touches
// the model, so it runs without a mock or credential.
func TestDSHRealRuntimeInitializeShutdown(t *testing.T) {
	root := dshSmokeRoot(t)
	res, err := Probe(dshSmokeConfig(root))
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	if !res.Ready() {
		t.Fatalf("probe not ready: %s", issueText(res.Missing))
	}
	if res.Version != "0.1.0-rc.8" {
		t.Fatalf("probe version = %q, want 0.1.0-rc.8", res.Version)
	}

	workspace := t.TempDir()
	procHandle, err := LaunchProcess(ProcessSpec{
		NodePath:   res.NodePath,
		EntryPath:  res.EntryPath,
		ConfigPath: res.ConfigPath,
		Dir:        workspace,
		Env: append(os.Environ(),
			"DSH_CWD="+workspace,
			"DSH_SESSION_ROOT="+filepath.Join(workspace, ".sessions"),
			"DEEPSEEK_API_KEY=keyless-smoke-no-call",
		),
	})
	if err != nil {
		t.Fatalf("LaunchProcess: %v", err)
	}
	defer procHandle.Kill() // idempotent; the clean path already reaped it

	client := NewClient(procHandle.Stdin(), procHandle.Stdout(), DefaultMaxFrameSize)
	ctx, cancel := context.WithTimeout(context.Background(), smokeTimeouts().Initialize)
	defer cancel()

	var initRes InitializeResult
	if err := client.Call(ctx, MethodInitialize, InitializeParams{
		CWD:      workspace,
		Provider: "deepseek-official",
		Model:    "deepseek-v4-pro",
	}, &initRes); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if initRes.ServerInfo.Name != serverIdentity {
		t.Fatalf("serverInfo.name = %q, want %q", initRes.ServerInfo.Name, serverIdentity)
	}

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), smokeTimeouts().Shutdown)
	defer shutdownCancel()
	if err := client.Call(shutdownCtx, MethodShutdown, ShutdownParams{}, nil); err != nil {
		t.Fatalf("shutdown: %v", err)
	}
	_ = procHandle.CloseStdin()

	select {
	case exitErr := <-procHandle.Wait():
		if exitErr != nil {
			t.Fatalf("runtime exit = %v (stderr tail: %s)", exitErr, procHandle.StderrTail(0))
		}
	case <-time.After(smokeTimeouts().Shutdown + smokeTimeouts().KillGrace):
		t.Fatal("runtime did not exit cleanly after shutdown")
	}
	procHandle.Cleanup()
}

// TestDSHRealRuntimePrompt drives the full managed Runner over the real runtime
// with an in-process loopback mock model: probe → LaunchProcess → initialize →
// session/prompt → event mapping → succeeded terminal state → shutdown → clean
// process exit. No real model or network is involved.
func TestDSHRealRuntimePrompt(t *testing.T) {
	root := dshSmokeRoot(t)
	mock := smokeModelServer(t)

	dir := t.TempDir()
	store, err := runhub.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	hub, err := runhub.New(dir)
	if err != nil {
		t.Fatal(err)
	}

	workspace := t.TempDir()
	var procHandle Proc
	t.Cleanup(func() {
		if procHandle != nil {
			procHandle.Kill()
		}
	})
	r := NewRunner(RunnerConfig{
		Probe:    dshSmokeConfig(root),
		Provider: "deepseek-official",
		Model:    "deepseek-v4-pro",
		Env: append(os.Environ(),
			"DSH_CWD="+workspace,
			"DSH_SESSION_ROOT="+filepath.Join(workspace, ".sessions"),
			"DEEPSEEK_API_KEY=keyless-smoke-no-call",
			"DEEPSEEK_BASE_URL="+mock.URL,
		),
		Timeouts: smokeTimeouts(),
		Launcher: func(spec ProcessSpec) (Proc, error) {
			p, err := LaunchProcess(spec)
			if err != nil {
				return nil, err
			}
			procHandle = p
			return p, nil
		},
	}, store)

	rec, run := hub.Launch(runhub.LaunchIntent{RequestID: "smoke-req-1", Source: runhub.SourceDSH, Workspace: workspace, Prompt: "Reply with exactly: smoke ok"})
	if rec.Status != runhub.ReceiptAccepted {
		t.Fatalf("Launch status = %s: %s", rec.Status, rec.Message)
	}

	binding, err := r.Start(context.Background(), runhub.LaunchRequest{
		LaunchIntent: runhub.LaunchIntent{RequestID: "smoke-req-1", Source: runhub.SourceDSH, Workspace: workspace, Prompt: "Reply with exactly: smoke ok"},
	}, hub)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if binding.RunID != run.ID || binding.NativeSessionID != "wg2-"+string(run.ID) {
		t.Fatalf("binding = %+v, want run %s session wg2-%s", binding, run.ID, run.ID)
	}

	deadline := time.Now().Add(smokeTimeouts().Prompt + smokeTimeouts().Initialize)
	var got runhub.AgentRun
	for time.Now().Before(deadline) {
		got, _ = hub.Get(run.ID)
		if got.State.IsTerminal() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got.State != runhub.StateSucceeded {
		t.Fatalf("run state = %s, want succeeded (detail=%q)", got.State, terminalDetail(t, store, run.ID))
	}
	if got.Summary != "smoke ok" {
		t.Fatalf("run summary = %q, want %q", got.Summary, "smoke ok")
	}

	// The managed run must persist a binding and never leak the prompt into the
	// durable event log.
	brec, ok, err := store.LoadBinding(run.ID)
	if err != nil || !ok {
		t.Fatalf("LoadBinding: ok=%v err=%v", ok, err)
	}
	if brec.Binding.NativeSessionID != binding.NativeSessionID {
		t.Fatalf("durable binding session = %q, want %q", brec.Binding.NativeSessionID, binding.NativeSessionID)
	}
	events, err := store.ListEvents(run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if blob := eventBlob(events); strings.Contains(blob, "Reply with exactly") || strings.Contains(blob, "keyless-smoke-no-call") {
		t.Fatalf("durable events leak prompt or key: %s", blob)
	}

	// Settlement tears the process down: shutdown → stdin EOF → clean exit.
	if procHandle == nil {
		t.Fatal("launcher never captured a process handle")
	}
	select {
	case exitErr := <-procHandle.Wait():
		if exitErr != nil {
			t.Fatalf("runtime exit = %v (stderr tail: %s)", exitErr, procHandle.StderrTail(0))
		}
	case <-time.After(smokeTimeouts().Shutdown + smokeTimeouts().KillGrace):
		t.Fatal("runtime did not exit after settle teardown")
	}
}

func terminalDetail(t *testing.T, store *runhub.Store, id runhub.RunID) string {
	t.Helper()
	events, err := store.ListEvents(id)
	if err != nil {
		return err.Error()
	}
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case runhub.EventSucceeded, runhub.EventFailed, runhub.EventCancelled, runhub.EventInterrupted, runhub.EventStale:
			return events[i].Payload.Detail
		}
	}
	return ""
}

func eventBlob(events []runhub.RunEvent) string {
	var b strings.Builder
	for _, e := range events {
		b.WriteString(e.Payload.Summary)
		b.WriteString(" ")
		b.WriteString(e.Payload.Detail)
		b.WriteString(" ")
		b.WriteString(e.Payload.Label)
		b.WriteString(" ")
	}
	return b.String()
}
