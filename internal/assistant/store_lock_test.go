package assistant

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestStoreLockSerializesProcesses(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	store := testStore(t, root)
	unlock, err := store.lockAssistant("helper-a")
	if err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestStoreLockHelperProcess$")
	cmd.Env = append(os.Environ(), "WORKGROUND2_ASSISTANT_LOCK_HELPER=1", "WORKGROUND2_ASSISTANT_LOCK_ROOT="+root)
	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		unlock()
		t.Fatalf("helper bypassed held cross-process lock: %v", err)
	case <-time.After(250 * time.Millisecond):
	}
	unlock()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatal("helper did not acquire released cross-process lock")
	}
}

func TestStoreLockHelperProcess(t *testing.T) {
	if os.Getenv("WORKGROUND2_ASSISTANT_LOCK_HELPER") != "1" {
		t.Skip("subprocess helper")
	}
	store, err := NewStore(os.Getenv("WORKGROUND2_ASSISTANT_LOCK_ROOT"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.lockAssistant("helper-a")
	if err != nil {
		t.Fatal(err)
	}
	unlock()
}
