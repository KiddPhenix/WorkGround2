package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"workground2/internal/provider"
)

func TestStreamTextProtocol(t *testing.T) {
	p := newTestProvider(t, "text")
	got := collectText(t, p, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if got != "plain response\n" {
		t.Fatalf("text = %q", got)
	}
}

// TestStreamTextProtocolEmitsBeforeExit confirms the text protocol emits the
// first line before the process exits (line-buffered streaming).
func TestStreamTextProtocolEmitsBeforeExit(t *testing.T) {
	p := newTestProvider(t, "stream-text")
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got strings.Builder
	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before first chunk")
		}
		if chunk.Type == provider.ChunkError {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if chunk.Type != provider.ChunkText || chunk.Text != "first \n" {
			t.Fatalf("first chunk = %#v", chunk)
		}
		got.WriteString(chunk.Text)
	case <-time.After(2500 * time.Millisecond):
		t.Fatal("first text chunk did not arrive before the helper process exited")
	}
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			got.WriteString(chunk.Text)
		case provider.ChunkError:
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
	if got.String() != "first \nsecond\n" {
		t.Fatalf("text = %q", got.String())
	}
}

// TestTextProtocolLinesStreamIndividually verifies the text protocol emits
// each output line as an individual ChunkText as it arrives, without waiting
// for the process to exit.
func TestTextProtocolLinesStreamIndividually(t *testing.T) {
	p := newTestProviderMode(t, "stream-lines", "text")
	ch, err := p.Stream(context.Background(), provider.Request{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	// First line must arrive before the 4s sleep finishes.
	var got strings.Builder
	var lines []string
	select {
	case chunk, ok := <-ch:
		if !ok {
			t.Fatal("stream closed before first line chunk")
		}
		if chunk.Type == provider.ChunkError {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
		if chunk.Type != provider.ChunkText {
			t.Fatalf("expected ChunkText, got %v", chunk.Type)
		}
		lines = append(lines, chunk.Text)
		got.WriteString(chunk.Text)
	case <-time.After(5 * time.Second):
		t.Fatal("first line did not arrive before the helper process sleep expired")
	}
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			lines = append(lines, chunk.Text)
			got.WriteString(chunk.Text)
		case provider.ChunkError:
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
	if got.String() != "line one\nline two\nline three\n" {
		t.Fatalf("text = %q", got.String())
	}
	if len(lines) != 3 {
		t.Fatalf("want 3 line chunks, got %d: %#v", len(lines), lines)
	}
}

func TestStreamJSONProtocol(t *testing.T) {
	p := newTestProvider(t, "json")
	got := collectText(t, p, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if got != "json response" {
		t.Fatalf("text = %q", got)
	}
}

func TestStreamJSONLProtocol(t *testing.T) {
	p := newTestProvider(t, "jsonl")
	got := collectText(t, p, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if got != "hello world" {
		t.Fatalf("text = %q", got)
	}
}

func TestStreamCodexJSONLAgentMessage(t *testing.T) {
	p := newTestProviderMode(t, "codex-jsonl", "jsonl")
	got := collectText(t, p, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if got != "hello from codex" {
		t.Fatalf("text = %q", got)
	}
}

func TestStreamCodexJSONLDedupesCumulativeItemText(t *testing.T) {
	p := newTestProviderMode(t, "codex-jsonl-cumulative", "jsonl")
	got := collectText(t, p, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if got != "hello world" {
		t.Fatalf("text = %q", got)
	}
}

func TestStreamSurfacesExitErrorWithStderr(t *testing.T) {
	p := newTestProvider(t, "fail")
	ch, err := p.Stream(context.Background(), provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var got string
	for chunk := range ch {
		if chunk.Type == provider.ChunkError && chunk.Err != nil {
			got = chunk.Err.Error()
		}
	}
	if !strings.Contains(got, "local cli failed") || !strings.Contains(got, "boom") {
		t.Fatalf("error = %q", got)
	}
}

func TestNormalizeInvocationUpgradesLegacyCodexExec(t *testing.T) {
	args, protocol := NormalizeInvocation(`C:\Users\admin\AppData\Local\OpenAI\Codex\bin\codex.exe`, []string{"exec", "--ignore-user-config", "--skip-git-repo-check"}, "text")
	if protocol != "jsonl" {
		t.Fatalf("protocol = %q, want jsonl", protocol)
	}
	if len(args) < 2 || args[0] != "exec" || args[1] != "--json" {
		t.Fatalf("args = %+v, want --json immediately after exec", args)
	}
}

func TestNormalizeInvocationKeepsExistingCodexJSONFlag(t *testing.T) {
	args, protocol := NormalizeInvocation("codex", []string{"exec", "--json", "--sandbox", "read-only"}, "jsonl")
	if protocol != "jsonl" {
		t.Fatalf("protocol = %q, want jsonl", protocol)
	}
	if strings.Count(strings.Join(args, "\x00"), "--json") != 1 {
		t.Fatalf("args = %+v, want one --json flag", args)
	}
}

func TestNormalizeInvocationForcesCodexJSONLProtocol(t *testing.T) {
	_, protocol := NormalizeInvocation("codex.cmd", []string{"exec", "--json"}, "json")
	if protocol != "jsonl" {
		t.Fatalf("protocol = %q, want jsonl", protocol)
	}
}

func TestNormalizeInvocationLeavesNonCodexCLIAlone(t *testing.T) {
	args, protocol := NormalizeInvocation("claude", []string{"--print"}, "text")
	if protocol != "text" || strings.Join(args, "\x00") != "--print" {
		t.Fatalf("args/protocol = %+v/%q, want unchanged", args, protocol)
	}
}

func newTestProvider(t *testing.T, protocol string) provider.Provider {
	t.Helper()
	return newTestProviderMode(t, protocol, protocol)
}

func newTestProviderMode(t *testing.T, mode string, protocol string) provider.Provider {
	t.Helper()
	p, err := New(provider.Config{
		Name:  "local-test",
		Model: "test-model",
		Extra: map[string]any{
			"command":         os.Args[0],
			"args":            []string{"-test.run=TestHelperProcess", "--", mode},
			"protocol":        protocol,
			"timeout_seconds": 10,
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

func collectText(t *testing.T, p provider.Provider, req provider.Request) string {
	t.Helper()
	ch, err := p.Stream(context.Background(), req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	var b strings.Builder
	for chunk := range ch {
		switch chunk.Type {
		case provider.ChunkText:
			b.WriteString(chunk.Text)
		case provider.ChunkError:
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
	return b.String()
}

func TestHelperProcess(t *testing.T) {
	if len(os.Args) < 3 {
		return
	}
	sep := -1
	for i, arg := range os.Args {
		if arg == "--" {
			sep = i
			break
		}
	}
	if sep < 0 || sep+1 >= len(os.Args) {
		return
	}
	mode := os.Args[sep+1]
	body, _ := io.ReadAll(os.Stdin)
	var req request
	_ = json.Unmarshal(body, &req)
	if req.Model != "test-model" {
		fmt.Fprintf(os.Stderr, "model = %q", req.Model)
		os.Exit(2)
	}
	switch mode {
	case "text":
		fmt.Print("plain response")
	case "stream-text":
		fmt.Print("first \n")
		time.Sleep(4 * time.Second)
		fmt.Print("second\n")
	case "json":
		fmt.Print(`{"content":"json response"}`)
	case "jsonl":
		fmt.Println(`{"delta":"hello "}`)
		fmt.Println(`{"delta":"world"}`)
		fmt.Println(`{"done":true}`)
	case "codex-jsonl":
		fmt.Println(`{"type":"thread.started","thread_id":"thread_1"}`)
		fmt.Println(`{"type":"turn.started"}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"hello from codex"}}`)
		fmt.Println(`{"type":"turn.completed","usage":{"input_tokens":1,"output_tokens":1}}`)
	case "codex-jsonl-cumulative":
		fmt.Println(`{"type":"item.updated","item":{"id":"item_1","type":"agent_message","text":"hello"}}`)
		fmt.Println(`{"type":"item.completed","item":{"id":"item_1","type":"agent_message","text":"hello world"}}`)
	case "fail":
		fmt.Fprint(os.Stderr, "boom")
		os.Exit(7)
	case "stream-lines":
		fmt.Print("line one\n")
		time.Sleep(4 * time.Second)
		fmt.Print("line two\n")
		time.Sleep(100 * time.Millisecond)
		fmt.Print("line three\n")
	}
	os.Exit(0)
}
