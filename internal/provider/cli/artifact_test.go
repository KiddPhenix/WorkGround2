package cli

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"workground2/internal/provider"
)

func TestReportCodexArtifactsReportsRealFiles(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "thread_abc"
	dir := filepath.Join(codexHome, "generated_images", threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	imgPath := filepath.Join(dir, "cat.png")
	if err := writeTestPNG(imgPath); err != nil {
		t.Fatal(err)
	}

	collector := &provider.ArtifactCollector{}
	ctx := provider.WithArtifactCollector(context.Background(), collector)
	reportCodexArtifacts(ctx, threadID)

	arts := collector.Artifacts()
	if len(arts) != 1 || arts[0] != imgPath {
		t.Fatalf("artifacts = %v, want [%s]", arts, imgPath)
	}
}

func TestReportCodexArtifactsRejectsPathTraversal(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	// Plant a file outside the thread directory via a traversal-style threadID.
	// Even though we create the file, the traversal check must reject it.
	outsideDir := filepath.Join(codexHome, "generated_images", "evil")
	if err := os.MkdirAll(outsideDir, 0o755); err != nil {
		t.Fatal(err)
	}
	outsidePath := filepath.Join(outsideDir, "stolen.png")
	if err := writeTestPNG(outsidePath); err != nil {
		t.Fatal(err)
	}

	collector := &provider.ArtifactCollector{}
	ctx := provider.WithArtifactCollector(context.Background(), collector)

	for _, badID := range []string{"..", "../..", "..\\evil", "../evil"} {
		collector = &provider.ArtifactCollector{}
		ctx = provider.WithArtifactCollector(context.Background(), collector)
		reportCodexArtifacts(ctx, badID)
		if len(collector.Artifacts()) != 0 {
			t.Fatalf("threadID %q should be rejected, got %v", badID, collector.Artifacts())
		}
	}
}

func TestReportCodexArtifactsNoCollector(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	dir := filepath.Join(codexHome, "generated_images", "t1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := writeTestPNG(filepath.Join(dir, "x.png")); err != nil {
		t.Fatal(err)
	}
	// No collector in context — should be a no-op.
	reportCodexArtifacts(context.Background(), "t1")
}

func TestReportCodexArtifactsNoCodeXHome(t *testing.T) {
	t.Setenv("CODEX_HOME", "")
	collector := &provider.ArtifactCollector{}
	ctx := provider.WithArtifactCollector(context.Background(), collector)
	reportCodexArtifacts(ctx, "t1")
	if len(collector.Artifacts()) != 0 {
		t.Fatal("should report nothing when CODEX_HOME is unset")
	}
}

func TestReportCodexArtifactsSkipsEmptyAndDirs(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "thread_skip"
	dir := filepath.Join(codexHome, "generated_images", threadID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Empty file — should be skipped.
	emptyPath := filepath.Join(dir, "empty.png")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	// Subdirectory — should be skipped.
	if err := os.MkdirAll(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	// Valid file — should be reported.
	goodPath := filepath.Join(dir, "good.png")
	if err := writeTestPNG(goodPath); err != nil {
		t.Fatal(err)
	}

	collector := &provider.ArtifactCollector{}
	ctx := provider.WithArtifactCollector(context.Background(), collector)
	reportCodexArtifacts(ctx, threadID)

	arts := collector.Artifacts()
	if len(arts) != 1 || arts[0] != goodPath {
		t.Fatalf("artifacts = %v, want [%s]", arts, goodPath)
	}
}

// TestCodexJSONLThreadIDCapture verifies that the JSONL parser captures the
// thread_id from a thread.started event and reports generated artifacts.
func TestCodexJSONLThreadIDCapture(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	threadID := "thread_capture_test"
	imgPath := filepath.Join(codexHome, "generated_images", threadID, "result.png")

	p := newTestProviderMode(t, "codex-jsonl-artifact", "jsonl")
	p.(*client).codex = true
	collector := &provider.ArtifactCollector{}
	ctx := provider.WithArtifactCollector(context.Background(), collector)

	ch, err := p.Stream(ctx, provider.Request{Messages: []provider.Message{
		{Role: provider.RoleUser, Content: "draw a cat"},
	}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	for chunk := range ch {
		if chunk.Type == provider.ChunkError && chunk.Err != nil {
			t.Fatalf("chunk error: %v", chunk.Err)
		}
	}
	arts := collector.Artifacts()
	if len(arts) != 1 || arts[0] != imgPath {
		t.Fatalf("artifacts = %v, want [%s]", arts, imgPath)
	}
}

func TestNonCodexJSONLDoesNotReportCodexArtifacts(t *testing.T) {
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	p := newTestProviderMode(t, "codex-jsonl-artifact", "jsonl")
	collector := &provider.ArtifactCollector{}
	ctx := provider.WithArtifactCollector(context.Background(), collector)
	ch, err := p.Stream(ctx, provider.Request{Messages: []provider.Message{{Role: provider.RoleUser, Content: "draw"}}})
	if err != nil {
		t.Fatal(err)
	}
	for range ch {
	}
	if got := collector.Artifacts(); len(got) != 0 {
		t.Fatalf("non-Codex CLI must not report Codex artifacts: %v", got)
	}
}

func writeTestPNG(path string) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 10, G: 20, B: 30, A: 255})
	return png.Encode(f, img)
}
