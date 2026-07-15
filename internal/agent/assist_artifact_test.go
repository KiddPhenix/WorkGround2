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
