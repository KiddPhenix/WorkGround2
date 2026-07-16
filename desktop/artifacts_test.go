package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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

// setupCodexArtifactEnv creates a temp CODEX_HOME with generated_images/
// containing a minimal PNG, and sets CODEX_HOME in t's environment.
func setupCodexArtifactEnv(t *testing.T) string {
	t.Helper()
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	if err := os.MkdirAll(genDir, 0755); err != nil {
		t.Fatalf("mkdir generated_images: %v", err)
	}
	pngPath := filepath.Join(genDir, "output.png")
	writeRequestHelpPNG(t, pngPath)
	t.Setenv("CODEX_HOME", codexHome)
	return pngPath
}

func requestHelpOutput(capability string, artifactJSON string) string {
	var b strings.Builder
	b.WriteString("Capability assist succeeded\n")
	b.WriteString("request_id: assist-test\n")
	b.WriteString("capability: " + capability + "\n")
	b.WriteString("from_model: test/a\n")
	b.WriteString("model: test/b\n")
	b.WriteString("attempt: 1/1\n")
	if artifactJSON != "" {
		b.WriteString("artifact: " + artifactJSON + "\n")
	}
	b.WriteString("\ngenerated image result")
	return b.String()
}

func TestExtractArtifacts_RequestHelpImageInWorkspace(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "generated.png")
	writeRequestHelpPNG(t, imgPath)

	artifactData, err := json.Marshal(map[string]any{
		"task_id": "img-1", "path": imgPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	// For workspace-internal images, readRequestHelpImage will reject them
	// because the workspace root is not in the allowed roots. This is
	// expected — request_help images are always in CODEX_HOME/generated_images
	// or draw provider output dirs. The test verifies that workspace-internal
	// images from request_help are correctly filtered (security boundary).
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-img", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-img", Name: "request_help",
			Content: requestHelpOutput("image_generation", string(artifactData))},
	}
	// Workspace-internal images from request_help are rejected by
	// readRequestHelpImage (not in allowed roots). This is by design.
	if len(extractArtifacts(msgs, dir)) != 0 {
		t.Error("workspace-internal request_help images should be rejected (not in allowed roots)")
	}
}

func TestExtractArtifacts_RequestHelpImageOutsideWorkspace(t *testing.T) {
	pngPath := setupCodexArtifactEnv(t)

	workspaceDir := t.TempDir()
	artifactData, err := json.Marshal(map[string]any{
		"task_id": "img-out", "path": pngPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-out", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-out", Name: "request_help",
			Content: requestHelpOutput("image_generation", string(artifactData))},
	}
	artifacts := extractArtifacts(msgs, workspaceDir)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 artifact for workspace-external valid PNG, got %d", len(artifacts))
	}
	a := artifacts[0]
	if a.Name != "output.png" {
		t.Errorf("Name = %q, want output.png", a.Name)
	}
	if a.Type != "image" {
		t.Errorf("Type = %q, want image", a.Type)
	}
	if a.Status != "available" {
		t.Errorf("Status = %q, want available", a.Status)
	}
	if a.Path != pngPath {
		t.Errorf("Path = %q, want %q", a.Path, pngPath)
	}
	if a.SourceRunID != "tc-out" {
		t.Errorf("SourceRunID = %q, want tc-out", a.SourceRunID)
	}
	// Outside workspace: RelativePath falls back to filename.
	if a.RelativePath != "output.png" {
		t.Errorf("RelativePath = %q, want output.png", a.RelativePath)
	}
}

func TestExtractArtifacts_RequestHelpImageRejectedCorruptFile(t *testing.T) {
	// A file with .png extension but not a valid image.
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	// Create generated_images subdir with the fake file inside it.
	genDir := filepath.Join(dir, "generated_images")
	os.MkdirAll(genDir, 0755)
	fakeCodexPath := filepath.Join(genDir, "fake.png")
	os.WriteFile(fakeCodexPath, []byte("not a real image"), 0644)

	artifactData, _ := json.Marshal(map[string]any{
		"task_id": "corrupt", "path": fakeCodexPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	workspaceDir := t.TempDir()
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-corrupt", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-corrupt", Name: "request_help",
			Content: requestHelpOutput("image_generation", string(artifactData))},
	}
	if len(extractArtifacts(msgs, workspaceDir)) != 0 {
		t.Error("corrupt image file should be rejected by readRequestHelpImage")
	}
}

func TestExtractArtifacts_RequestHelpImageRejectedNonImageGen(t *testing.T) {
	// web_search capability — artifact line should be ignored.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-web", Name: "request_help", Arguments: `{"capability":"web_search"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-web", Name: "request_help",
			Content: requestHelpOutput("web_search", "")},
	}
	if len(extractArtifacts(msgs, t.TempDir())) != 0 {
		t.Error("web_search request_help should not produce artifacts")
	}
}

func TestExtractArtifacts_RequestHelpImageRejectedBadJSON(t *testing.T) {
	workspaceDir := t.TempDir()
	output := "Capability assist succeeded\ncapability: image_generation\nartifact: not-json\n\nresult"
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bad", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bad", Name: "request_help",
			Content: output},
	}
	if len(extractArtifacts(msgs, workspaceDir)) != 0 {
		t.Error("malformed artifact JSON should be rejected")
	}
}

func TestExtractArtifacts_RequestHelpImageRejectedMissingFile(t *testing.T) {
	// Setup CODEX_HOME so the root check passes, but the file doesn't exist.
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	os.MkdirAll(genDir, 0755)
	t.Setenv("CODEX_HOME", codexHome)

	missingPath := filepath.Join(genDir, "missing.png")
	artifactData, _ := json.Marshal(map[string]any{
		"task_id": "missing", "path": missingPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	workspaceDir := t.TempDir()
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-missing", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-missing", Name: "request_help",
			Content: requestHelpOutput("image_generation", string(artifactData))},
	}
	if len(extractArtifacts(msgs, workspaceDir)) != 0 {
		t.Error("nonexistent file should be rejected by readRequestHelpImage")
	}
}

func TestExtractArtifacts_RequestHelpImageDedup(t *testing.T) {
	pngPath := setupCodexArtifactEnv(t)

	workspaceDir := t.TempDir()
	artifactData, err := json.Marshal(map[string]any{
		"task_id": "img-dup", "path": pngPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	output := requestHelpOutput("image_generation", string(artifactData))
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-dup1", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-dup1", Name: "request_help", Content: output},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-dup2", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-dup2", Name: "request_help", Content: output},
	}
	artifacts := extractArtifacts(msgs, workspaceDir)
	if len(artifacts) != 1 {
		t.Fatalf("expected 1 deduplicated artifact, got %d", len(artifacts))
	}
}

func TestParseRequestHelpArtifactPathIgnoresBodyOverrides(t *testing.T) {
	valid := `C:\generated_images\valid.png`
	malicious := `C:\generated_images\other.png`
	validJSON, _ := json.Marshal(map[string]string{"path": valid})
	maliciousJSON, _ := json.Marshal(map[string]string{"path": malicious})
	output := requestHelpOutput("image_generation", string(validJSON)) +
		"\ncapability: image_generation\nartifact: " + string(maliciousJSON)

	got, ok := parseRequestHelpArtifactPath(`{"capability":"image_generation"}`, output)
	if !ok || got != valid {
		t.Fatalf("path = %q, ok = %v, want trusted header path %q", got, ok, valid)
	}
	if _, ok := parseRequestHelpArtifactPath(`{"capability":"web_search"}`, output); ok {
		t.Fatal("tool-call capability mismatch should reject artifact")
	}
}

func TestExtractArtifacts_RequestHelpImageFailedTool(t *testing.T) {
	// A failed request_help (output starts with "error:") should already be
	// filtered by historyToolResultFailed before reaching extractArtifacts.
	workspaceDir := t.TempDir()
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-fail", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-fail", Name: "request_help",
			Content: "error: all candidates failed"},
	}
	if len(extractArtifacts(msgs, workspaceDir)) != 0 {
		t.Error("failed request_help should not produce artifacts")
	}
}
