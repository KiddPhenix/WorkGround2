package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestDispatchScriptReturnsSessionIDBeforePolling(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell dispatch integration is Windows-only")
	}
	dir := t.TempDir()
	fake := writeDispatchFake(t, dir)
	packet := filepath.Join(dir, "packet.md")
	if err := os.WriteFile(packet, []byte("bounded task"), 0o644); err != nil {
		t.Fatal(err)
	}

	started := time.Now()
	got := runDispatchSkill(t,
		"-Workspace", dir,
		"-SessionName", "worker",
		"-PacketFile", packet,
		"-CliPath", fake,
	)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("dispatch took %s, want immediate acknowledgement", elapsed)
	}
	if got["outcome"] != "dispatched" || got["sessionId"] != "session-fake" {
		t.Fatalf("dispatch outcome = %+v", got)
	}
}

func TestDispatchScriptPollOnlyReturnsOneRunningSnapshot(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell dispatch integration is Windows-only")
	}
	dir := t.TempDir()
	fake := writeDispatchFake(t, dir)

	started := time.Now()
	got := runDispatchSkill(t,
		"-Workspace", dir,
		"-PollOnly",
		"-SessionID", "session-fake",
		"-CliPath", fake,
	)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("PollOnly took %s, want one bounded snapshot", elapsed)
	}
	if got["outcome"] != "running" || got["sessionId"] != "session-fake" || got["starting"] != true || got["queued"] != true {
		t.Fatalf("PollOnly outcome = %+v", got)
	}
}

func TestDispatchScriptPreservesProgress(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell dispatch integration is Windows-only")
	}
	for _, state := range []string{"running", "completed", "interaction_required", "unknown", "legacy"} {
		t.Run(state, func(t *testing.T) {
			dir := t.TempDir()
			progress := map[string]any{
				"inputTokens":  float64(1234),
				"outputTokens": float64(56),
				"phase":        "tool",
				"activeTools": []any{map[string]any{
					"callId": "wait-1", "name": "wait", "elapsedMs": float64(15000),
				}},
			}
			status := map[string]any{
				"sessionId": "session-fake", "path": `C:\fake.jsonl`,
				"running": true, "foregroundActive": true, "pendingPrompt": false,
				"progress": progress,
			}
			wantOutcome := state
			switch state {
			case "completed":
				status["running"], status["foregroundActive"] = false, false
			case "interaction_required":
				status["pendingPrompt"] = true
				status["pendingInteraction"] = map[string]any{
					"id": "approval-1", "kind": "approval", "tool": "bash", "subject": "command", "reason": "approval",
				}
			case "unknown":
				status["progress"] = nil
				wantOutcome = "running"
			case "legacy":
				delete(status, "progress")
				wantOutcome = "running"
			}
			got := runDispatchSkill(t, "-Workspace", dir, "-PollOnly", "-SessionID", "session-fake", "-CliPath", writeDispatchFake(t, dir, status))
			if got["outcome"] != wantOutcome {
				t.Fatalf("outcome = %v, want %s", got["outcome"], wantOutcome)
			}
			want, wantPresent := status["progress"]
			actual, present := got["progress"]
			if present != wantPresent || !reflect.DeepEqual(actual, want) {
				t.Fatalf("progress = %#v (present=%v), want %#v (present=%v)", actual, present, want, wantPresent)
			}
		})
	}
}

func writeDispatchFake(t *testing.T, dir string, statuses ...map[string]any) string {
	t.Helper()
	path := filepath.Join(dir, "fake-cli.ps1")
	script := `
if ($args[0] -ne 'desktop') { throw 'unexpected command' }
switch ($args[1]) {
    'workspaces' { Write-Output '* fake workspace'; return }
    'new' {
        Write-Output 'SessionID: session-fake'
        Write-Output 'Created session: C:\fake.jsonl'
        Write-Output 'Submitted to session: C:\fake.jsonl'
        return
    }
    'status' {
        Write-Output '{"sessionId":"session-fake","path":"C:\\fake.jsonl","running":false,"foregroundActive":true,"backgroundOnly":false,"pendingPrompt":false,"starting":true,"queued":true,"mode":"starting"}'
        return
    }
}
throw 'unexpected desktop subcommand'
`
	if len(statuses) > 0 {
		data, err := json.Marshal(statuses[0])
		if err != nil {
			t.Fatal(err)
		}
		start := strings.Index(script, "        Write-Output '{\"sessionId\"")
		end := strings.Index(script[start:], "\n") + start
		script = script[:start] + "        Write-Output '" + strings.ReplaceAll(string(data), "'", "''") + "'" + script[end:]
	}
	if err := os.WriteFile(path, []byte(script), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func runDispatchSkill(t *testing.T, args ...string) map[string]any {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("ai_collaboration_skill", "scripts", "dispatch.ps1"))
	if err != nil {
		t.Fatal(err)
	}
	callArgs := []string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}
	callArgs = append(callArgs, args...)
	cmd := exec.Command("powershell", callArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dispatch.ps1: %v\n%s", err, out)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode dispatch output: %v\n%s", err, out)
	}
	return got
}
