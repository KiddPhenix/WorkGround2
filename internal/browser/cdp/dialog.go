package cdp

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"

	"workground2/internal/browser"
)

// dialogExecutor performs the CDP dialog resolution. It is an interface so
// unit tests can record resolutions without a real browser.
type dialogExecutor interface {
	handle(ctx context.Context, accept bool) error
}

type cdpDialogExecutor struct{}

// handle dismisses/accepts the dialog. The command runs through chromedp.Run
// so the action context carries the CDP executor: a bare chromedp context has
// no executor value and Do would fail with ErrInvalidContext.
func (cdpDialogExecutor) handle(ctx context.Context, accept bool) error {
	return chromedp.Run(ctx, chromedp.ActionFunc(func(actCtx context.Context) error {
		return page.HandleJavaScriptDialog(accept).Do(actCtx)
	}))
}

// dialogPolicy is the per-target dialog resolution policy for one in-flight
// operation. It lives in driver.dialogs from acquire to release, keyed by
// target ID so concurrent tabs never share policy.
type dialogPolicy struct {
	allowLeave bool
	// opCancel interrupts the in-flight operation when a dialog is dismissed,
	// so navigation/click/close reports dialog_blocked instead of hanging until
	// the action timeout. Stored atomically: set by the
	// operation goroutine, read by the event goroutine.
	opCancel atomic.Value // context.CancelFunc
	// blocked reports that a confirm/prompt/beforeunload dialog was dismissed.
	blocked atomic.Bool
	// dialog is the most recent blocked dialog context (browser.DialogContext).
	dialog atomic.Value
	// settled is closed once an opening event was processed (or the settle
	// window elapsed) so actions can report a prompt result.
	settled    chan struct{}
	settleOnce sync.Once
	// resolved is closed once the dialog dismissal/acceptance CDP command
	// completed (or failed), so the next operation cannot observe a dialog
	// that is still open and block the page.
	resolved    chan struct{}
	resolveOnce sync.Once
	// resolveResult is written before resolved closes. A wrapper keeps the
	// atomic value's concrete type stable across different error types.
	resolveResult atomic.Value // dialogResolveResult
}

type dialogResolveResult struct{ err error }

func newDialogPolicy(allowLeave bool) *dialogPolicy {
	return &dialogPolicy{
		allowLeave: allowLeave,
		settled:    make(chan struct{}),
		resolved:   make(chan struct{}),
	}
}

func (p *dialogPolicy) setOpCancel(cancel context.CancelFunc) {
	if cancel != nil {
		p.opCancel.Store(cancel)
	}
}

func (p *dialogPolicy) cancelOperation() {
	if cancel, ok := p.opCancel.Load().(context.CancelFunc); ok && cancel != nil {
		cancel()
	}
}

func (p *dialogPolicy) markBlocked(dialog browser.DialogContext) {
	p.blocked.Store(true)
	p.dialog.Store(dialog)
	p.settle()
}

func (p *dialogPolicy) settle() {
	p.settleOnce.Do(func() { close(p.settled) })
}

func (p *dialogPolicy) markResolved(err error) {
	p.resolveOnce.Do(func() {
		p.resolveResult.Store(dialogResolveResult{err: err})
		close(p.resolved)
	})
}

func (p *dialogPolicy) resolutionError() error {
	result, _ := p.resolveResult.Load().(dialogResolveResult)
	return result.err
}

// decideDialogAccept returns whether a dialog of the given type should be
// accepted (HandleJavaScriptDialog accept=true) under the policy. Alerts are
// always accepted so pages never deadlock on them; confirm/prompt default to
// dismiss; beforeunload is accepted only when the operation explicitly opted
// into leaving.
func decideDialogAccept(dialogType page.DialogType, allowLeave bool) bool {
	switch dialogType {
	case page.DialogTypeAlert:
		return true
	case page.DialogTypeBeforeunload:
		return allowLeave
	default: // confirm, prompt
		return false
	}
}

// acquireDialogPolicy registers the policy for one operation on targetID.
// A later operation on the same target replaces the previous policy, which is
// fine because operations on one target are serialized by the Manager.
func (d *driver) acquireDialogPolicy(targetID string, opts browser.ActionOptions) *dialogPolicy {
	p := newDialogPolicy(opts.AllowLeave)
	d.mu.Lock()
	if d.dialogs == nil {
		d.dialogs = make(map[string]*dialogPolicy)
	}
	d.dialogs[targetID] = p
	d.mu.Unlock()
	return p
}

// releaseDialogPolicy removes the policy, but only if it is still the current
// one for the target (a later operation may have replaced it).
func (d *driver) releaseDialogPolicy(targetID string, p *dialogPolicy) {
	d.mu.Lock()
	if d.dialogs[targetID] == p {
		delete(d.dialogs, targetID)
	}
	d.mu.Unlock()
}

// onDialogOpening resolves a JavaScript dialog promptly and records a
// dismissed dialog as a blocked outcome for the initiating operation. Dialogs
// without an active policy (spontaneous alerts, late events after release) are
// still resolved with the safe defaults so nothing ever deadlocks.
//
// The resolution runs in its own goroutine on purpose: chromedp dispatches
// events from the same goroutine that consumes command responses, so executing
// HandleJavaScriptDialog synchronously inside this callback would wait for a
// response that can never be delivered (deadlock). The callback itself only
// mutates the policy (non-blocking) and hands the CDP round-trip to a goroutine.
func (d *driver) onDialogOpening(targetID string, ev *page.EventJavascriptDialogOpening) {
	d.mu.RLock()
	p := d.dialogs[targetID]
	d.mu.RUnlock()

	accept := decideDialogAccept(ev.Type, p != nil && p.allowLeave)

	// A dismissed beforeunload/confirm/prompt means the initiating operation
	// did not reach its requested result. Record it and interrupt the command
	// after the CDP dismissal completes so callers see dialog_blocked instead
	// of a false success or a generic timeout.
	if !accept && p != nil {
		p.markBlocked(browser.DialogContext{
			TargetID:      targetID,
			Type:          browser.DialogType(ev.Type),
			Message:       ev.Message,
			DefaultPrompt: ev.DefaultPrompt,
		})
		d.resolveDialogFor(targetID, p, false)
		return
	}
	if p != nil {
		p.settle()
	}
	d.resolveDialogFor(targetID, p, accept)
}

// dialogOutcome waits briefly for an in-flight dialog event to land after the
// underlying CDP action returned, then reports whether the operation was
// blocked by a dismissed dialog. The window is polled so a
// blocked dialog is reported in milliseconds; when the window elapses without
// an event the action is treated as unblocked. For blocked outcomes it also
// waits for the dismissal command itself to complete, so the next operation
// never observes a dialog that is still open (which would freeze the page's
// main thread).
func (d *driver) dialogOutcome(p *dialogPolicy) (browser.DialogContext, bool, error) {
	settle := d.dialogSettle
	if settle <= 0 {
		settle = defaultDialogSettle
	}
	deadline := time.Now().Add(settle)
	const pollStep = 10 * time.Millisecond
	for {
		if p.blocked.Load() {
			if err := waitDialogResolution(p); err != nil {
				dialog, _ := p.dialog.Load().(browser.DialogContext)
				return dialog, true, err
			}
			dialog, _ := p.dialog.Load().(browser.DialogContext)
			return dialog, true, nil
		}
		select {
		case <-p.settled:
			// A non-blocking dialog (e.g. an accepted alert) was handled; the
			// action itself succeeded.
			return browser.DialogContext{}, false, waitDialogResolution(p)
		case <-time.After(pollStep):
			if time.Now().After(deadline) {
				return browser.DialogContext{}, false, nil
			}
		}
	}
}

func waitDialogResolution(p *dialogPolicy) error {
	select {
	case <-p.resolved:
		return p.resolutionError()
	case <-time.After(defaultDialogResolveWait):
		return context.DeadlineExceeded
	}
}

func (d *driver) resolveDialogFor(targetID string, p *dialogPolicy, accept bool) {
	ctx, err := d.dialogContextFor(targetID)
	if err != nil {
		if p != nil {
			p.markResolved(err)
			p.cancelOperation()
		}
		return // target gone; the dialog died with it
	}
	// Must not block the event-dispatch goroutine (see onDialogOpening).
	go func() {
		err := d.dialogExec.handle(ctx, accept)
		if p != nil {
			p.markResolved(err)
			// A dismissed dialog must interrupt its initiating command so
			// it returns promptly. A resolution failure also cancels the command
			// and is surfaced as outcome-unknown instead of a generic timeout.
			if p.blocked.Load() || err != nil {
				p.cancelOperation()
			}
		}
	}()
}

func (d *driver) dialogContextFor(targetID string) (context.Context, error) {
	d.mu.RLock()
	ctx := d.targetContexts[targetID]
	if ctx == nil {
		ctx = d.browserBase
	}
	d.mu.RUnlock()
	if ctx == nil {
		return nil, context.Canceled
	}
	return ctx, nil
}
