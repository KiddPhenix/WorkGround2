package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
	"workground2/internal/tool/builtin"
)

func writePNG(t *testing.T, path string, w, h int, c color.RGBA) {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func viewImageAgent(t *testing.T, dir string, mp *testutil.MockProvider) *Agent {
	t.Helper()
	reg := tool.NewRegistry()
	ws := builtin.Workspace{Dir: dir}
	for _, tl := range ws.Tools("view_image") {
		reg.Add(tl)
	}
	return New(mp, reg, NewSession("system"), Options{}, event.Discard)
}

func pngDims(t *testing.T, dataURL string) (int, int) {
	t.Helper()
	_, payload, ok := provider.ParseImageDataURL(dataURL)
	if !ok {
		t.Fatalf("not a data URL: %q", dataURL)
	}
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		t.Fatalf("decode image: %v", err)
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("decode config: %v", err)
	}
	return cfg.Width, cfg.Height
}

// TestViewImageToolDeliversPixelsToNextRequest verifies the full loop: the
// first model turn issues a view_image call, the second request keeps the
// paired assistant tool_call → tool result, and then carries the image as a
// host-origin user message whose Images field holds a real data URL.
func TestViewImageToolDeliversPixelsToNextRequest(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "diagram.png"), 2, 2, color.RGBA{R: 255, A: 255})

	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "v1", Name: "view_image", Arguments: `{"path":"diagram.png"}`}}},
		testutil.Turn{Text: "I can see the diagram"},
	)
	a := viewImageAgent(t, dir, mp)
	if err := a.Run(context.Background(), "look at diagram.png"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	second := reqs[1]
	var sawToolResult, sawHostImage bool
	for _, m := range second.Messages {
		if m.Role == provider.RoleTool && m.ToolCallID == "v1" && m.Name == "view_image" {
			sawToolResult = true
		}
		if m.Role == provider.RoleUser && m.Origin == provider.MessageOriginHost && len(m.Images) > 0 {
			sawHostImage = true
			if len(m.Images) != 1 || !strings.HasPrefix(m.Images[0], "data:image/png;base64,") {
				t.Fatalf("host images = %v, want one png data URL", m.Images)
			}
			if !strings.Contains(m.Content, "view_image") {
				t.Fatalf("host image message should carry a source note, got %q", m.Content)
			}
		}
	}
	if !sawToolResult {
		t.Fatalf("second request missing paired tool result: %+v", second.Messages)
	}
	if !sawHostImage {
		t.Fatalf("second request missing host user message with images: %+v", second.Messages)
	}
}

// TestViewImageToolMergesMultipleImagesInCallOrder verifies a two-call batch
// keeps provider order and attaches both images to the host message.
func TestViewImageToolMergesMultipleImagesInCallOrder(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "a.png"), 1, 1, color.RGBA{R: 255, A: 255})
	writePNG(t, filepath.Join(dir, "b.png"), 2, 2, color.RGBA{B: 255, A: 255})

	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{
			{ID: "v1", Name: "view_image", Arguments: `{"path":"a.png"}`},
			{ID: "v2", Name: "view_image", Arguments: `{"path":"b.png"}`},
		}},
		testutil.Turn{Text: "both seen"},
	)
	a := viewImageAgent(t, dir, mp)
	if err := a.Run(context.Background(), "look at a.png and b.png"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	var hostImages []string
	for _, m := range reqs[1].Messages {
		if m.Role == provider.RoleUser && m.Origin == provider.MessageOriginHost && len(m.Images) > 0 {
			hostImages = m.Images
		}
	}
	if len(hostImages) != 2 {
		t.Fatalf("host images = %d, want 2", len(hostImages))
	}
	w1, h1 := pngDims(t, hostImages[0])
	w2, h2 := pngDims(t, hostImages[1])
	if w1 != 1 || h1 != 1 || w2 != 2 || h2 != 2 {
		t.Fatalf("image order wrong: got %dx%d then %dx%d", w1, h1, w2, h2)
	}
}

// TestViewImageToolSkipsFailedImages verifies a failed view_image call injects
// no image while a sibling success still does.
func TestViewImageToolSkipsFailedImages(t *testing.T) {
	dir := t.TempDir()
	writePNG(t, filepath.Join(dir, "good.png"), 1, 1, color.RGBA{G: 255, A: 255})

	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{
			{ID: "v1", Name: "view_image", Arguments: `{"path":"good.png"}`},
			{ID: "v2", Name: "view_image", Arguments: `{"path":"missing.png"}`},
		}},
		testutil.Turn{Text: "one seen"},
	)
	a := viewImageAgent(t, dir, mp)
	if err := a.Run(context.Background(), "look at good.png and missing.png"); err != nil {
		t.Fatalf("Run: %v", err)
	}

	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("requests = %d, want 2", len(reqs))
	}
	var hostImages []string
	for _, m := range reqs[1].Messages {
		if m.Role == provider.RoleUser && m.Origin == provider.MessageOriginHost && len(m.Images) > 0 {
			hostImages = m.Images
		}
	}
	if len(hostImages) != 1 {
		t.Fatalf("host images = %d, want only the successful one", len(hostImages))
	}
	if w, h := pngDims(t, hostImages[0]); w != 1 || h != 1 {
		t.Fatalf("successful image wrong dims: %dx%d", w, h)
	}
}
