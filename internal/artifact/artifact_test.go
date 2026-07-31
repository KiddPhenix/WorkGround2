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
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadWorkspaceFile(Discovered{Path: rel}, root); err == nil {
		t.Fatal("expected outside-workspace artifact to be rejected")
	}
}

func TestLoadFileLoadsExternalViaDotDot(t *testing.T) {
	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "outside.bin")
	if err := os.WriteFile(outside, []byte("outside"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, outside)
	if err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(Discovered{Path: rel}, root)
	if err != nil {
		t.Fatalf("LoadFile should load external '..' path: %v", err)
	}
	if string(got.Data) != "outside" {
		t.Fatalf("Data = %q, want %q", got.Data, "outside")
	}
	if got.Path != outside {
		t.Errorf("Path = %q, want %q", got.Path, outside)
	}
}

func TestLoadWorkspaceFileRejectsEmptyPath(t *testing.T) {
	root := t.TempDir()
	if _, err := LoadWorkspaceFile(Discovered{Path: ""}, root); err == nil {
		t.Fatal("expected error for empty path")
	}
}

func TestLoadFileRejectsEmptyBaseDir(t *testing.T) {
	if _, err := LoadFile(Discovered{Path: "x.bin"}, ""); err == nil {
		t.Fatal("expected error for empty base directory")
	}
}

func TestLoadFileRejectsRelativeBaseDir(t *testing.T) {
	if _, err := LoadFile(Discovered{Path: "x.bin"}, "relative/root"); err == nil {
		t.Fatal("expected error for relative base directory")
	}
}

func TestLoadFileAbsolute(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "external_output.bin")
	want := []byte("external artifact payload")
	if err := os.WriteFile(path, want, 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := LoadFile(Discovered{Name: "external_output.bin", Path: path}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != string(want) {
		t.Fatalf("Data = %q, want %q", got.Data, want)
	}
	if got.Path != path {
		t.Errorf("Path = %q, want %q", got.Path, path)
	}
	if got.Name != "external_output.bin" {
		t.Errorf("Name = %q, want external_output.bin", got.Name)
	}
	if got.Type == "" {
		t.Error("Type should be detected")
	}
}

func TestLoadFileRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("real"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skip("symlink creation requires privileges on this platform")
	}
	_, err := LoadFile(Discovered{Path: link}, dir)
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("error = %v, want symlink rejection", err)
	}
}

func TestLoadFileRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	_, err := LoadFile(Discovered{Path: dir}, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("error = %v, want regular file rejection", err)
	}
}

func TestLoadFileRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.bin")
	if err := os.WriteFile(path, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := LoadFile(Discovered{Path: path}, dir)
	if err == nil || !strings.Contains(err.Error(), "between 1 byte") {
		t.Fatalf("error = %v, want size rejection", err)
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

// ── FileProducer relative path discovery tests ─────────────────────────────

func TestFileProducer_BashRelativeDocx_ChineseVerb(t *testing.T) {
	// "路线指引.docx saved successfully." → the relative path with a
	// non-source extension must be discovered even though the English
	// verb "saved" is followed by "successfully.", not the file name.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"docgen.ps1"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "路线指引.docx saved successfully."},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	d := discovered[0]
	if d.Name != "路线指引.docx" {
		t.Errorf("Name = %q, want 路线指引.docx", d.Name)
	}
	if d.Path != "路线指引.docx" {
		t.Errorf("Path = %q, want 路线指引.docx", d.Path)
	}
	if d.SlotKind() != "docx" {
		t.Errorf("SlotKind = %q, want docx", d.SlotKind())
	}
}

func TestFileProducer_BashRelativeDocx_ANSIPowerShell(t *testing.T) {
	// PowerShell Get-Item table with ANSI green highlighting:
	//   \x1b[32;1m路线指引.docx  37993 ...
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"Get-Item 路线指引.docx"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "\x1b[32;1m路线指引.docx  37993  2025-01-15  10:30\x1b[0m"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d: %+v", len(discovered), discovered)
	}
	d := discovered[0]
	if d.Name != "路线指引.docx" {
		t.Errorf("Name = %q, want 路线指引.docx", d.Name)
	}
	if d.Path != "路线指引.docx" {
		t.Errorf("Path = %q, want 路线指引.docx", d.Path)
	}
	if d.SlotKind() != "docx" {
		t.Errorf("SlotKind = %q, want docx", d.SlotKind())
	}
}

func TestFileProducer_BashRelativeDocx_DedupVerbAndRelative(t *testing.T) {
	// When both extractVerbPaths ("saved report.docx") and
	// extractRelativeArtifactPaths find the same file, only one
	// Discovered entry must be emitted.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"save.ps1"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "saved report.docx"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 deduplicated, got %d: %+v", len(discovered), discovered)
	}
	if discovered[0].Name != "report.docx" {
		t.Errorf("Name = %q, want report.docx", discovered[0].Name)
	}
}

func TestFileProducer_BashRelative_RejectsSourceFile(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"ls"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "main.go  utils_test.go  config.yaml"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 0 {
		t.Fatalf("expected 0, got %d: %+v", len(discovered), discovered)
	}
}

func TestFileProducer_BashRelative_RejectsURL(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"curl"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "download https://example.com/report.docx complete"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 0 {
		t.Fatalf("expected 0, got %d: %+v", len(discovered), discovered)
	}
}

func TestFileProducer_BashRelative_RejectsNonArtifactExtensions(t *testing.T) {
	// Tokens whose extension is not in the artifact whitelist must never
	// be treated as artifact paths, even if they pass looksLikePath.
	tests := []struct {
		name    string
		content string
	}{
		{"version number", "deployed v1.2.3 to staging"},
		{"date stamp", "built release.2026"},
		{"unknown TLD", "ping example.io ok"},
		{"custom unknown ext", "generated report.customext"},
		{"bare domain .com", "ping example.com ok"},
		{"bare domain .org", "see example.org for details"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msgs := []provider.Message{
				{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
					{ID: "tc1", Name: "bash", Arguments: `{"command":"run"}`},
				}},
				{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
					Content: tt.content},
			}
			if len(Collect(msgs, []Producer{&FileProducer{}})) != 0 {
				t.Errorf("expected 0 for %q", tt.content)
			}
		})
	}
}

func TestFileProducer_BashRelative_QuotedPathWithSpace(t *testing.T) {
	// "团队 路线.docx" saved successfully. → one artifact with the
	// complete, space-preserving relative path.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"docgen.ps1"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: `"团队 路线.docx" saved successfully.`},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d: %+v", len(discovered), discovered)
	}
	d := discovered[0]
	if d.Name != "团队 路线.docx" {
		t.Errorf("Name = %q, want 团队 路线.docx", d.Name)
	}
	if d.Path != "团队 路线.docx" {
		t.Errorf("Path = %q, want 团队 路线.docx", d.Path)
	}
	if d.SlotKind() != "docx" {
		t.Errorf("SlotKind = %q, want docx", d.SlotKind())
	}
}

func TestFileProducer_BashRelative_RejectsProse(t *testing.T) {
	// Common prose with dots (U.S., etc.) should not produce artifacts.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"echo done"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "The build finished successfully. Everything is fine."},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 0 {
		t.Fatalf("expected 0, got %d: %+v", len(discovered), discovered)
	}
}

func TestFileProducer_BashRelative_IgnoresFailedResult(t *testing.T) {
	// A failed bash result must not produce artifacts, even if it mentions
	// a plausible file name.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"docgen.ps1"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "error: could not create 路线指引.docx"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 0 {
		t.Fatalf("expected 0, got %d: %+v", len(discovered), discovered)
	}
}

func TestFileProducer_BashRelative_PreservesAbsolutePath(t *testing.T) {
	// Absolute paths must still be discovered as before.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc1", Name: "bash", Arguments: `{"command":"build.cmd"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc1", Name: "bash",
			Content: "Build done. D:\\out\\app.exe is ready."},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1, got %d: %+v", len(discovered), discovered)
	}
	if discovered[0].Path != "D:\\out\\app.exe" {
		t.Errorf("Path = %q, want D:\\out\\app.exe", discovered[0].Path)
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

// ── FileProducer bash inline output API tests ──────────────────────────────
// These verify that shell-embedded Python file-output calls (e.g.
// doc.save('file.docx')) are discovered as artifacts even when the
// tool result is garbled or lacks a recognisable path pattern.

func TestFileProducer_BashPythonSaveDocx(t *testing.T) {
	// Reproduces the real incident: doc.save('路线指引.docx') in bash,
	// garbled terminal output, ls confirms the file exists on disk.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"from docx import Document; doc = Document(); doc.save('路线指引.docx')\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash",
			Content: "\ufffd\u03ff\ufffd\u07ff\ufffd\ufffd\ufffd\ufffd\r\n"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	d := discovered[0]
	if d.Name != "路线指引.docx" {
		t.Errorf("Name = %q, want 路线指引.docx", d.Name)
	}
	if d.SlotKind() != "docx" {
		t.Errorf("SlotKind = %q, want docx", d.SlotKind())
	}
	if d.Path != "路线指引.docx" {
		t.Errorf("Path = %q, want 路线指引.docx", d.Path)
	}
	if d.SourceRunID != "tc-bash" {
		t.Errorf("SourceRunID = %q, want tc-bash", d.SourceRunID)
	}
}

func TestFileProducer_BashPythonSaveDocxDoubleQuoted(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c 'from docx import Document; doc = Document(); doc.save(\"报告.docx\")'"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	if discovered[0].Name != "报告.docx" {
		t.Errorf("Name = %q, want 报告.docx", discovered[0].Name)
	}
}

func TestFileProducer_BashPythonSaveWithPath(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"doc.save('output/路线指引.docx')\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	if discovered[0].Name != "路线指引.docx" {
		t.Errorf("Name = %q, want 路线指引.docx", discovered[0].Name)
	}
}

func TestFileProducer_BashSavefigPng(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"import matplotlib.pyplot as plt; plt.plot([1,2]); plt.savefig('chart.png')\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	if discovered[0].Name != "chart.png" {
		t.Errorf("Name = %q, want chart.png", discovered[0].Name)
	}
	if discovered[0].SlotKind() != "image" {
		t.Errorf("SlotKind = %q, want image", discovered[0].SlotKind())
	}
}

func TestFileProducer_BashToExcelXlsx(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"df.to_excel('report.xlsx')\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	if discovered[0].SlotKind() != "xlsx" {
		t.Errorf("SlotKind = %q, want xlsx", discovered[0].SlotKind())
	}
}

func TestFileProducer_BashOnlyReadsNotProduced(t *testing.T) {
	// Reading a .docx must not be mistaken for producing one.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"from docx import Document; doc = Document('input.docx'); print(doc.paragraphs[0].text)\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: "Hello from docx"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 0 {
		t.Fatalf("expected 0 discovered, got %d: reading should not produce artifacts", len(discovered))
	}
}

func TestFileProducer_BashOnlyEchoMentionNotProduced(t *testing.T) {
	// Mentioning a filename in an echo/print is not output — must not match.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"echo \"generated file: report.docx\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: "generated file: report.docx"},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 0 {
		t.Fatalf("expected 0 discovered, got %d: echo output should not match inline save patterns", len(discovered))
	}
}

func TestFileProducer_BashOutputFlagStillWorks(t *testing.T) {
	// Regression: -o flag extraction must still work.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"ffmpeg -i input.mp4 -o output.mp4"}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 discovered, got %d", len(discovered))
	}
	if discovered[0].Name != "output.mp4" {
		t.Errorf("Name = %q, want output.mp4", discovered[0].Name)
	}
}

func TestFileProducer_BashMultipleSavesDeduped(t *testing.T) {
	// Same file saved twice in one command → one artifact.
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"doc.save('out.docx'); doc2.save('out.docx')\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 1 {
		t.Fatalf("expected 1 deduped, got %d", len(discovered))
	}
}

func TestFileProducer_BashMultipleSavesDistinctPaths(t *testing.T) {
	msgs := []provider.Message{
		{Role: provider.RoleAssistant, ToolCalls: []provider.ToolCall{
			{ID: "tc-bash", Name: "bash", Arguments: `{"command":"python -c \"plt.savefig('a.png'); doc.save('b.docx')\""}`},
		}},
		{Role: provider.RoleTool, ToolCallID: "tc-bash", Name: "bash", Content: ""},
	}
	discovered := Collect(msgs, []Producer{&FileProducer{}})
	if len(discovered) != 2 {
		t.Fatalf("expected 2 distinct, got %d", len(discovered))
	}
	// Order must be deterministic (discovery order).
	names := make([]string, len(discovered))
	for i, d := range discovered {
		names[i] = d.Name
	}
	if names[0] != "a.png" || names[1] != "b.docx" {
		t.Errorf("unexpected order: %v", names)
	}
}
