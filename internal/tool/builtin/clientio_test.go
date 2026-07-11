package builtin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type overlayStub struct {
	read      string
	readCalls int
	writes    map[string]string
}

type terminalStub struct{ calls int }

func (s *terminalStub) RunCommand(context.Context, string, string, time.Duration) (string, bool, error) {
	s.calls++
	return "host terminal", true, nil
}

func TestWorkspaceBashUsesHostTerminalForForegroundCommand(t *testing.T) {
	terminal := &terminalStub{}
	bashTool := byName(Workspace{Dir: t.TempDir(), Terminal: terminal}.Tools("bash"))["bash"]
	out, err := bashTool.Execute(context.Background(), jsonArgs(t, map[string]any{"command": "echo ok"}))
	if err != nil || out != "host terminal" || terminal.calls != 1 {
		t.Fatalf("bash terminal = %q, calls=%d, err=%v", out, terminal.calls, err)
	}
}

func (o *overlayStub) ReadTextFile(context.Context, string) (string, bool) {
	o.readCalls++
	return o.read, true
}

func (o *overlayStub) WriteTextFile(_ context.Context, path, content string) (bool, error) {
	if o.writes == nil {
		o.writes = map[string]string{}
	}
	o.writes[path] = content
	return true, nil
}

func TestWorkspaceFileOverlaySeesUnsavedTextAndWrites(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "note.txt")
	if err := os.WriteFile(path, []byte("disk"), 0o644); err != nil {
		t.Fatal(err)
	}
	overlay := &overlayStub{read: "unsaved"}
	tools := byName(Workspace{Dir: root, FileOverlay: overlay}.Tools("read_file", "write_file"))
	out, err := tools["read_file"].Execute(context.Background(), jsonArgs(t, map[string]any{"path": "note.txt"}))
	if err != nil || !strings.Contains(out, "unsaved") || strings.Contains(out, "disk") {
		t.Fatalf("overlay read = %q, %v", out, err)
	}
	if _, err := tools["write_file"].Execute(context.Background(), jsonArgs(t, map[string]any{"path": "new.txt", "content": "new"})); err != nil {
		t.Fatal(err)
	}
	if got := overlay.writes[filepath.Join(root, "new.txt")]; got != "new" {
		t.Fatalf("overlay write = %q", got)
	}
}

func TestWorkspaceOverlayCannotBypassWriteConfinement(t *testing.T) {
	root := t.TempDir()
	overlay := &overlayStub{}
	write := byName(Workspace{Dir: root, FileOverlay: overlay}.Tools("write_file"))["write_file"]
	outside := filepath.Join(filepath.Dir(root), "outside.txt")
	if _, err := write.Execute(context.Background(), jsonArgs(t, map[string]any{"path": outside, "content": "x"})); err == nil {
		t.Fatal("outside write unexpectedly succeeded")
	}
	if len(overlay.writes) != 0 {
		t.Fatal("overlay was called before confinement")
	}
}

func jsonArgs(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
