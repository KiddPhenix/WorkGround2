package assistantdaemon

import (
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"workground2/internal/control"
)

// TestDaemonSessionControlConcurrentRestoreBuildsOneController verifies that
// many concurrent requireCtrl calls for the same unloaded Session build exactly
// one Controller (the restore path is serialized and double-checked), not one
// per caller.
func TestDaemonSessionControlConcurrentRestoreBuildsOneController(t *testing.T) {
	var builds atomic.Int64
	start := make(chan struct{})
	release := make(chan struct{})
	c := newDaemonSessionControl("model", io.Discard, nil, nil)
	c.build = func(sessionID string) (*control.Controller, error) {
		builds.Add(1)
		close(start) // only the first builder reaches here under restoreMu
		<-release    // hold the lock so every concurrent caller queues behind it
		return &control.Controller{}, nil
	}

	const n = 32
	var wg sync.WaitGroup
	errs := make([]error, n)
	ctrls := make([]*control.Controller, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ctrl, err := c.requireCtrl("session-1")
			ctrls[i], errs[i] = ctrl, err
		}(i)
	}

	// Wait until the single builder is in-flight, then release it.
	<-start
	close(release)
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("requireCtrl %d: %v", i, errs[i])
		}
		if ctrls[i] == nil {
			t.Fatalf("requireCtrl %d returned nil controller", i)
		}
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls = %d, want exactly 1", got)
	}
	// All callers observed the same controller instance.
	for i := 1; i < n; i++ {
		if ctrls[i] != ctrls[0] {
			t.Fatalf("concurrent restore returned divergent controllers: %p vs %p", ctrls[i], ctrls[0])
		}
	}
}

// TestDaemonSessionControlRepeatedRestoreReusesLiveController verifies that once
// a Session is loaded, a later requireCtrl returns the cached controller without
// re-building.
func TestDaemonSessionControlRepeatedRestoreReusesLiveController(t *testing.T) {
	var builds atomic.Int64
	c := newDaemonSessionControl("model", io.Discard, nil, nil)
	c.build = func(sessionID string) (*control.Controller, error) {
		builds.Add(1)
		return &control.Controller{}, nil
	}
	if _, err := c.requireCtrl("session-1"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.requireCtrl("session-1"); err != nil {
		t.Fatal(err)
	}
	if got := builds.Load(); got != 1 {
		t.Fatalf("build calls = %d, want 1 (second require must reuse the live controller)", got)
	}
}
