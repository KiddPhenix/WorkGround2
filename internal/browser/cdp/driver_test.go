package cdp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/dom"

	"workground2/internal/browser"
)

func TestOperationContextHonorsCallAndLifecycleCancellation(t *testing.T) {
	for _, tc := range []struct {
		name            string
		cancelCall      bool
		cancelLifecycle bool
	}{
		{name: "call", cancelCall: true},
		{name: "lifecycle", cancelLifecycle: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			lifecycle, cancelLifecycle := context.WithCancel(context.Background())
			call, cancelCall := context.WithCancel(context.Background())
			defer cancelLifecycle()
			defer cancelCall()
			d := &driver{cdpCtx: lifecycle}
			op, cancel, err := d.operationContext(call)
			if err != nil {
				t.Fatal(err)
			}
			defer cancel()
			if tc.cancelCall {
				cancelCall()
			}
			if tc.cancelLifecycle {
				cancelLifecycle()
			}
			select {
			case <-op.Done():
			case <-time.After(time.Second):
				t.Fatal("operation context ignored cancellation")
			}
		})
	}
}

func TestOperationContextDoesNotClampLongCallerDeadline(t *testing.T) {
	lifecycle, cancelLifecycle := context.WithCancel(context.Background())
	defer cancelLifecycle()
	call, cancelCall := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancelCall()
	d := &driver{cdpCtx: lifecycle}
	op, cancel, err := d.operationContext(call)
	if err != nil {
		t.Fatal(err)
	}
	defer cancel()
	if deadline, ok := op.Deadline(); ok && time.Until(deadline) < 90*time.Second {
		t.Fatalf("driver clamped caller deadline: %s", time.Until(deadline))
	}
}

func TestInvalidationsAndConcurrentCloseDoNotPanic(t *testing.T) {
	lifecycle, eventCancel := context.WithCancel(context.Background())
	d := &driver{
		events: make(chan browser.Invalidation, 128), invalCh: make(chan browser.Invalidation, 64),
		eventDone: make(chan struct{}), eventCancel: eventCancel,
	}
	go d.runEventLoop(lifecycle)

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 100; n++ {
				d.handleEvent(&dom.EventDocumentUpdated{})
			}
		}()
	}
	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		_ = d.Close()
		_ = d.Close()
	}()
	wg.Wait()
	select {
	case <-closeDone:
	case <-time.After(time.Second):
		t.Fatal("Close deadlocked")
	}
	for range d.Invalidations() {
	}
}

func TestHandleEventsMapToInvalidations(t *testing.T) {
	lifecycle, eventCancel := context.WithCancel(context.Background())
	d := &driver{
		events: make(chan browser.Invalidation, 16), invalCh: make(chan browser.Invalidation, 16),
		eventDone: make(chan struct{}), eventCancel: eventCancel, activeTarget: "tab-1",
	}
	go d.runEventLoop(lifecycle)
	d.handleEvent(&dom.EventDocumentUpdated{})
	select {
	case got := <-d.Invalidations():
		if got.Kind != browser.InvalidationDocument || got.TargetID != "tab-1" {
			t.Fatalf("unexpected invalidation: %+v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no DOM invalidation")
	}
	_ = d.Close()
}

func TestConsumeCloseErrorOnlyBlocksFirstRetry(t *testing.T) {
	want := errors.New("graceful close failed")
	d := &driver{closeErr: want, closeErrPending: true}
	if got := d.consumeCloseError(); !errors.Is(got, want) {
		t.Fatalf("first close error = %v, want %v", got, want)
	}
	if got := d.consumeCloseError(); got != nil {
		t.Fatalf("stale close error pinned retry: %v", got)
	}
}

func TestConsumeCloseErrorConcurrentReturnsErrorOnce(t *testing.T) {
	want := errors.New("graceful close failed")
	d := &driver{closeErr: want, closeErrPending: true}
	const callers = 8
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() { results <- d.consumeCloseError() }()
	}
	seen := 0
	for i := 0; i < callers; i++ {
		if errors.Is(<-results, want) {
			seen++
		}
	}
	if seen != 1 {
		t.Fatalf("close error returned %d times, want 1", seen)
	}
}

func TestValidateBrowserVersion(t *testing.T) {
	for _, tc := range []struct {
		name     string
		kind     browser.BrowserKind
		product  string
		protocol string
		wantErr  bool
	}{
		{"chrome", browser.BrowserChrome, "Chrome/151.0", "1.3", false},
		{"chromium", browser.BrowserChromium, "Chromium/151.0", "1.3", false},
		{"edge", browser.BrowserEdge, "Edg/151.0", "1.3", false},
		{"empty protocol", browser.BrowserChrome, "Chrome/151.0", "", true},
		{"wrong family", browser.BrowserEdge, "Chrome/151.0", "1.3", true},
		{"firefox", browser.BrowserChrome, "Firefox/150", "1.3", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateBrowserVersion(tc.kind, tc.product, tc.protocol); (err != nil) != tc.wantErr {
				t.Fatalf("error=%v wantErr=%v", err, tc.wantErr)
			}
		})
	}
}
