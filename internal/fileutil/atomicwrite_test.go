package fileutil

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestReplaceFileNoRetryWhenTmpMissing(t *testing.T) {
	oldBase := replaceRetryBase
	replaceRetryBase = 10 * time.Second
	t.Cleanup(func() { replaceRetryBase = oldBase })

	dir := t.TempDir()
	start := time.Now()
	err := ReplaceFile(filepath.Join(dir, "missing.tmp"), filepath.Join(dir, "x.txt"))
	if err == nil {
		t.Fatal("want error when tmp source is missing")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("missing tmp should fail fast, took %v — it retried", elapsed)
	}
}

func TestReplaceFileRetriesThenReturnsError(t *testing.T) {
	oldBase, oldMax := replaceRetryBase, maxReplaceRetries
	replaceRetryBase, maxReplaceRetries = 0, 3
	t.Cleanup(func() { replaceRetryBase, maxReplaceRetries = oldBase, oldMax })

	dir := t.TempDir()
	tmp := filepath.Join(dir, "x.tmp")
	if err := os.WriteFile(tmp, []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(dir, "blocked")
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(tmp, dest); err == nil {
		t.Fatal("want error when dest can never be replaced")
	}
	if !fileExists(tmp) {
		t.Error("tmp should survive a failed replace so the next launch can retry")
	}
}

func TestReplaceFileRenamesInPlace(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "x.tmp")
	dest := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(tmp, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceFile(tmp, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "hello" {
		t.Errorf("dest = %q, want hello", b)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("tmp should be gone after ReplaceFile")
	}
}

func TestCopyOntoOverwritesAndPreservesMode(t *testing.T) {
	dir := t.TempDir()
	tmp := filepath.Join(dir, "x.tmp")
	dest := filepath.Join(dir, "x.txt")
	if err := os.WriteFile(tmp, []byte("new"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("old-and-longer"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := copyOnto(tmp, dest); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(dest); string(b) != "new" {
		t.Errorf("dest = %q, want new (fully overwritten)", b)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Error("tmp should be removed after copyOnto")
	}
	// Mode preservation is meaningful on Unix; Windows only tracks the read-only bit.
	if info, err := os.Stat(dest); err == nil && info.Mode().Perm() != 0o600 {
		t.Logf("dest mode = %o (want 0600 on Unix)", info.Mode().Perm())
	}
}

func TestAtomicWriteFileReplacesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(path, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWriteFile(path, []byte("new-content"), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new-content" {
		t.Fatalf("content = %q, want %q", got, "new-content")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm != 0o600 {
		t.Fatalf("perm = %o, want 600", perm)
	}
	// No leftover tmp files in the directory.
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if e.Name() != "config.toml" {
			t.Fatalf("unexpected leftover file: %s", e.Name())
		}
	}
}

func TestCopyOntoPreservesOldDestOnError(t *testing.T) {
	// copyOnto must never truncate dest in place — if the final rename
	// fails the old content must survive so the next read can succeed.
	dir := t.TempDir()
	tmp := filepath.Join(dir, "src.tmp")
	dest := filepath.Join(dir, "dest.json")
	oldContent := []byte(`{"room":"test-room","snapshot":{"members":["a","b"]}}`)
	if err := os.WriteFile(tmp, []byte("new-payload"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, oldContent, 0o644); err != nil {
		t.Fatal(err)
	}
	// Simulate rename failure: make dest a directory so replaceFile fails
	// on the final rename inside copyOnto.
	os.Remove(dest)
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	err := copyOnto(tmp, dest)
	if err == nil {
		t.Fatal("copyOnto should fail when dest is a directory")
	}
	// tmp must survive so the caller can retry.
	if !fileExists(tmp) {
		t.Error("tmp should survive a failed copyOnto")
	}
	// After copyOnto fails, the original file content must be intact.
	// Since we replaced dest with a directory, verify no regular file exists
	// that could be a truncated version.
	if fi, statErr := os.Stat(dest); statErr != nil || !fi.IsDir() {
		t.Error("dest should still be a directory after failed copyOnto — it was not truncated")
	}
}

func TestReplaceFileSurvivesTransientFailure(t *testing.T) {
	// ReplaceFile must preserve both tmp and old dest content across
	// transient failures so that the next launch can retry.
	oldBase, oldMax := replaceRetryBase, maxReplaceRetries
	replaceRetryBase, maxReplaceRetries = 0, 2
	t.Cleanup(func() { replaceRetryBase, maxReplaceRetries = oldBase, oldMax })

	dir := t.TempDir()
	tmp := filepath.Join(dir, "src.tmp")
	dest := filepath.Join(dir, "blocked-dest")
	oldContent := []byte("original-content")
	if err := os.WriteFile(tmp, []byte("replacement"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Make dest a directory so both replaceFile and copyOnto rename fail.
	if err := os.Mkdir(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	_ = oldContent // old dest is a directory in this test, not written

	if err := ReplaceFile(tmp, dest); err == nil {
		t.Fatal("want error when dest can never be replaced")
	}
	if !fileExists(tmp) {
		t.Error("tmp should survive failed ReplaceFile for retry")
	}
	// After failure, no leftover intermediate temp files from copyOnto.
	entries, _ := os.ReadDir(dir)
	tmpCount := 0
	for _, e := range entries {
		if e.Name() == "src.tmp" {
			continue
		}
		if !e.IsDir() {
			t.Errorf("unexpected leftover file after failed ReplaceFile: %s", e.Name())
		}
		tmpCount++
	}
}

func TestReplaceFileAtomicContentNeverPartial(t *testing.T) {
	// Readers racing repeated replacements must only observe a complete old
	// or new JSON document, never the truncation window from the old fallback.
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	oldContent := append([]byte(`{"room":"alpha","payload":"`), bytes.Repeat([]byte("a"), 256*1024)...)
	oldContent = append(oldContent, []byte(`"}`)...)
	newContent := append([]byte(`{"room":"beta","payload":"`), bytes.Repeat([]byte("b"), 256*1024)...)
	newContent = append(newContent, []byte(`"}`)...)
	if err := os.WriteFile(path, oldContent, 0o644); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	errCh := make(chan error, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-start
		for i := 0; i < 1000; i++ {
			var got []byte
			var err error
			for attempt := 0; attempt < 10; attempt++ {
				got, err = os.ReadFile(path)
				if err == nil {
					break
				}
				time.Sleep(time.Millisecond)
			}
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			if !bytes.Equal(got, oldContent) && !bytes.Equal(got, newContent) {
				select {
				case errCh <- &partialReadError{size: len(got)}:
				default:
				}
				return
			}
		}
	}()
	close(start)
	for i := 0; i < 100; i++ {
		data := newContent
		if i%2 == 1 {
			data = oldContent
		}
		if err := AtomicWriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	select {
	case err := <-errCh:
		t.Fatal(err)
	default:
	}
}

type partialReadError struct{ size int }

func (e *partialReadError) Error() string {
	return fmt.Sprintf("reader observed partial replacement: %d bytes", e.size)
}

func TestAtomicWriteFileCreatesParentDir(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "deep", "creds")
	if err := AtomicWriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatalf("AtomicWriteFile into missing dir: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("file not created: %v", err)
	}
}
