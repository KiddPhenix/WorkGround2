package assistantdaemon

import (
	"context"
	"io"
	"path/filepath"
	"testing"
)

func TestRuntimeLeaderHandoffWithEmptyStore(t *testing.T) {
	root := filepath.Join(t.TempDir(), "assistants")
	one, err := New(Options{StoreRoot: root, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer one.Close()
	two, err := New(Options{StoreRoot: root, Stderr: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	defer two.Close()

	if err := one.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	first := one.currentLease()
	if first.Fence == "" {
		t.Fatal("first runtime did not acquire leadership")
	}
	if err := two.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := two.currentLease(); got.Fence != "" {
		t.Fatalf("follower retained lease %+v", got)
	}
	if err := one.Close(); err != nil {
		t.Fatal(err)
	}
	if err := two.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := two.currentLease(); got.Fence == "" || got.Fence == first.Fence {
		t.Fatalf("second runtime did not take over: %+v", got)
	}
}
