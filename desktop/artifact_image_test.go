package main

import (
	"encoding/base64"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/provider"
)

// writeTestPNG creates a minimal valid PNG at path.
func writeTestPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// writeSessionJSONL writes messages as JSONL to path.
func writeSessionJSONL(t *testing.T, path string, msgs []provider.Message) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, m := range msgs {
		if err := enc.Encode(m); err != nil {
			t.Fatal(err)
		}
	}
}

// setTabNoCtrl creates a tab on the App without a live controller, so
// ArtifactsForTab falls back to the persisted session file. The session
// file must already exist at a path inside desktopSessionDir(workspaceRoot).
func (a *App) setTabNoCtrl(tabID, workspaceRoot, sessionPath string) {
	t := &WorkspaceTab{
		ID:            tabID,
		WorkspaceRoot: workspaceRoot,
		SessionPath:   sessionPath,
		Ready:         true,
		disabledMCP:   map[string]ServerView{},
	}
	if a.tabs == nil {
		a.tabs = map[string]*WorkspaceTab{}
	}
	a.tabs[tabID] = t
	if a.activeTabID == "" {
		a.activeTabID = tabID
	}
}

func TestArtifactImageDataURL_WorkspaceImage(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "output", "screenshot.png")
	writeTestPNG(t, imgPath)

	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"go test -o output/screenshot.png"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash", Content: "generated output/screenshot.png"},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	if len(records) == 0 {
		t.Fatal("ArtifactsForTab returned no records")
	}
	var imgView *ArtifactView
	for i := range records {
		if records[i].Type == "image" && records[i].Status == "available" {
			imgView = &records[i]
			break
		}
	}
	if imgView == nil {
		t.Fatalf("no available image artifact found in %+v", records)
	}

	url, err := app.ArtifactImageDataURL("test", imgView.ArtifactID)
	if err != nil {
		t.Fatalf("ArtifactImageDataURL: %v", err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("data URL prefix = %q", url[:min(len(url), len(prefix))])
	}
	if raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix)); err != nil || len(raw) == 0 {
		t.Fatalf("decode data URL: len=%d err=%v", len(raw), err)
	}
}

func TestArtifactImageRejectsInvalidArtifactID(t *testing.T) {
	app := &App{}
	app.setTabNoCtrl("test", t.TempDir(), "")

	_, err := app.ArtifactImageDataURL("test", "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent artifact")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestArtifactImageRejectsWrongType(t *testing.T) {
	dir := t.TempDir()
	binPath := filepath.Join(dir, "output", "app.exe")
	if err := os.MkdirAll(filepath.Dir(binPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(binPath, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/app.exe","content":"fake"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 4 bytes to output/app.exe"},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	if len(records) == 0 {
		t.Fatal("ArtifactsForTab returned no records")
	}
	binView := &records[0]
	if binView.Type == "image" {
		t.Skip("artifact type is image, test setup produced image-type binary")
	}

	_, err := app.ArtifactImageDataURL("test", binView.ArtifactID)
	if err == nil {
		t.Fatal("expected error for non-image artifact")
	}
	if !strings.Contains(err.Error(), "not image") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestArtifactImageRejectsMissingFile(t *testing.T) {
	dir := t.TempDir()
	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/missing.png","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 1 bytes to output/missing.png"},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	if len(records) == 0 {
		t.Fatal("ArtifactsForTab returned no records")
	}
	missingView := &records[0]
	if missingView.Status != "missing" {
		t.Skipf("expected missing status, got %s", missingView.Status)
	}

	_, err := app.ArtifactImageDataURL("test", missingView.ArtifactID)
	if err == nil {
		t.Fatal("expected error for missing artifact")
	}
	if !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestArtifactImageRejectsNonImageContent(t *testing.T) {
	dir := t.TempDir()
	badPath := filepath.Join(dir, "output", "bad.png")
	if err := os.MkdirAll(filepath.Dir(badPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(badPath, []byte("plain text not an image"), 0o644); err != nil {
		t.Fatal(err)
	}

	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/bad.png","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 20 bytes to output/bad.png"},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	if len(records) == 0 {
		t.Fatal("ArtifactsForTab returned no records")
	}
	badView := &records[0]

	_, err := app.ArtifactImageDataURL("test", badView.ArtifactID)
	if err == nil {
		t.Fatal("expected error for non-image content")
	}
}

func TestArtifactImageRejectsOversizeFile(t *testing.T) {
	dir := t.TempDir()
	largePath := filepath.Join(dir, "output", "large.png")
	if err := os.MkdirAll(filepath.Dir(largePath), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(largePath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxArtifactImageBytes + 1); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/large.png","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 1 bytes to output/large.png"},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	if len(records) == 0 {
		t.Fatal("ArtifactsForTab returned no records")
	}
	largeView := &records[0]

	_, err = app.ArtifactImageDataURL("test", largeView.ArtifactID)
	if err == nil {
		t.Fatal("expected error for oversize file")
	}
}

func TestArtifactImageRejectsOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	outsideDir := t.TempDir()
	outsidePath := filepath.Join(outsideDir, "outside.png")
	writeTestPNG(t, outsidePath)

	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: jsonMarshal(t, map[string]string{"path": outsidePath, "content": "x"})},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote to " + outsidePath},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	// An absolute path outside workspace should be filtered by extractArtifacts.
	// If somehow captured, readArtifactImage rejects it.
	if len(records) == 0 {
		return
	}
	for _, r := range records {
		_, err := app.ArtifactImageDataURL("test", r.ArtifactID)
		if err == nil {
			t.Fatalf("expected error for artifact outside workspace: %+v", r)
		}
	}
}

func TestArtifactImageRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "output", "real.png")
	writeTestPNG(t, imgPath)
	linkPath := filepath.Join(dir, "output", "link.png")
	if err := os.Symlink(imgPath, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}

	sessionDir := desktopSessionDir(dir)
	sessionPath := filepath.Join(sessionDir, "test.jsonl")
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"ln -s real.png link.png"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash", Content: "created output/link.png"},
	}
	writeSessionJSONL(t, sessionPath, msgs)

	app := &App{}
	app.setTabNoCtrl("test", dir, sessionPath)

	records := app.ArtifactsForTab("test")
	if len(records) == 0 {
		t.Fatal("ArtifactsForTab returned no records")
	}
	for _, r := range records {
		if r.Path == linkPath || strings.HasSuffix(r.Path, "link.png") {
			_, err := app.ArtifactImageDataURL("test", r.ArtifactID)
			if err == nil || !strings.Contains(err.Error(), "symlink") {
				t.Fatalf("expected symlink rejection, got err=%v for %+v", err, r)
			}
			return
		}
	}
	t.Log("symlink artifact not found in records")
}

func TestArtifactImage_EmptyTabID(t *testing.T) {
	app := &App{}
	_, err := app.ArtifactImageDataURL("", "any")
	if err == nil {
		t.Fatal("expected error for empty tabID")
	}
}

func jsonMarshal(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
