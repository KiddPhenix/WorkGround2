package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"workground2/internal/jobs"
	"workground2/internal/provider"
	"workground2/internal/sandbox"
	"workground2/internal/tool"

	_ "workground2/internal/tool/builtin"
)

// stopWaitProvider scripts a three-request turn: start a real background bash
// job, call the `wait` tool on that job, then give a final answer. The wait
// tool call's job id is unknown until request 2 arrives (the agent reports the
// assigned id in the bash tool result), so the provider parses it from the
// session the model sends back.
type stopWaitProvider struct {
	mu    sync.Mutex
	calls int
}

func (p *stopWaitProvider) Name() string { return "stop-wait" }

var waitJobIDPattern = regexp.MustCompile(`bash-(\d+)`)

func (p *stopWaitProvider) Stream(ctx context.Context, req provider.Request) (<-chan provider.Chunk, error) {
	p.mu.Lock()
	p.calls++
	call := p.calls
	p.mu.Unlock()
	ch := make(chan provider.Chunk, 4)
	switch call {
	case 1:
		ch <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{
			ID: "c1", Name: "bash",
			Arguments: `{"command":"` + waitSleepCommand() + `","run_in_background":true}`,
		}}
	case 2:
		jobID := ""
		for i := len(req.Messages) - 1; i >= 0; i-- {
			if req.Messages[i].Role != provider.RoleTool {
				continue
			}
			if m := waitJobIDPattern.FindStringSubmatch(req.Messages[i].Content); m != nil {
				jobID = "bash-" + m[1]
				break
			}
		}
		if jobID == "" {
			ch <- provider.Chunk{Type: provider.ChunkError, Err: fmt.Errorf("no background job id in tool results")}
			close(ch)
			return ch, nil
		}
		args, _ := json.Marshal(map[string]any{"job_ids": []string{jobID}})
		ch <- provider.Chunk{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "c2", Name: "wait", Arguments: string(args)}}
	case 3:
		ch <- provider.Chunk{Type: provider.ChunkText, Text: "final after wait stop"}
	default:
		ch <- provider.Chunk{Type: provider.ChunkError, Err: fmt.Errorf("unexpected extra request %d", call)}
	}
	ch <- provider.Chunk{Type: provider.ChunkDone}
	close(ch)
	return ch, nil
}

func waitSleepCommand() string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds 30"
	}
	return "sleep 30"
}

func hasStopWaitShell() bool {
	if runtime.GOOS == "windows" {
		_, err := exec.LookPath("powershell")
		if err != nil {
			_, err = exec.LookPath("pwsh")
		}
		return err == nil
	}
	_, err := exec.LookPath("sh")
	return err == nil
}

// TestStopWaitToolLeavesBackgroundJobAlive proves stopping the `wait` tool call
// only ends the waiting — the independent background job keeps running (its
// context is the job manager's session context, never the call's).
func TestStopWaitToolLeavesBackgroundJobAlive(t *testing.T) {
	if !hasStopWaitShell() {
		t.Skip("no shell for background jobs on this host")
	}
	if sandbox.ResolveShell("", "", nil).Kind == sandbox.ShellBash && runtime.GOOS != "windows" {
		// POSIX bash path is fine too; keep the test on the resolved shell.
	}
	m := jobs.NewManager(nil)
	defer m.Close()
	reg := tool.NewRegistry()
	for _, bt := range tool.Builtins() {
		switch bt.Name() {
		case "bash", "wait", "bash_output", "kill_shell":
			reg.Add(bt)
		}
	}
	a := New(&stopWaitProvider{}, reg, NewSession("sys"), Options{Jobs: m}, &recordSink{})
	done := make(chan error, 1)
	go func() { done <- a.Run(context.Background(), "run") }()

	// Wait until the background job is registered AND the wait call is active.
	deadline := time.Now().Add(15 * time.Second)
	var waitActive bool
	for time.Now().Before(deadline) {
		snap := a.ProgressSnapshot()
		for _, at := range snap.ActiveTools {
			if at.CallID == "c2" {
				waitActive = true
			}
		}
		if waitActive && len(m.Running()) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !waitActive {
		t.Fatal("wait tool call never became active")
	}
	if got := a.StopToolCall(stopIDFor(t, a, "c2")); got.Status != ToolStopAccepted {
		t.Fatalf("stop wait = %+v, want accepted", got)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run = %v, want nil (turn continues after stopping wait)", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("turn did not finish after stopping the wait call")
	}
	// The background job must still be running — stopping `wait` never touches it.
	running := m.Running()
	if len(running) == 0 {
		t.Fatal("background job was killed when its wait call was stopped")
	}
	// The model saw the stopped wait result once and continued.
	found := false
	for _, msg := range a.Session().Messages {
		if msg.Role == provider.RoleTool && msg.ToolCallID == "c2" && strings.Contains(msg.Content, "user manually stopped") {
			found = true
		}
	}
	if !found {
		t.Fatal("session lacks the user-stopped result for the wait call")
	}
	// Cleanup: kill the background job through the kill_shell tool.
	if killTool, ok := tool.LookupBuiltin("kill_shell"); ok {
		killCtx := jobs.WithManager(context.Background(), m)
		args, _ := json.Marshal(map[string]any{"job_id": running[0].ID})
		_, _ = killTool.Execute(killCtx, args)
	}
}
