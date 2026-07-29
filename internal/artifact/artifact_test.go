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
	if discovered[0].SlotKind() != "exe" {
		t.Errorf("SlotKind = %q, want exe", discovered[0].SlotKind())
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
		{"", "doc.pdf", "pdf"},
		{"", "letter.docx", "docx"},
		{"", "script.sh", "sh"},
		{"", "run.bat", "bat"},
		{"", "run.cmd", "bat"},
		{"", "deploy.ps1", "ps1"},
		{"", "archive.zip", "zip"},
		{"", "data.tar.gz", "zip"},
		{"application/octet-stream", "app.exe", "exe"},
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

// ── extractRawFilePaths tests ──────────────────────────────────────────────

func TestExtractRawFilePaths_GarbledPrefix(t *testing.T) {
	// Simulates PowerShell GBK-garbled output where a Chinese prefix
	// precedes an absolute Windows path (the real reproduction case).
	const output = "\ufffd\u063f\ufffd\ufffd\ufffd\ufffd\ufffd\ufffd: D:\\Work\\test\\final_card.png\r\n  \ufffd\ufffd\ufffd\ufffd: \u7231\u4f60\r\n"
	paths := extractRawFilePaths(output)
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d: %v", len(paths), paths)
	}
	if paths[0] != "D:\\Work\\test\\final_card.png" {
		t.Errorf("path = %q", paths[0])
	}
}

func TestExtractRawFilePaths_WindowsAbsolute(t *testing.T) {
	paths := extractRawFilePaths("Build done. D:\\out\\app.exe is ready.")
	if len(paths) != 1 || paths[0] != "D:\\out\\app.exe" {
		t.Fatalf("got %v", paths)
	}
}

func TestExtractRawFilePaths_UnixAbsolute(t *testing.T) {
	paths := extractRawFilePaths("Exported /home/user/report.pdf successfully.")
	if len(paths) != 1 || paths[0] != "/home/user/report.pdf" {
		t.Fatalf("got %v", paths)
	}
}

func TestExtractRawFilePaths_FiltersSource(t *testing.T) {
	// Source files must still be filtered even from raw scanning.
	paths := extractRawFilePaths("D:\\src\\main.go compiled.")
	if len(paths) != 0 {
		t.Fatalf("expected 0, got %v", paths)
	}
}

func TestExtractRawFilePaths_RejectsNonPathTokens(t *testing.T) {
	// A bare sentence should not produce false positives.
	paths := extractRawFilePaths("Hello world. The build finished without errors. Everything is fine.")
	if len(paths) != 0 {
		t.Fatalf("expected 0, got %v", paths)
	}
}

func TestExtractRawFilePaths_BareTopLevelDirIgnored(t *testing.T) {
	// /usr alone is not a file path (no second separator, no extension).
	paths := extractRawFilePaths("/usr")
	if len(paths) != 0 {
		t.Fatalf("expected 0 for bare top-level dir, got %v", paths)
	}
}

// ── SlotKind extension tests ───────────────────────────────────────────────

func TestSlotKind_ImageByExtension(t *testing.T) {
	tests := []struct{ name, want string }{
		{"photo.png", "image"},
		{"photo.jpg", "image"},
		{"photo.jpeg", "image"},
		{"anim.gif", "image"},
		{"clip.mp4", "video"},
		{"clip.webm", "video"},
		{"clip.mov", "video"},
		{"clip.avi", "video"},
		{"clip.mkv", "video"},
		{"song.mp3", "audio"},
		{"song.wav", "audio"},
		{"song.ogg", "audio"},
		{"song.flac", "audio"},
		{"song.aac", "audio"},
		{"song.wma", "audio"},
	}
	for _, tt := range tests {
		d := Discovered{Name: tt.name}
		if got := d.SlotKind(); got != tt.want {
			t.Errorf("SlotKind(%q) = %q, want %q", tt.name, got, tt.want)
		}
	}
}

// ── FileProducer bash indirect image test ──────────────────────────────────

func TestFileProducer_BashIndirectImage(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "final_card.png")
	writeValidPNG(t, pngPath)

	// Simulate the real reproduction: write_file → bash → garbled output with path.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-write", Name: "write_file", Arguments: `{"path":"generate_card.py","content":"..."}`},
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python generate_card.py"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-write", Name: "write_file",
			Content: "wrote 7086 bytes to generate_card.py"},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash",
			// PowerShell GBK-garbled output: Chinese prefix + colon + space + absolute path.
			Content: "\ufffd\u063f\ufffd\ufffd\ufffd\ufffd\ufffd\ufffd: " + pngPath + "\r\n  \ufffd\ufffd\ufffd\ufffd: \ufffd\ufffd\r\n"},
	}

	discovered := Collect(msgs, DefaultProducers())

	// Should find 2 artifacts: generate_card.py (filtered as source) → 0,
	// final_card.png via bash raw path scan → 1.
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	d := discovered[0]
	if d.Name != "final_card.png" {
		t.Errorf("Name = %q, want final_card.png", d.Name)
	}
	if d.Path != pngPath {
		t.Errorf("Path = %q, want %q", d.Path, pngPath)
	}
	if d.SlotKind() != "image" {
		t.Errorf("SlotKind = %q, want image", d.SlotKind())
	}
	// FileProducer sets Data=nil — callers use LoadWorkspaceFile to validate.
	if len(d.Data) != 0 {
		t.Error("Data should be nil for FileProducer artifact")
	}
}

func TestFileProducer_BashIndirectImageLoadsWithWorkspaceFile(t *testing.T) {
	dir := t.TempDir()
	pngPath := filepath.Join(dir, "final_card.png")
	writeValidPNG(t, pngPath)

	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python generate_card.py"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash",
			Content: "贺卡已生成: " + pngPath},
	}

	discovered := Collect(msgs, DefaultProducers())
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}

	// Simulate Work task materialize via LoadWorkspaceFile.
	loaded, err := LoadWorkspaceFile(discovered[0], dir)
	if err != nil {
		t.Fatalf("LoadWorkspaceFile: %v", err)
	}
	if loaded.Name != "final_card.png" {
		t.Errorf("Name = %q", loaded.Name)
	}
	if !strings.HasPrefix(loaded.Type, "image/png") {
		t.Errorf("Type = %q, want image/png", loaded.Type)
	}
	if len(loaded.Data) == 0 {
		t.Error("Data empty after LoadWorkspaceFile")
	}
}

// ── CapabilityProducer / CollectSlotGuidance tests ──────────────────────────

func TestImageProducerCapabilityProducer(t *testing.T) {
	var p Producer = &ImageProducer{}
	cp, ok := p.(CapabilityProducer)
	if !ok {
		t.Fatal("ImageProducer must implement CapabilityProducer")
	}
	kinds := cp.SlotKinds()
	if len(kinds) != 1 || kinds[0] != "image" {
		t.Fatalf("SlotKinds = %v, want [image]", kinds)
	}
	if capability := cp.SlotCapability(); capability != "image_generation" {
		t.Fatalf("SlotCapability = %q, want image_generation", capability)
	}
	g := cp.SlotPromptGuidance()
	if g == "" || !strings.Contains(g, "request_help") || !strings.Contains(g, "image_generation") {
		t.Fatalf("SlotPromptGuidance missing request_help/image_generation: %q", g)
	}
}

func TestCollectSlotGuidance_ImageOnly(t *testing.T) {
	guidance := CollectSlotGuidance(DefaultProducers())
	// FileProducer does not implement CapabilityProducer; only ImageProducer does.
	if len(guidance) != 1 {
		t.Fatalf("expected 1 guidance entry, got %d", len(guidance))
	}
	if guidance[0].Kind != "image" {
		t.Errorf("Kind = %q, want image", guidance[0].Kind)
	}
	if guidance[0].Capability != "image_generation" {
		t.Errorf("Capability = %q, want image_generation", guidance[0].Capability)
	}
	if !strings.Contains(guidance[0].Guidance, "request_help") {
		t.Errorf("Guidance missing request_help: %q", guidance[0].Guidance)
	}
}

func TestCollectSlotGuidance_NoCapabilityProducer(t *testing.T) {
	// FileProducer alone should return no guidance.
	producers := []Producer{&FileProducer{}}
	guidance := CollectSlotGuidance(producers)
	if len(guidance) != 0 {
		t.Fatalf("expected 0 guidance, got %d", len(guidance))
	}
}

type customCapabilityProducer struct{}

func (*customCapabilityProducer) Discover(provider.ToolCall, provider.Message) []Discovered {
	return nil
}
func (*customCapabilityProducer) SlotKinds() []string        { return []string{" Video ", ""} }
func (*customCapabilityProducer) SlotCapability() string     { return " VIDEO_GENERATION " }
func (*customCapabilityProducer) SlotPromptGuidance() string { return " use video helper " }

func TestCollectSlotGuidance_CustomProducerNormalizesContract(t *testing.T) {
	guidance := CollectSlotGuidance([]Producer{&customCapabilityProducer{}})
	if len(guidance) != 1 {
		t.Fatalf("guidance count = %d, want 1", len(guidance))
	}
	got := guidance[0]
	if got.Kind != "video" || got.Capability != "video_generation" ||
		got.Guidance != "use video helper" {
		t.Fatalf("guidance = %+v", got)
	}
}
