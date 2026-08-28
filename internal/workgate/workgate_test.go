package workgate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFileMissingDefaultsToRunning(t *testing.T) {
	f := OpenFile(filepath.Join(t.TempDir(), "workcontrol.json"))
	if f.State() != Running {
		t.Fatalf("State() = %q, want running", f.State())
	}
	if f.Epoch() != 1 {
		t.Fatalf("Epoch() = %d, want 1", f.Epoch())
	}
	if !f.Allowed() {
		t.Fatalf("Allowed() = false, want true for missing file")
	}
	if f.LastErr() != nil {
		t.Fatalf("LastErr() = %v, want nil", f.LastErr())
	}
}

func TestFileReadsStateAndEpoch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "workcontrol.json")
	for _, tc := range []struct {
		body        string
		state       State
		epoch       int64
		revision    int64
		allowed     bool
		allowResume bool
	}{
		{`{"state":"running","epoch":3,"fence":"x"}`, Running, 3, 1, true, true},
		{`{"state":"quiescing","epoch":4,"fence":"x"}`, Quiescing, 4, 1, false, false},
		{`{"state":"paused","epoch":5,"fence":"x"}`, Paused, 5, 1, false, false},
		{`{"state":"recovering","epoch":6,"revision":9,"fence":"x"}`, Recovering, 6, 9, false, true},
	} {
		if err := os.WriteFile(path, []byte(tc.body), 0o600); err != nil {
			t.Fatal(err)
		}
		f := OpenFile(path)
		if got := f.State(); got != tc.state {
			t.Errorf("body %s: State() = %q, want %q", tc.body, got, tc.state)
		}
		if got := f.Epoch(); got != tc.epoch {
			t.Errorf("body %s: Epoch() = %d, want %d", tc.body, got, tc.epoch)
		}
		if got := f.Revision(); got != tc.revision {
			t.Errorf("body %s: Revision() = %d, want %d", tc.body, got, tc.revision)
		}
		if got := f.Allowed(); got != tc.allowed {
			t.Errorf("body %s: Allowed() = %v, want %v", tc.body, got, tc.allowed)
		}
		if got := f.AllowedResume(); got != tc.allowResume {
			t.Errorf("body %s: AllowedResume() = %v, want %v", tc.body, got, tc.allowResume)
		}
	}
}

func TestFileParseFailureFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workcontrol.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f := OpenFile(path)
	if f.State() != Paused {
		t.Fatalf("State() = %q, want paused (fail-closed)", f.State())
	}
	if f.Allowed() {
		t.Fatalf("Allowed() = true, want false for unparseable file")
	}
	if f.LastErr() == nil {
		t.Fatalf("LastErr() = nil, want a parse error to be observable")
	}
}

func TestFileInvalidStateFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workcontrol.json")
	if err := os.WriteFile(path, []byte(`{"state":"exploded","epoch":9}`), 0o600); err != nil {
		t.Fatal(err)
	}
	f := OpenFile(path)
	if f.State() != Paused {
		t.Fatalf("State() = %q, want paused (fail-closed)", f.State())
	}
	if f.LastErr() == nil {
		t.Fatalf("LastErr() = nil, want an invalid-state error to be observable")
	}
}
