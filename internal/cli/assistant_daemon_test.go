package cli

import (
	"path/filepath"
	"testing"
)

func TestAssistantDaemonOnceWithEmptyStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	if code := assistantCommand([]string{"daemon", "--once", "--store", root}); code != 0 {
		t.Fatalf("assistant daemon --once exit code = %d", code)
	}
}

func TestAssistantDaemonRejectsUnknownMode(t *testing.T) {
	if code := assistantCommand([]string{"remote"}); code != 2 {
		t.Fatalf("assistant remote exit code = %d", code)
	}
}
