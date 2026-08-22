package dshcompat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/tool"
)

func TestReadBoundedLineRejectsOversizeInput(t *testing.T) {
	_, err := readBoundedLine(bufio.NewReaderSize(bytes.NewBufferString("123456\n"), 2), 5)
	if err == nil || !strings.Contains(err.Error(), "exceeds 5 bytes") {
		t.Fatalf("readBoundedLine error = %v", err)
	}
}

func TestMaterializeBridgeIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	first, err := materializeBridge(dir)
	if err != nil {
		t.Fatal(err)
	}
	second, err := materializeBridge(dir)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("bridge paths differ: %q != %q", first, second)
	}
	if b, err := os.ReadFile(first); err != nil || string(b) != string(bridgeScript) {
		t.Fatalf("materialized bridge mismatch: err=%v", err)
	}
}

func TestModelNamePart(t *testing.T) {
	if got := modelNamePart("@deepseek-ai/dsh-base"); got != "deepseek_ai_dsh_base" {
		t.Fatalf("modelNamePart = %q", got)
	}
	if got := modelNamePart("路径"); got != "plugin" {
		t.Fatalf("non-ASCII modelNamePart = %q", got)
	}
}

func TestRealDSHBridgeTools(t *testing.T) {
	repo := strings.TrimSpace(os.Getenv("DSH_COMPAT_TEST_ROOT"))
	if repo == "" {
		t.Skip("set DSH_COMPAT_TEST_ROOT to a deepseek-harness checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	workspace := t.TempDir()
	writeTestFile(t, filepath.Join(workspace, "go.mod"), "module workground2\n")
	writeTestFile(t, filepath.Join(workspace, ".agents", "skills", "wg2-compat-smoke", "SKILL.md"), `---
name: wg2-compat-smoke
description: DSH compatibility smoke skill
---

The DSH skill plugin loaded this body through WG2.
`)
	client, err := Start(ctx, Spec{
		Name:              "dsh-base",
		BundlePackageJSON: filepath.Join(repo, "packages", "bundle", "base", "package.json"),
		RuntimeAnchor:     filepath.Join(repo, "apps", "cli", "package.json"),
		Workspace:         workspace,
		DSHHome:           filepath.Join(t.TempDir(), "dsh-home"),
		RuntimeDir:        t.TempDir(),
		Stderr:            io.Discard,
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
		if err := client.Close(); err != nil {
			t.Errorf("second Close: %v", err)
		}
	})
	if client.Info().Protocol != protocolVersion || len(client.Info().Tools) < 5 {
		t.Fatalf("info = %+v", client.Info())
	}

	todo := findRawTool(client.Tools(), "__todo_write")
	if todo == nil {
		t.Fatalf("todo_write missing from %d tools", len(client.Tools()))
	}
	result, err := todo.Execute(ctx, json.RawMessage(`{"todos":[{"content":"verify DSH bridge","status":"in_progress"}]}`))
	if err != nil {
		t.Fatalf("todo_write: %v", err)
	}
	if !strings.Contains(result, "1 in progress") {
		t.Fatalf("todo_write result = %q", result)
	}

	read := findRawTool(client.Tools(), "__read")
	if read == nil {
		t.Fatal("read missing")
	}
	result, err = read.Execute(ctx, json.RawMessage(`{"file_path":"go.mod","limit":1}`))
	if err != nil || !strings.Contains(result, "module workground2") {
		t.Fatalf("read result = %q, err = %v", result, err)
	}

	skill := findRawTool(client.Tools(), "__skill")
	if skill == nil {
		t.Fatal("skill missing")
	}
	result, err = skill.Execute(ctx, json.RawMessage(`{"name":"wg2-compat-smoke"}`))
	if err != nil || !strings.Contains(result, "loaded this body through WG2") {
		t.Fatalf("skill result = %q, err = %v", result, err)
	}

	if pwsh := findRawTool(client.Tools(), "__pwsh"); pwsh != nil {
		cancelCtx, cancelCall := context.WithTimeout(ctx, 250*time.Millisecond)
		defer cancelCall()
		started := time.Now()
		_, err = pwsh.Execute(cancelCtx, json.RawMessage(`{"command":"Start-Sleep -Seconds 20","description":"Wait for cancellation test"}`))
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("pwsh cancellation error = %v", err)
		}
		if elapsed := time.Since(started); elapsed > 3*time.Second {
			t.Fatalf("pwsh cancellation returned after %s", elapsed)
		}
	}
}

func findRawTool(tools []tool.Tool, suffix string) tool.Tool {
	for _, candidate := range tools {
		if strings.HasSuffix(candidate.Name(), suffix) {
			return candidate
		}
	}
	return nil
}
