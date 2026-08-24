package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"workground2/internal/imageutil"
	"workground2/internal/tool"
)

func testPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	img.Set(0, 0, image.White)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func viewImageExec(t *testing.T, workDir string, path string) (string, error) {
	t.Helper()
	args, err := json.Marshal(map[string]any{"path": path})
	if err != nil {
		t.Fatalf("marshal args: %v", err)
	}
	return (viewImage{workDir: workDir}).Execute(context.Background(), args)
}

func TestViewImageRegistered(t *testing.T) {
	tl, ok := tool.LookupBuiltin("view_image")
	if !ok {
		t.Fatal("view_image not registered")
	}
	if !tl.ReadOnly() {
		t.Error("view_image must be ReadOnly")
	}
	if c, ok := tl.(tool.PlanModeClassifier); !ok || !c.PlanModeSafe() {
		t.Error("view_image must declare PlanModeSafe")
	}
	if _, ok := tl.(tool.ImageExecutor); !ok {
		t.Error("view_image must implement tool.ImageExecutor")
	}
}

func TestViewImageRelativePath(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "diagram.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := viewImageExec(t, dir, "diagram.png")
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	v := viewImage{workDir: dir}
	_, images, err := v.ExecuteImages(context.Background(), argsJSON(t, map[string]any{"path": "diagram.png"}))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(images) != 1 || !strings.HasPrefix(images[0], "data:image/png;base64,") {
		t.Fatalf("images = %v, want one png data URL", images)
	}
}

func TestViewImageWorkspaceReference(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "diagram.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viewImage{workDir: dir}
	_, images, err := v.ExecuteImages(context.Background(), argsJSON(t, map[string]any{"path": "@diagram.png"}))
	if err != nil || len(images) != 1 {
		t.Fatalf("ExecuteImages(@path) images=%d err=%v", len(images), err)
	}
}

func TestViewImageAbsolutePathInsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "photo.png")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := viewImageExec(t, dir, path)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
}

func TestViewImageRejectsOutsideWorkspace(t *testing.T) {
	dir := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret.png")
	if err := os.WriteFile(outside, testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, filepath.Join("..", filepath.Base(outside))} {
		if _, err := viewImageExec(t, dir, path); err == nil {
			t.Fatalf("Execute(%q) should reject an out-of-workspace path", path)
		}
	}
}

func TestViewImageRejectsSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(t.TempDir(), "real.png")
	if err := os.WriteFile(target, testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link.png")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink not available: %v", err)
	}
	if _, err := viewImageExec(t, dir, "link.png"); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink should be rejected, got %v", err)
	}
}

func TestViewImageRejectsSymlinkDirectory(t *testing.T) {
	dir := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(target, "real.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "linked")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("directory symlink not available: %v", err)
	}
	if _, err := viewImageExec(t, dir, filepath.Join("linked", "real.png")); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink directory should be rejected, got %v", err)
	}
}

func TestViewImageHonorsForbidReadRoots(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(dir, "secret")
	if err := os.MkdirAll(secret, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(secret, "hidden.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viewImage{workDir: dir, forbidRoots: realRoots([]string{secret})}
	if _, err := v.Execute(context.Background(), argsJSON(t, map[string]any{"path": "secret/hidden.png"})); !os.IsNotExist(err) {
		t.Fatalf("forbidden image should appear missing, got %v", err)
	}
}

func TestViewImageRejectsDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := viewImageExec(t, dir, "."); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("directory should be rejected, got %v", err)
	}
}

func TestViewImageRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "empty.png"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := viewImageExec(t, dir, "empty.png"); err == nil || !strings.Contains(err.Error(), "empty") {
		t.Fatalf("empty file should be rejected, got %v", err)
	}
}

func TestViewImageRejectsOversize(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "huge.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(imageutil.MaxBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	_ = f.Close()
	if _, err := viewImageExec(t, dir, "huge.png"); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversize file should be rejected, got %v", err)
	}
}

func TestViewImageRejectsSpoofedMime(t *testing.T) {
	dir := t.TempDir()
	// The extension says PNG but the bytes are not an image.
	if err := os.WriteFile(filepath.Join(dir, "fake.png"), []byte("this is not an image"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := viewImageExec(t, dir, "fake.png"); err == nil || !strings.Contains(err.Error(), "not a supported image") {
		t.Fatalf("spoofed MIME should be rejected, got %v", err)
	}
}

func TestViewImageRejectsMissing(t *testing.T) {
	dir := t.TempDir()
	if _, err := viewImageExec(t, dir, "missing.png"); err == nil {
		t.Fatal("missing file should error")
	}
}

func TestViewImageRepeatSafe(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viewImage{workDir: dir}
	for i := 0; i < 3; i++ {
		_, images, err := v.ExecuteImages(context.Background(), argsJSON(t, map[string]any{"path": "a.png"}))
		if err != nil {
			t.Fatalf("repeat %d: %v", i, err)
		}
		if len(images) != 1 {
			t.Fatalf("repeat %d: images = %v", i, images)
		}
	}
}

func TestViewImageFailureReturnsNoImages(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.png"), testPNG(t), 0o644); err != nil {
		t.Fatal(err)
	}
	v := viewImage{workDir: dir}
	if _, images, err := v.ExecuteImages(context.Background(), argsJSON(t, map[string]any{"path": "a.png"})); err != nil || len(images) != 1 {
		t.Fatal(err)
	}
	_, images, err := v.ExecuteImages(context.Background(), argsJSON(t, map[string]any{"path": "missing.png"}))
	if err == nil {
		t.Fatal("missing path should error")
	}
	if len(images) != 0 {
		t.Fatalf("images after failure = %v, want empty", images)
	}
}
