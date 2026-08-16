package cdp

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/page"

	"workground2/internal/browser"
)

type fakeDialogExecutor struct {
	mu    sync.Mutex
	calls []bool // accept values, in order
	err   error
}

func (f *fakeDialogExecutor) handle(_ context.Context, accept bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, accept)
	return f.err
}

func (f *fakeDialogExecutor) accepts() []bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]bool, len(f.calls))
	copy(out, f.calls)
	return out
}

// waitFor polls until at least n resolutions have been recorded. Resolutions
// run on a separate goroutine, so tests must wait instead of asserting
// synchronously.
func (f *fakeDialogExecutor) waitFor(t *testing.T, n int) []bool {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		f.mu.Lock()
		got := len(f.calls)
		f.mu.Unlock()
		if got >= n {
			return f.accepts()
		}
		select {
		case <-deadline:
			t.Fatalf("waited for %d dialog resolutions, got %d", n, got)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// newDialogTestDriver builds a driver wired for dialog tests: fake executor,
// one attached target context, and a short settle window.
func newDialogTestDriver(t *testing.T) (*driver, *fakeDialogExecutor) {
	t.Helper()
	exec := &fakeDialogExecutor{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	lifecycle, eventCancel := context.WithCancel(context.Background())
	t.Cleanup(eventCancel)
	d := &driver{
		dialogs:        make(map[string]*dialogPolicy),
		dialogExec:     exec,
		dialogSettle:   20 * time.Millisecond,
		targetContexts: map[string]context.Context{"tab-1": ctx, "tab-2": ctx},
		events:         make(chan browser.Invalidation, 8),
		invalCh:        make(chan browser.Invalidation, 8),
		eventDone:      make(chan struct{}),
		eventCancel:    eventCancel,
	}
	go d.runEventLoop(lifecycle)
	return d, exec
}

func beforeunloadEvent() *page.EventJavascriptDialogOpening {
	return &page.EventJavascriptDialogOpening{
		Message: "Leave site?",
		Type:    page.DialogTypeBeforeunload,
	}
}

func TestDecideDialogAccept(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dialogType page.DialogType
		allowLeave bool
		want       bool
	}{
		{"alert always accepted", page.DialogTypeAlert, false, true},
		{"alert accepted with leave too", page.DialogTypeAlert, true, true},
		{"confirm default dismissed", page.DialogTypeConfirm, false, false},
		{"confirm dismissed even with leave", page.DialogTypeConfirm, true, false},
		{"prompt default dismissed", page.DialogTypePrompt, false, false},
		{"prompt dismissed even with leave", page.DialogTypePrompt, true, false},
		{"beforeunload default stays", page.DialogTypeBeforeunload, false, false},
		{"beforeunload leaves when allowed", page.DialogTypeBeforeunload, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decideDialogAccept(tc.dialogType, tc.allowLeave); got != tc.want {
				t.Fatalf("decideDialogAccept(%q, %v) = %v, want %v", tc.dialogType, tc.allowLeave, got, tc.want)
			}
		})
	}
}

func TestDialogPolicyAcquireReleaseCleanup(t *testing.T) {
	d, _ := newDialogTestDriver(t)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	if got := d.dialogs["tab-1"]; got != p {
		t.Fatalf("acquired policy not registered for tab-1")
	}
	d.releaseDialogPolicy("tab-1", p)
	if _, ok := d.dialogs["tab-1"]; ok {
		t.Fatal("released policy still registered")
	}
}

func TestDialogPolicyReleaseDoesNotDropReplacement(t *testing.T) {
	d, _ := newDialogTestDriver(t)
	first := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	second := d.acquireDialogPolicy("tab-1", browser.ActionOptions{AllowLeave: true})
	// The first operation finishes late; releasing it must not unregister the
	// newer policy for the same target.
	d.releaseDialogPolicy("tab-1", first)
	if got := d.dialogs["tab-1"]; got != second {
		t.Fatal("stale release dropped the replacement policy")
	}
	d.releaseDialogPolicy("tab-1", second)
	if _, ok := d.dialogs["tab-1"]; ok {
		t.Fatal("replacement policy not cleaned up")
	}
}

func TestDialogPolicyCrossTabIsolation(t *testing.T) {
	d, _ := newDialogTestDriver(t)
	a := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	b := d.acquireDialogPolicy("tab-2", browser.ActionOptions{AllowLeave: true})
	if got := d.dialogs["tab-1"]; got != a {
		t.Fatal("tab-1 policy lost")
	}
	if got := d.dialogs["tab-2"]; got != b {
		t.Fatal("tab-2 policy lost")
	}
	d.releaseDialogPolicy("tab-1", a)
	if got := d.dialogs["tab-2"]; got != b {
		t.Fatal("releasing tab-1 leaked into tab-2")
	}
}

func TestOnDialogOpeningBeforeUnloadStay(t *testing.T) {
	d, exec := newDialogTestDriver(t)
	opCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	p.setOpCancel(cancel)

	d.onDialogOpening("tab-1", beforeunloadEvent())

	if !p.blocked.Load() {
		t.Fatal("dismissed beforeunload not recorded as blocked")
	}
	if got := exec.waitFor(t, 1); len(got) != 1 || got[0] {
		t.Fatalf("dialog resolution = %v, want [false]", got)
	}
	select {
	case <-opCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("dismissed beforeunload did not interrupt the operation")
	}
	dialog, ok := p.dialog.Load().(browser.DialogContext)
	if !ok || dialog.Type != browser.DialogBeforeUnload || dialog.TargetID != "tab-1" {
		t.Fatalf("blocked dialog context = %+v", dialog)
	}
}

func TestOnDialogOpeningBeforeUnloadAllowLeave(t *testing.T) {
	d, exec := newDialogTestDriver(t)
	opCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{AllowLeave: true})
	p.setOpCancel(cancel)

	d.onDialogOpening("tab-1", beforeunloadEvent())

	if opCtx.Err() != nil {
		t.Fatal("allowed beforeunload must not cancel the operation")
	}
	if p.blocked.Load() {
		t.Fatal("allowed beforeunload must not be recorded as blocked")
	}
	if got := exec.waitFor(t, 1); len(got) != 1 || !got[0] {
		t.Fatalf("dialog resolution = %v, want [true]", got)
	}
}

func TestOnDialogOpeningConfirmIsReportedBlocked(t *testing.T) {
	d, exec := newDialogTestDriver(t)
	opCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	p.setOpCancel(cancel)

	d.onDialogOpening("tab-1", &page.EventJavascriptDialogOpening{
		Message: "Continue?",
		Type:    page.DialogTypeConfirm,
	})
	if got := exec.waitFor(t, 1); len(got) != 1 || got[0] {
		t.Fatalf("confirm resolution = %v, want [false]", got)
	}
	dialog, blocked, err := d.dialogOutcome(p)
	if err != nil || !blocked || dialog.Type != browser.DialogConfirm {
		t.Fatalf("dialogOutcome = (%+v, %v, %v), want blocked confirm", dialog, blocked, err)
	}
	select {
	case <-opCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("dismissed confirm did not interrupt the operation")
	}
}

func TestOnDialogOpeningUnexpectedDialogsDoNotDeadlock(t *testing.T) {
	for _, tc := range []struct {
		name       string
		dialogType page.DialogType
		want       bool
	}{
		{"alert accepted", page.DialogTypeAlert, true},
		{"confirm dismissed", page.DialogTypeConfirm, false},
		{"prompt dismissed", page.DialogTypePrompt, false},
		{"beforeunload dismissed", page.DialogTypeBeforeunload, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, exec := newDialogTestDriver(t)
			// No policy registered: the dialog must still be resolved with the
			// safe defaults instead of hanging the page.
			d.onDialogOpening("tab-1", &page.EventJavascriptDialogOpening{Type: tc.dialogType})
			if got := exec.waitFor(t, 1); len(got) != 1 || got[0] != tc.want {
				t.Fatalf("resolution = %v, want [%v]", got, tc.want)
			}
		})
	}
}

func TestOnDialogOpeningDuplicateAndLateEvents(t *testing.T) {
	d, exec := newDialogTestDriver(t)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	d.onDialogOpening("tab-1", &page.EventJavascriptDialogOpening{Type: page.DialogTypeAlert})
	d.onDialogOpening("tab-1", &page.EventJavascriptDialogOpening{Type: page.DialogTypeConfirm})
	// Late event after the policy was released (e.g. a timer alert firing
	// after the operation finished): resolved with defaults, no panic.
	d.releaseDialogPolicy("tab-1", p)
	d.onDialogOpening("tab-1", &page.EventJavascriptDialogOpening{Type: page.DialogTypeAlert})
	if got := exec.waitFor(t, 3); len(got) != 3 {
		t.Fatalf("resolution calls = %d, want 3: %v", len(got), got)
	}
}

func TestDialogOutcomeReportsBlocked(t *testing.T) {
	d, _ := newDialogTestDriver(t)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	p.markBlocked(browser.DialogContext{TargetID: "tab-1", Type: browser.DialogBeforeUnload, Message: "Leave?"})
	p.markResolved(nil)
	dialog, blocked, err := d.dialogOutcome(p)
	if err != nil || !blocked || dialog.Type != browser.DialogBeforeUnload {
		t.Fatalf("dialogOutcome = (%+v, %v, %v), want blocked beforeunload", dialog, blocked, err)
	}
}

func TestDialogOutcomeUnblocked(t *testing.T) {
	d, _ := newDialogTestDriver(t)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	// No dialog event: outcome reports unblocked after the settle window.
	if _, blocked, err := d.dialogOutcome(p); blocked || err != nil {
		t.Fatalf("dialogOutcome without dialog = blocked %v, err %v", blocked, err)
	}
}

func TestDialogOutcomeExposesResolutionFailure(t *testing.T) {
	d, exec := newDialogTestDriver(t)
	want := errors.New("CDP dialog command failed")
	exec.err = want
	opCtx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	p := d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	p.setOpCancel(cancel)
	d.onDialogOpening("tab-1", beforeunloadEvent())

	dialog, blocked, err := d.dialogOutcome(p)
	if !blocked || !errors.Is(err, want) || dialog.Type != browser.DialogBeforeUnload {
		t.Fatalf("dialogOutcome = (%+v, %v, %v), want blocked resolution failure", dialog, blocked, err)
	}
	if opCtx.Err() == nil {
		t.Fatal("resolution failure did not cancel the initiating operation")
	}
}

func TestHandleTargetEventRoutesDialogOpening(t *testing.T) {
	d, exec := newDialogTestDriver(t)
	d.handleTargetEvent("tab-1", &page.EventJavascriptDialogOpening{
		Message: "Hello",
		Type:    page.DialogTypeAlert,
	})
	if got := exec.waitFor(t, 1); len(got) != 1 || !got[0] {
		t.Fatalf("dialog resolution = %v, want [true]", got)
	}
}

func TestDialogPolicyClosedAfterDriverClose(t *testing.T) {
	d, _ := newDialogTestDriver(t)
	d.acquireDialogPolicy("tab-1", browser.ActionOptions{})
	d.Close()
	if got := len(d.dialogs); got != 0 {
		t.Fatalf("dialogs after Close = %d, want 0", got)
	}
}
