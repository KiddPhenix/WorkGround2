package main

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/config"
	"workground2/internal/provider"
)

func TestArtifactClassify(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"build/app.exe", "binary"}, {"setup.msi", "package"},
		{"start.bat", "script"}, {"start.sh", "script"}, {"start.ps1", "script"},
		{"archive.zip", "archive"}, {"archive.tar.gz", "archive"},
		{"screenshot.png", "image"}, {"clip.mp4", "video"},
		{"song.mp3", "audio"}, {"report.pdf", "document"},
		{"unknown.xyz", "file"},
	}
	for _, tt := range tests {
		if got := classifyArtifact(tt.path); got != tt.want {
			t.Errorf("classifyArtifact(%q) = %q, want %q", tt.path, got, tt.want)
		}
	}
}

func TestIsSourceFile(t *testing.T) {
	for _, p := range []string{"main.go", "app.ts", "config.json", "notes.txt", "go.mod"} {
		if !isSourceFile(p) {
			t.Errorf("isSourceFile(%q) = false", p)
		}
	}
	for _, p := range []string{"app.exe", "build.zip", "image.png", "report.pdf", "start.bat"} {
		if isSourceFile(p) {
			t.Errorf("isSourceFile(%q) = true", p)
		}
	}
}

func TestExtractBashOutputPaths(t *testing.T) {
	got := extractBashOutputPaths("go build -o bin/app.exe ./cmd/app")
	if len(got) != 1 || got[0] != "bin/app.exe" {
		t.Errorf("got %v, want [bin/app.exe]", got)
	}

	got2 := extractBashOutputPaths("cl /Fe:bin\\app.exe src\\main.cpp")
	if len(got2) != 1 || got2[0] != `bin\app.exe` {
		t.Errorf("got %v, want [bin\\app.exe]", got2)
	}

	got3 := extractBashOutputPaths("echo hello")
	if len(got3) != 0 {
		t.Errorf("got %v, want []", got3)
	}
}

func TestCompleteStepEvidencePaths(t *testing.T) {
	args := `{"evidence":[{"kind":"files","paths":["bin/app.exe","bin/helper.dll"]},{"kind":"verification","command":"go test"}]}`
	got := completeStepEvidencePaths(args)
	if len(got) != 2 || got[0] != "bin/app.exe" || got[1] != "bin/helper.dll" {
		t.Errorf("got %v, want [bin/app.exe bin/helper.dll]", got)
	}
}

func TestExtractResultPaths(t *testing.T) {
	got := extractResultPaths("wrote 1024 bytes to bin/app.exe")
	if len(got) != 1 || got[0] != "bin/app.exe" {
		t.Errorf("wrote: got %v", got)
	}
	got2 := extractResultPaths("created output/report.pdf")
	if len(got2) != 1 || got2[0] != "output/report.pdf" {
		t.Errorf("created: got %v", got2)
	}
	got3 := extractResultPaths("Wrote 512 bytes to out/image.png")
	if len(got3) != 1 || got3[0] != "out/image.png" {
		t.Errorf("Wrote: got %v", got3)
	}
}

func TestExtractArtifacts_WriteFile(t *testing.T) {
	dir := t.TempDir()
	outPath := filepath.Join(dir, "output", "app.exe")
	os.MkdirAll(filepath.Dir(outPath), 0o755)
	os.WriteFile(outPath, []byte("fake"), 0o644)

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/app.exe","content":"fake"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 4 bytes to output/app.exe"},
	}

	artifacts := extractArtifacts(msgs, dir)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	a := artifacts[0]
	if a.Name != "app.exe" || a.Type != "binary" || a.Status != "available" {
		t.Errorf("artifact: Name=%s Type=%s Status=%s", a.Name, a.Type, a.Status)
	}
}

func TestExtractArtifacts_CompleteStep(t *testing.T) {
	dir := t.TempDir()
	scripts := filepath.Join(dir, "scripts")
	os.MkdirAll(scripts, 0o755)
	os.WriteFile(filepath.Join(scripts, "start.bat"), []byte("@echo off"), 0o644)
	os.WriteFile(filepath.Join(scripts, "start.ps1"), []byte("Write-Host ok"), 0o644)
	os.WriteFile(filepath.Join(scripts, "start.sh"), []byte("#!/bin/sh"), 0o755)

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "complete_step", Arguments: `{"step":"x","result":"done","evidence":[{"kind":"files","summary":"scripts","paths":["scripts/start.bat","scripts/start.ps1","scripts/start.sh"]}]}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "complete_step", Content: "ok"},
	}

	artifacts := extractArtifacts(msgs, dir)
	if len(artifacts) != 3 {
		t.Fatalf("expected 3 artifacts, got %d: %#v", len(artifacts), artifacts)
	}
}

func TestExtractArtifacts_BashBuild(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin", "app.exe"), []byte("fake"), 0o644)

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"go build -o bin/app.exe ./cmd/app"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash", Content: "build succeeded"},
	}

	artifacts := extractArtifacts(msgs, dir)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(artifacts))
	}
	if artifacts[0].Type != "binary" {
		t.Errorf("Type = %q, want binary", artifacts[0].Type)
	}
}

func TestExtractArtifacts_FailedTool(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"x.exe","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "error: permission denied"},
	}
	if len(extractArtifacts(msgs, t.TempDir())) != 0 {
		t.Error("expected 0 artifacts for failed tool")
	}
}

func TestExtractArtifacts_SourceFiltered(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"main.go","content":"package main"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 12 bytes to main.go"},
	}
	if len(extractArtifacts(msgs, t.TempDir())) != 0 {
		t.Error("source files should be filtered")
	}
}

func TestExtractArtifacts_OutOfWorkspace(t *testing.T) {
	dir := t.TempDir()
	outside := `C:\outside\outside.exe`
	if runtime.GOOS != "windows" {
		outside = "/outside/outside.exe"
	}
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"` + outside + `","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "ok"},
	}
	if len(extractArtifacts(msgs, dir)) != 0 {
		t.Error("out-of-workspace paths should be filtered")
	}
}

func TestExtractArtifacts_Missing(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"bin/missing.exe","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 1 bytes to bin/missing.exe"},
	}
	artifacts := extractArtifacts(msgs, t.TempDir())
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 missing artifact, got %d", len(artifacts))
	}
	if artifacts[0].Status != "missing" {
		t.Errorf("expected missing status, got %s", artifacts[0].Status)
	}
}

func TestExtractArtifacts_Dedup(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	os.WriteFile(filepath.Join(dir, "bin", "app.exe"), []byte("x"), 0o644)

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"bin/app.exe","content":"v1"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "ok"},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc2", Name: "write_file", Arguments: `{"path":"bin/app.exe","content":"v2"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc2", Name: "write_file", Content: "ok"},
	}
	if len(extractArtifacts(msgs, dir)) != 1 {
		t.Error("duplicate paths should be deduplicated")
	}
}

func TestExtractArtifacts_StableOrder(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "b.exe"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "a.png"), []byte("x"), 0o644)

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"b.exe","content":"x"}`},
			{ID: "tc2", Name: "write_file", Arguments: `{"path":"a.png","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "ok"},
		{Role: provider.RoleTool, ToolCallID: "tc2", Name: "write_file", Content: "ok"},
	}

	artifacts := extractArtifacts(msgs, dir)
	if len(artifacts) != 2 || artifacts[0].Name != "a.png" || artifacts[1].Name != "b.exe" {
		t.Errorf("expected sorted [a.png b.exe], got %v", artifacts)
	}
}

func TestExtractArtifacts_ReadFileIgnored(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "read_file", Arguments: `{"path":"existing.txt"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "read_file", Content: "file contents"},
	}
	if len(extractArtifacts(msgs, t.TempDir())) != 0 {
		t.Error("read_file should not produce artifacts")
	}
}

func TestExtractArtifacts_OutOfOrderResults(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "one.exe"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, "two.png"), []byte("x"), 0o644)
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "one", Name: "write_file", Arguments: `{"path":"one.exe"}`},
			{ID: "two", Name: "generate_image", Arguments: `{"output_path":"two.png"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "two", Name: "generate_image", Content: "saved two.png"},
		{Role: provider.RoleTool, ToolCallID: "one", Name: "write_file", Content: "ok"},
	}
	artifacts := extractArtifacts(msgs, dir)
	if len(artifacts) != 2 || artifacts[0].Name != "one.exe" || artifacts[1].Name != "two.png" {
		t.Fatalf("out-of-order results were not recovered: %#v", artifacts)
	}
}

func TestExtractArtifacts_Empty(t *testing.T) {
	if len(extractArtifacts(nil, "")) != 0 {
		t.Error("empty input should return empty")
	}
}

func artifactRecoveryApp(tabID, workspaceRoot, sessionPath string) *App {
	return &App{
		tabs: map[string]*WorkspaceTab{
			tabID: {
				ID:            tabID,
				Scope:         "project",
				WorkspaceRoot: workspaceRoot,
				SessionPath:   sessionPath,
			},
		},
		activeTabID: tabID,
		tabOrder:    []string{tabID},
	}
}

func TestArtifactsForTab_RestartRecoveryFromEventLog(t *testing.T) {
	stateHome := robustTempDir(t)
	t.Setenv("WorkGround2_STATE_HOME", stateHome)

	workspaceRoot := robustTempDir(t)
	outDir := filepath.Join(workspaceRoot, "output")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatal(err)
	}
	artifactPath := filepath.Join(outDir, "app.exe")
	if err := os.WriteFile(artifactPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessionDir := config.ProjectSessionDir(workspaceRoot)
	if err := os.MkdirAll(sessionDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sessionPath := filepath.Join(sessionDir, "restart.jsonl")
	session := agent.NewSession("system")
	session.Add(provider.Message{
		Role: provider.RoleAssistant,
		ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/app.exe","content":"fake"}`},
		},
	})
	session.Add(provider.Message{
		Role:       provider.RoleTool,
		ToolCallID: "tc1",
		Name:       "write_file",
		Content:    "wrote 4 bytes to output/app.exe",
	})
	if err := session.SaveSnapshot(sessionPath); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(agent.SessionEventLogPath(sessionPath)); err != nil {
		t.Fatalf("event log missing: %v", err)
	}
	// The compatibility anchor carries no transcript, so recovery must replay
	// the authoritative .events.jsonl sidecar.
	if err := os.WriteFile(sessionPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	tabID := "test-recovery"
	artifacts := artifactRecoveryApp(tabID, workspaceRoot, sessionPath).ArtifactsForTab(tabID)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact from disk recovery, got %d: %#v", len(artifacts), artifacts)
	}
	got := artifacts[0]
	if got.Name != "app.exe" || got.Type != "binary" || got.Status != "available" || got.SessionID != tabID || got.RelativePath != "output/app.exe" {
		t.Fatalf("unexpected restored artifact: %#v", got)
	}
}

func TestArtifactsForTab_NoCtrlNoSessionPath(t *testing.T) {
	tabID := "test-empty-path"
	if arts := artifactRecoveryApp(tabID, t.TempDir(), "").ArtifactsForTab(tabID); len(arts) != 0 {
		t.Errorf("expected 0 artifacts for empty session path, got %d", len(arts))
	}
}

func TestArtifactsForTab_NoCtrlBadPath(t *testing.T) {
	stateHome := robustTempDir(t)
	t.Setenv("WorkGround2_STATE_HOME", stateHome)

	workspaceRoot := robustTempDir(t)
	sneakyPath := filepath.Join(workspaceRoot, "..", "outside", "bad.jsonl")
	tabID := "test-bad-path"
	if arts := artifactRecoveryApp(tabID, workspaceRoot, sneakyPath).ArtifactsForTab(tabID); len(arts) != 0 {
		t.Errorf("expected 0 artifacts for path outside session dir, got %d", len(arts))
	}
}
