package artifact

import (
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/config"
	"workground2/internal/provider"
)

// ── helpers ────────────────────────────────────────────────────────────────

func writeValidPNG(t *testing.T, path string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
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

// ── ImageProducer tests ────────────────────────────────────────────────────

func TestImageProducer_Success(t *testing.T) {
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	os.MkdirAll(genDir, 0o755)
	pngPath := filepath.Join(genDir, "output.png")
	writeValidPNG(t, pngPath)
	t.Setenv("CODEX_HOME", codexHome)

	artifactData, err := json.Marshal(map[string]any{
		"task_id": "img-1", "path": pngPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	if err != nil {
		t.Fatal(err)
	}

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-img", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-img", Name: "request_help",
			Content: requestHelpOutput("image_generation", string(artifactData))},
	}

	discovered := Collect(msgs, []Producer{&ImageProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	d := discovered[0]
	if d.Name != "output.png" {
		t.Errorf("Name = %q, want output.png", d.Name)
	}
	if !strings.HasPrefix(d.Type, "image/") {
		t.Errorf("Type = %q, want image/*", d.Type)
	}
	if d.Path != pngPath {
		t.Errorf("Path = %q, want %q", d.Path, pngPath)
	}
	if len(d.Data) == 0 {
		t.Error("Data is empty")
	}
	if d.SourceRunID != "tc-img" {
		t.Errorf("SourceRunID = %q, want tc-img", d.SourceRunID)
	}
	if d.SlotKind() != "image" {
		t.Errorf("SlotKind = %q, want image", d.SlotKind())
	}
}

func TestImageProducer_RejectsNonImageGen(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-web", Name: "request_help", Arguments: `{"capability":"web_search"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-web", Name: "request_help",
			Content: requestHelpOutput("web_search", "")},
	}
	if len(Collect(msgs, []Producer{&ImageProducer{}})) != 0 {
		t.Error("web_search should not produce image artifacts")
	}
}

func TestImageProducer_RejectsFailedTool(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-fail", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-fail", Name: "request_help",
			Content: "error: all candidates failed"},
	}
	if len(Collect(msgs, []Producer{&ImageProducer{}})) != 0 {
		t.Error("failed request_help should not produce artifacts")
	}
}

func TestImageProducer_RejectsBadArtifactJSON(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bad", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bad", Name: "request_help",
			Content: "Capability assist succeeded\ncapability: image_generation\nartifact: not-json\n\nresult"},
	}
	if len(Collect(msgs, []Producer{&ImageProducer{}})) != 0 {
		t.Error("malformed artifact JSON should be rejected")
	}
}

// ── FileProducer tests ─────────────────────────────────────────────────────

func TestFileProducer_WriteFile(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"output/app.exe","content":"fake"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 4 bytes to output/app.exe"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1, got %d", len(discovered))
	}
	if discovered[0].Name != "app.exe" {
		t.Errorf("Name = %q, want app.exe", discovered[0].Name)
	}
	if discovered[0].SlotKind() != "file" {
		t.Errorf("SlotKind = %q, want file", discovered[0].SlotKind())
	}
}

func TestFileProducer_SourceFiltered(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"main.go","content":"package main"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "wrote 12 bytes to main.go"},
	}
	if len(Collect(msgs, []Producer{&FileProducer{}})) != 0 {
		t.Error("source files should be filtered")
	}
}

func TestFileProducer_FailedTool(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "write_file", Arguments: `{"path":"x.exe","content":"x"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "write_file", Content: "error: permission denied"},
	}
	if len(Collect(msgs, []Producer{&FileProducer{}})) != 0 {
		t.Error("failed tool should not produce artifacts")
	}
}

// ── Collect dedup tests ────────────────────────────────────────────────────

func TestCollect_Dedup(t *testing.T) {
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	os.MkdirAll(genDir, 0o755)
	pngPath := filepath.Join(genDir, "output.png")
	writeValidPNG(t, pngPath)
	t.Setenv("CODEX_HOME", codexHome)

	artifactData, _ := json.Marshal(map[string]any{
		"task_id": "img-1", "path": pngPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})
	output := requestHelpOutput("image_generation", string(artifactData))

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "request_help", Content: output},
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc2", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc2", Name: "request_help", Content: output},
	}

	discovered := Collect(msgs, DefaultProducers())
	if len(discovered) != 1 {
		t.Fatalf("expected 1 deduplicated, got %d", len(discovered))
	}
}

// ── DefaultProducers integration test ──────────────────────────────────────

func TestDefaultProducers_ImageAndFile(t *testing.T) {
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	os.MkdirAll(genDir, 0o755)
	pngPath := filepath.Join(genDir, "output.png")
	writeValidPNG(t, pngPath)
	t.Setenv("CODEX_HOME", codexHome)

	artifactData, _ := json.Marshal(map[string]any{
		"task_id": "img-1", "path": pngPath, "mime": "image/png",
		"size": 100, "width": 1, "height": 1,
	})

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-img", Name: "request_help", Arguments: `{"capability":"image_generation"}`},
			{ID: "tc-file", Name: "write_file", Arguments: `{"path":"bin/app.exe","content":"fake"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-img", Name: "request_help",
			Content: requestHelpOutput("image_generation", string(artifactData))},
		{Role: provider.RoleTool, ToolCallID: "tc-file", Name: "write_file", Content: "ok"},
	}

	discovered := Collect(msgs, DefaultProducers())
	if len(discovered) != 2 {
		t.Fatalf("expected 2 artifacts, got %d", len(discovered))
	}

	// Verify image is first (dedup order is stable)
	var img, file *Discovered
	for i := range discovered {
		switch discovered[i].SourceRunID {
		case "tc-img":
			img = &discovered[i]
		case "tc-file":
			file = &discovered[i]
		}
	}
	if img == nil || len(img.Data) == 0 {
		t.Error("image artifact missing or data empty")
	}
	if file == nil || file.Name != "app.exe" {
		t.Errorf("file artifact: %+v", file)
	}
}

// ── ValidateImageFile tests ────────────────────────────────────────────────

func TestValidateImageFile_Success(t *testing.T) {
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	os.MkdirAll(genDir, 0o755)
	pngPath := filepath.Join(genDir, "output.png")
	writeValidPNG(t, pngPath)
	t.Setenv("CODEX_HOME", codexHome)

	raw, mime, err := ValidateImageFile(pngPath)
	if err != nil {
		t.Fatalf("ValidateImageFile: %v", err)
	}
	if len(raw) == 0 || !strings.HasPrefix(mime, "image/png") {
		t.Errorf("raw=%d bytes, mime=%q", len(raw), mime)
	}
}

func TestValidateImageFile_RejectsOutsideRoot(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "outside.png")
	writeValidPNG(t, pngPath)

	if _, _, err := ValidateImageFile(pngPath); err == nil {
		t.Error("expected rejection for image outside allowed roots")
	}
}

func TestValidateImageFile_RejectsCorruptFile(t *testing.T) {
	codexHome := t.TempDir()
	genDir := filepath.Join(codexHome, "generated_images")
	os.MkdirAll(genDir, 0o755)
	fakePath := filepath.Join(genDir, "fake.png")
	os.WriteFile(fakePath, []byte("not a real image"), 0o644)
	t.Setenv("CODEX_HOME", codexHome)

	if _, _, err := ValidateImageFile(fakePath); err == nil {
		t.Error("expected rejection for corrupt image")
	}
}

// ── SlotKind tests ─────────────────────────────────────────────────────────

func TestDiscoveredSlotKind(t *testing.T) {
	tests := []struct {
		mime string
		name string
		want string
	}{
		{"image/png", "out.png", "image"},
		{"image/jpeg", "photo.jpg", "image"},
		{"image/gif", "anim.gif", "image"},
		{"video/mp4", "clip.mp4", "video"},
		{"audio/mpeg", "song.mp3", "audio"},
		{"", "report.xlsx", "xlsx"},
		{"", "doc.pdf", "document"},
		{"", "archive.zip", "archive"},
		{"", "data.tar.gz", "archive"},
		// Data is nil for file-only artifacts, so SlotKind returns "file".
		{"application/octet-stream", "app.exe", "file"},
	}

	for _, tt := range tests {
		d := Discovered{Name: tt.name, Type: tt.mime}
		if got := d.SlotKind(); got != tt.want {
			t.Errorf("SlotKind(%q, %q) = %q, want %q", tt.mime, tt.name, got, tt.want)
		}
	}
}

// ── parseRequestHelpImageArtifact tests ─────────────────────────────────────

func TestParseRequestHelpImageArtifact_Valid(t *testing.T) {
	path := config.WorkGround2HomeDir() + "/generated_images/test.png"
	artifactJSON, _ := json.Marshal(map[string]string{"path": path})
	output := requestHelpOutput("image_generation", string(artifactJSON))

	got, ok := parseRequestHelpImageArtifact(`{"capability":"image_generation"}`, output)
	if !ok || got != path {
		t.Fatalf("got=%q ok=%v, want %q true", got, ok, path)
	}
}

func TestParseRequestHelpImageArtifact_IgnoresBodyOverrides(t *testing.T) {
	valid := `/generated_images/valid.png`
	malicious := `/etc/passwd`
	validJSON, _ := json.Marshal(map[string]string{"path": valid})
	maliciousJSON, _ := json.Marshal(map[string]string{"path": malicious})
	output := requestHelpOutput("image_generation", string(validJSON)) +
		"\ncapability: image_generation\nartifact: " + string(maliciousJSON)

	got, ok := parseRequestHelpImageArtifact(`{"capability":"image_generation"}`, output)
	if !ok || got != valid {
		t.Fatalf("path=%q ok=%v, want %q", got, ok, valid)
	}
}

func TestParseRequestHelpImageArtifact_CapabilityMismatch(t *testing.T) {
	output := requestHelpOutput("image_generation", `{"path":"/tmp/x.png"}`)
	if _, ok := parseRequestHelpImageArtifact(`{"capability":"web_search"}`, output); ok {
		t.Error("capability mismatch should reject")
	}
}

func TestDiscoveredExplicitKindWins(t *testing.T) {
	item := Discovered{Name: "output.custom", Type: "application/octet-stream", Kind: "scene"}
	if got := item.SlotKind(); got != "scene" {
		t.Fatalf("SlotKind = %q, want scene", got)
	}
}

func TestLoadWorkspaceFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "dist", "artifact.bin")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	want := []byte("artifact body")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadWorkspaceFile(Discovered{Name: "artifact.bin", Path: filepath.Join("dist", "artifact.bin")}, root)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != string(want) || got.Path != path || got.Type == "" {
		t.Fatalf("loaded artifact = %+v", got)
	}
}

func TestLoadWorkspaceFileRejectsOutsideWorkspace(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspaceFile(Discovered{Path: outside}, root); err == nil {
		t.Fatal("expected outside-workspace artifact to be rejected")
	}
}
