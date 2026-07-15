package agent

import (
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/config"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/pkg/drawaddon"
)

type testDrawTool struct {
	path string
}

func (t *testDrawTool) Name() string            { return "draw_image" }
func (t *testDrawTool) Description() string     { return "test image generator" }
func (t *testDrawTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *testDrawTool) ReadOnly() bool          { return false }
func (t *testDrawTool) Execute(context.Context, json.RawMessage) (string, error) {
	if err := os.MkdirAll(filepath.Dir(t.path), 0o755); err != nil {
		return "", err
	}
	file, err := os.Create(t.path)
	if err != nil {
		return "", err
	}
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 20, G: 40, B: 60, A: 255})
	encodeErr := png.Encode(file, img)
	closeErr := file.Close()
	if encodeErr != nil {
		return "", encodeErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	data, err := json.Marshal(drawTaskResult{
		TaskID: "draw-1", ProviderID: "test", Status: drawaddon.TaskSucceeded, OutputPath: t.path,
	})
	return string(data), err
}

func TestRequestHelpImageRequiresVerifiedArtifact(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	outputDir := filepath.Join(home, "images")
	if _, err := drawaddon.New(home).Save(context.Background(), drawaddon.ProviderInput{
		ID: "test", Enabled: true, Mode: drawaddon.ModeCLI, CLICommand: "test", OutputDir: outputDir,
	}); err != nil {
		t.Fatalf("save draw provider: %v", err)
	}
	path := filepath.Join(outputDir, "result.png")
	registry := tool.NewRegistry()
	registry.Add(&testDrawTool{path: path})
	prov := &mockProvider{name: "img/m1", streams: [][]provider.Chunk{
		{{Type: provider.ChunkToolCall, ToolCall: &provider.ToolCall{ID: "draw-call", Name: "draw_image", Arguments: `{}`}}, {Type: provider.ChunkDone}},
		successChunks,
	}}
	cfg := requestHelpConfig("image_generation", "img", "m1")
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	helper := NewRequestHelpTool(cfg, registry, "current-model", resolve, 20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2).
		WithTranscripts(NewSubagentStore(filepath.Join(home, "subagents")), home, "base-model", "")
	out, err := helper.Execute(WithParentSession(context.Background(), "parent"), []byte(`{"capability":"image_generation","prompt":"draw"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	encodedPath, _ := json.Marshal(path)
	for _, want := range []string{"request_id: assist-", `"path":` + string(encodedPath), "image/png"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output should contain %q: %s", want, out)
		}
	}
}

func TestAssistRequestIDStable(t *testing.T) {
	ctx := WithParentSession(context.Background(), "parent")
	first := assistRequestID(ctx, config.CapWebSearch, "search")
	second := assistRequestID(ctx, config.CapWebSearch, "search")
	if first != second {
		t.Fatalf("request ids differ: %q != %q", first, second)
	}
	if first == assistRequestID(ctx, config.CapImageGeneration, "search") {
		t.Fatal("request id should include capability")
	}
}

func TestValidateImageArtifactRejectsOutsideOutputRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	outputDir := filepath.Join(home, "allowed")
	if _, err := drawaddon.New(home).Save(context.Background(), drawaddon.ProviderInput{
		ID: "test", Enabled: true, Mode: drawaddon.ModeCLI, CLICommand: "test", OutputDir: outputDir,
	}); err != nil {
		t.Fatalf("save draw provider: %v", err)
	}
	outside := filepath.Join(home, "outside.png")
	if _, err := (&testDrawTool{path: outside}).Execute(context.Background(), nil); err != nil {
		t.Fatalf("write image: %v", err)
	}
	data, _ := json.Marshal(drawTaskResult{
		TaskID: "draw-escape", ProviderID: "test", Status: drawaddon.TaskSucceeded, OutputPath: outside,
	})
	run := EphemeralSubagentRun("")
	run.Session.Add(provider.Message{Role: provider.RoleTool, Name: "draw_image", Content: string(data)})
	if _, err := validateImageArtifact(run); err == nil || !strings.Contains(err.Error(), "outside configured output") {
		t.Fatalf("expected output-root validation error, got %v", err)
	}
}

func TestValidateCodexArtifactSuccess(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "thread_ok"
	dir := filepath.Join(codexHome, "generated_images", threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(dir, "result.png")
	if _, err := (&testDrawTool{path: imgPath}).Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	collector := &provider.ArtifactCollector{}
	collector.AddArtifact(imgPath)

	artifact, err := validateCodexArtifact(collector)
	if err != nil {
		t.Fatalf("validateCodexArtifact: %v", err)
	}
	if artifact.Path != imgPath {
		t.Fatalf("path = %q, want %q", artifact.Path, imgPath)
	}
	if !strings.HasPrefix(artifact.MIME, "image/") {
		t.Fatalf("mime = %q", artifact.MIME)
	}
	if artifact.Size == 0 {
		t.Fatal("size should be non-zero")
	}
}

func TestValidateCodexArtifactRejectsPathOutsideRoot(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	// Plant a file outside the generated_images root.
	outside := filepath.Join(codexHome, "evil.png")
	if _, err := (&testDrawTool{path: outside}).Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	collector := &provider.ArtifactCollector{}
	collector.AddArtifact(outside)

	_, err := validateCodexArtifact(collector)
	if err == nil || !strings.Contains(err.Error(), "outside generated_images") {
		t.Fatalf("expected boundary error, got %v", err)
	}
}

func TestValidateCodexArtifactRejectsRelativePath(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	collector := &provider.ArtifactCollector{}
	collector.AddArtifact("relative/path.png")

	_, err := validateCodexArtifact(collector)
	if err == nil || !strings.Contains(err.Error(), "not absolute") {
		t.Fatalf("expected absolute-path error, got %v", err)
	}
}

func TestValidateCodexArtifactRejectsNonImageMIME(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "thread_text"
	dir := filepath.Join(codexHome, "generated_images", threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	txtPath := filepath.Join(dir, "not_image.txt")
	if err := os.WriteFile(txtPath, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}
	collector := &provider.ArtifactCollector{}
	collector.AddArtifact(txtPath)

	_, err := validateCodexArtifact(collector)
	if err == nil || !strings.Contains(err.Error(), "non-image MIME") {
		t.Fatalf("expected MIME error, got %v", err)
	}
}

func TestValidateCodexArtifactNoCollector(t *testing.T) {
	_, err := validateCodexArtifact(nil)
	if err == nil || !strings.Contains(err.Error(), "no CLI artifact collector") {
		t.Fatalf("expected no-collector error, got %v", err)
	}
}

func TestValidateCodexArtifactNoArtifacts(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	collector := &provider.ArtifactCollector{}
	_, err := validateCodexArtifact(collector)
	if err == nil || !strings.Contains(err.Error(), "no generated image files") {
		t.Fatalf("expected no-artifacts error, got %v", err)
	}
}

// TestImageArtifactDimensions verifies that validateImageArtifact captures
// width/height from the decoded image config.
func TestImageArtifactDimensions(t *testing.T) {
	home := t.TempDir()
	t.Setenv("WorkGround2_HOME", home)
	outputDir := filepath.Join(home, "images")
	if _, err := drawaddon.New(home).Save(context.Background(), drawaddon.ProviderInput{
		ID: "dims", Enabled: true, Mode: drawaddon.ModeCLI, CLICommand: "test", OutputDir: outputDir,
	}); err != nil {
		t.Fatalf("save draw provider: %v", err)
	}
	// Create a 4×3 image (wider than tall).
	path := filepath.Join(outputDir, "wide.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	data, _ := json.Marshal(drawTaskResult{
		TaskID: "dims-1", ProviderID: "dims", Status: drawaddon.TaskSucceeded, OutputPath: path,
	})
	run := EphemeralSubagentRun("")
	run.Session.Add(provider.Message{Role: provider.RoleTool, Name: "draw_image", Content: string(data)})

	artifact, err := validateImageArtifact(run)
	if err != nil {
		t.Fatalf("validateImageArtifact: %v", err)
	}
	if artifact.Width != 4 {
		t.Fatalf("width = %d, want 4", artifact.Width)
	}
	if artifact.Height != 3 {
		t.Fatalf("height = %d, want 3", artifact.Height)
	}
	if artifact.Size <= 0 {
		t.Fatal("size should be non-zero")
	}
	if artifact.MIME != "image/png" {
		t.Fatalf("mime = %q, want image/png", artifact.MIME)
	}
}

// TestCodexArtifactDimensions verifies that validateCodexFile captures
// width/height.
func TestCodexArtifactDimensions(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexHome, "generated_images", "thread_dims")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "tall.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rect(0, 0, 2, 5))
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	artifact, err := validateCodexFile(path, dir)
	if err != nil {
		t.Fatalf("validateCodexFile: %v", err)
	}
	if artifact.Width != 2 {
		t.Fatalf("width = %d, want 2", artifact.Width)
	}
	if artifact.Height != 5 {
		t.Fatalf("height = %d, want 5", artifact.Height)
	}
}

// codexArtifactProvider simulates a Codex CLI provider: it streams a text
// answer (e.g. a placeholder _image_id_.png) and, as a side effect, reports a
// generated image file through the request-scoped ArtifactCollector attached
// to ctx — exactly as the real CLI provider does.
type codexArtifactProvider struct {
	name   string
	chunks []provider.Chunk
	paths  []string // files to report via the collector
}

func (p *codexArtifactProvider) Name() string { return p.name }
func (p *codexArtifactProvider) Stream(ctx context.Context, _ provider.Request) (<-chan provider.Chunk, error) {
	if collector, ok := provider.ArtifactCollectorFrom(ctx); ok {
		for _, path := range p.paths {
			collector.AddArtifact(path)
		}
	}
	ch := make(chan provider.Chunk, len(p.chunks))
	for _, c := range p.chunks {
		ch <- c
	}
	close(ch)
	return ch, nil
}

// TestRequestHelpCodexArtifactSucceeds verifies the full flow: image_generation
// request_help runs the sub-agent, the provider reports a Codex side-effect
// image via the ArtifactCollector (no draw_image tool call), and request_help
// falls back to validateCodexArtifact and succeeds.
func TestRequestHelpCodexArtifactSucceeds(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "thread_e2e"
	dir := filepath.Join(codexHome, "generated_images", threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(dir, "cat.png")
	if _, err := (&testDrawTool{path: imgPath}).Execute(context.Background(), nil); err != nil {
		t.Fatal(err)
	}

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "codex",
		Kind:         "cli",
		Command:      "codex",
		Model:        "codex",
		Capabilities: []string{"image_generation"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"image_generation": {"codex/codex"},
	}

	prov := &codexArtifactProvider{
		name: "codex/codex",
		chunks: []provider.Chunk{
			{Type: provider.ChunkText, Text: "_image_id_.png"},
			{Type: provider.ChunkDone},
		},
		paths: []string{imgPath},
	}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	helper := NewRequestHelpTool(cfg, tool.NewRegistry(), "current-model", resolve,
		20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2).
		WithTranscripts(NewSubagentStore(t.TempDir()), t.TempDir(), "base-model", "")

	out, err := helper.Execute(WithParentSession(context.Background(), "parent"), []byte(`{"capability":"image_generation","prompt":"draw a cat"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	encodedPath, _ := json.Marshal(imgPath)
	for _, want := range []string{
		"request_id: assist-",
		`"path":` + string(encodedPath),
		"image/png",
		"codex-cli",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("output should contain %q: %s", want, out)
		}
	}
}

// TestRequestHelpCodexArtifactRejectsFakePath verifies that when the CLI
// provider reports a path that does not point to a real image file, the
// request_help tool fails explicitly instead of accepting a fake path.
func TestRequestHelpCodexArtifactRejectsFakePath(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)

	cfg := config.Default()
	cfg.Providers = append(cfg.Providers, config.ProviderEntry{
		Name:         "codex",
		Kind:         "cli",
		Command:      "codex",
		Model:        "codex",
		Capabilities: []string{"image_generation"},
	})
	cfg.Agent.AssistModels = map[string][]string{
		"image_generation": {"codex/codex"},
	}

	// Provider reports a non-existent path.
	prov := &codexArtifactProvider{
		name: "codex/codex",
		chunks: []provider.Chunk{
			{Type: provider.ChunkText, Text: "_image_id_.png"},
			{Type: provider.ChunkDone},
		},
		paths: []string{filepath.Join(codexHome, "generated_images", "thread_fake", "nope.png")},
	}
	resolve := func(string, string) (provider.Provider, *provider.Pricing, int, error) {
		return prov, nil, 0, nil
	}
	helper := NewRequestHelpTool(cfg, tool.NewRegistry(), "current-model", resolve,
		20, 0, 0, 0, 0, 0, 0, 0, nil, 0, 2).
		WithTranscripts(NewSubagentStore(t.TempDir()), t.TempDir(), "base-model", "")

	_, err := helper.Execute(WithParentSession(context.Background(), "parent"), []byte(`{"capability":"image_generation","prompt":"draw a cat"}`))
	if err == nil {
		t.Fatal("Execute should fail when no real image exists")
	}
	if !strings.Contains(err.Error(), "no verified image artifact") {
		t.Fatalf("expected no-verified-image error, got: %v", err)
	}
}
