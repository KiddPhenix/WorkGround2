package cdp

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"workground2/internal/browser"
)

// fakeLauncher stands in for *driver in launchWithRetry tests. The embedded
// nil browser.Driver keeps the type honest without implementing every method.
type fakeLauncher struct {
	browser.Driver
	startErr   error
	closeCalls int
}

func (f *fakeLauncher) start(context.Context) error { return f.startErr }
func (f *fakeLauncher) Close() error {
	f.closeCalls++
	return nil
}

func portConflictErr() error {
	return errors.New("browser startup failed: chrome failed to start:\n[1:2:ERROR:tcp_server_socket_posix.cc(1)] bind() returned an error, errno=98 (Address already in use)")
}

func TestLaunchWithRetrySucceedsAfterTwoConflicts(t *testing.T) {
	var created []*fakeLauncher
	makeDriver := func() driverLauncher {
		f := &fakeLauncher{startErr: portConflictErr()}
		if len(created) == 2 {
			f.startErr = nil
		}
		created = append(created, f)
		return f
	}
	got, err := launchWithRetry(context.Background(), makeDriver, maxLaunchAttempts)
	if err != nil {
		t.Fatalf("launchWithRetry: %v", err)
	}
	if len(created) != 3 {
		t.Fatalf("created %d drivers, want 3", len(created))
	}
	if got != driverLauncher(created[2]) {
		t.Fatalf("returned driver is not the third attempt")
	}
	if created[0].closeCalls != 1 || created[1].closeCalls != 1 || created[2].closeCalls != 0 {
		t.Fatalf("close counts = %d/%d/%d, want 1/1/0", created[0].closeCalls, created[1].closeCalls, created[2].closeCalls)
	}
}

func TestLaunchWithRetryFailsAfterThreeConflicts(t *testing.T) {
	var created []*fakeLauncher
	makeDriver := func() driverLauncher {
		f := &fakeLauncher{startErr: portConflictErr()}
		created = append(created, f)
		return f
	}
	_, err := launchWithRetry(context.Background(), makeDriver, maxLaunchAttempts)
	if err == nil {
		t.Fatal("launchWithRetry succeeded, want explicit failure")
	}
	if !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("final error %q missing attempt count", err)
	}
	if !strings.Contains(strings.ToLower(err.Error()), "address already in use") {
		t.Fatalf("final error %q missing root cause", err)
	}
	if len(created) != 3 {
		t.Fatalf("created %d drivers, want 3", len(created))
	}
	for i, f := range created {
		if f.closeCalls != 1 {
			t.Fatalf("failed attempt %d closed %d times, want 1", i, f.closeCalls)
		}
	}
}

func TestLaunchWithRetryDoesNotRetryNonPortError(t *testing.T) {
	want := errors.New("browser startup failed: executable file not found")
	var created []*fakeLauncher
	makeDriver := func() driverLauncher {
		f := &fakeLauncher{startErr: want}
		created = append(created, f)
		return f
	}
	_, err := launchWithRetry(context.Background(), makeDriver, maxLaunchAttempts)
	if !errors.Is(err, want) {
		t.Fatalf("launchWithRetry error = %v, want original non-port error", err)
	}
	if strings.Contains(err.Error(), "attempts") {
		t.Fatalf("non-port error was wrapped with attempt count: %v", err)
	}
	if len(created) != 1 {
		t.Fatalf("created %d drivers, want 1 (no retry)", len(created))
	}
	if created[0].closeCalls != 1 {
		t.Fatalf("failed attempt closed %d times, want 1", created[0].closeCalls)
	}
}

func TestLaunchWithRetryRetriesProbeFailure(t *testing.T) {
	want := errors.New("loopback listen denied")
	var created []*fakeLauncher
	makeDriver := func() driverLauncher {
		f := &fakeLauncher{startErr: &debugPortUnavailable{cause: fmt.Errorf("pick debug port: %w", want)}}
		created = append(created, f)
		return f
	}
	_, err := launchWithRetry(context.Background(), makeDriver, maxLaunchAttempts)
	if err == nil || !errors.Is(err, want) || !strings.Contains(err.Error(), "3 attempts") {
		t.Fatalf("launchWithRetry error = %v, want probe cause and attempt count", err)
	}
	if len(created) != maxLaunchAttempts {
		t.Fatalf("created %d drivers, want %d", len(created), maxLaunchAttempts)
	}
	for i, f := range created {
		if f.closeCalls != 1 {
			t.Fatalf("failed probe attempt %d closed %d times, want 1", i, f.closeCalls)
		}
	}
}

func TestLaunchWithRetryFirstAttemptSucceeds(t *testing.T) {
	var created []*fakeLauncher
	makeDriver := func() driverLauncher {
		f := &fakeLauncher{}
		created = append(created, f)
		return f
	}
	got, err := launchWithRetry(context.Background(), makeDriver, maxLaunchAttempts)
	if err != nil {
		t.Fatalf("launchWithRetry: %v", err)
	}
	if got != driverLauncher(created[0]) || len(created) != 1 {
		t.Fatalf("returned driver/attempts mismatch: created=%d", len(created))
	}
	if created[0].closeCalls != 0 {
		t.Fatalf("successful attempt closed %d times, want 0", created[0].closeCalls)
	}
}
