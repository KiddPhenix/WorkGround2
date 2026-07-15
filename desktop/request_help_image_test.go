package main

import (
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRequestHelpPNG(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestRequestHelpImageDataURLValidatesGeneratedImage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	path := filepath.Join(home, "generated_images", "thread-1", "result.png")
	writeRequestHelpPNG(t, path)

	url, err := (&App{}).RequestHelpImageDataURL(path)
	if err != nil {
		t.Fatalf("RequestHelpImageDataURL: %v", err)
	}
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(url, prefix) {
		t.Fatalf("data URL prefix = %q", url[:min(len(url), len(prefix))])
	}
	if raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(url, prefix)); err != nil || len(raw) == 0 {
		t.Fatalf("decode data URL: len=%d err=%v", len(raw), err)
	}
}

func TestRequestHelpImageRejectsUnsafePathsAndFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("CODEX_HOME", home)
	root := filepath.Join(home, "generated_images")
	valid := filepath.Join(root, "thread-1", "valid.png")
	writeRequestHelpPNG(t, valid)

	outside := filepath.Join(t.TempDir(), "outside.png")
	writeRequestHelpPNG(t, outside)
	badImage := filepath.Join(root, "thread-1", "bad.png")
	if err := os.WriteFile(badImage, []byte("plain text"), 0o644); err != nil {
		t.Fatal(err)
	}
	large := filepath.Join(root, "thread-1", "large.png")
	f, err := os.Create(large)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(maxRequestHelpImageBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	for name, path := range map[string]string{
		"relative": "result.png",
		"outside":  outside,
		"missing":  filepath.Join(root, "thread-1", "missing.png"),
		"nonimage": badImage,
		"oversize": large,
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := readRequestHelpImage(path); err == nil {
				t.Fatalf("readRequestHelpImage(%q) unexpectedly succeeded", path)
			}
		})
	}

	symlink := filepath.Join(root, "thread-1", "link.png")
	if err := os.Symlink(valid, symlink); err == nil {
		if _, _, err := readRequestHelpImage(symlink); err == nil || !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink error = %v", err)
		}
	}
}
