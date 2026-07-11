package acp

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func TestToolLocationsAndPlanEntries(t *testing.T) {
	sink := newUpdateSink(&fakeNotifier{}, "s1")
	sink.bindCwd(t.TempDir())
	locs := sink.toolLocations("read_file", `{"path":"src/main.go","offset":4}`)
	if len(locs) != 1 || locs[0].Line == nil || *locs[0].Line != 5 {
		t.Fatalf("locations = %+v", locs)
	}
	entries, ok := planEntriesFromTodoArgs(`{"todos":[{"content":"Phase","status":"in_progress","level":0},{"content":"Step","status":"bogus","level":1}]}`)
	if !ok || len(entries) != 2 || entries[0].Priority != "high" || entries[1].Priority != "medium" || entries[1].Status != "pending" {
		t.Fatalf("plan entries = %+v, ok=%v", entries, ok)
	}
}

func TestClientIOTerminalLifecycle(t *testing.T) {
	methods := []string{}
	zero := 0
	fn := &fakeNotifier{onReq: func(method string, params any) (json.RawMessage, error) {
		methods = append(methods, method)
		switch method {
		case "terminal/create":
			return json.RawMessage(`{"terminalId":"term-1"}`), nil
		case "terminal/wait_for_exit", "terminal/release":
			return json.RawMessage(`{}`), nil
		case "terminal/output":
			raw, _ := json.Marshal(TerminalOutputResult{Output: "ok", ExitStatus: &TerminalExitStatus{ExitCode: &zero}})
			return raw, nil
		default:
			t.Fatalf("unexpected terminal method %q", method)
			return nil, nil
		}
	}}
	client := newClientIO(fn, "s1", ClientCapabilities{Terminal: true})
	out, ok, err := client.RunCommand(context.Background(), "echo ok", t.TempDir(), time.Second)
	if err != nil || !ok || out != "ok" {
		t.Fatalf("RunCommand = %q, %v, %v", out, ok, err)
	}
	want := []string{"terminal/create", "terminal/wait_for_exit", "terminal/output", "terminal/release"}
	if len(methods) != len(want) {
		t.Fatalf("methods = %v", methods)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Fatalf("methods = %v, want %v", methods, want)
		}
	}
}

func TestClientIOUsesAdvertisedFilesystemMethods(t *testing.T) {
	fn := &fakeNotifier{onReq: func(method string, params any) (json.RawMessage, error) {
		switch method {
		case "fs/read_text_file":
			return json.RawMessage(`{"content":"unsaved"}`), nil
		case "fs/write_text_file":
			return json.RawMessage(`{}`), nil
		default:
			t.Fatalf("unexpected method %q", method)
			return nil, nil
		}
	}}
	client := newClientIO(fn, "s1", ClientCapabilities{FS: FSCapabilities{ReadTextFile: true, WriteTextFile: true}})
	if got, ok := client.ReadTextFile(context.Background(), "a.go"); !ok || got != "unsaved" {
		t.Fatalf("ReadTextFile = %q, %v", got, ok)
	}
	if ok, err := client.WriteTextFile(context.Background(), "a.go", "saved"); !ok || err != nil {
		t.Fatalf("WriteTextFile = %v, %v", ok, err)
	}
}

func TestSessionModesState(t *testing.T) {
	state := sessionModesState(sessionModePlan)
	if state.CurrentModeID != sessionModePlan || len(state.AvailableModes) != 3 {
		t.Fatalf("modes = %+v", state)
	}
}
