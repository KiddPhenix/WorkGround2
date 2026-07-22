package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"workground2/internal/control"
	"workground2/internal/work"
)

const workViewEventPrefix = "work:view:"

type workViewWatch struct {
	tabID       string
	workID      string
	broadcaster *control.WorkViewBroadcaster
	streamID    string
	cancel      context.CancelFunc
}

// workController is the local narrow port the desktop needs from a Controller.
// The concrete *control.Controller implements both WorkControl() and WorkViews().
type workController interface {
	WorkControl() control.WorkControl
	WorkViews() *control.WorkViewBroadcaster
}

func (a *App) resolveWorkController(tabID string) (control.WorkControl, error) {
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return nil, fmt.Errorf("workspace is still starting")
	}
	wc, ok := ctrl.(workController)
	if !ok {
		return nil, fmt.Errorf("work: feature not available on this controller")
	}
	wctl := wc.WorkControl()
	return wctl, nil
}

// CreateWork creates a new Work from a Blueprint. RequestID enables idempotent retries.
func (a *App) CreateWork(tabID string, input work.CreateWorkInput) (*work.Work, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.CreateWork(a.bootContext(), input)
}

// GetWork returns the current Work projection.
func (a *App) GetWork(tabID, workID string) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.GetWork(a.bootContext(), workID)
}

// RecoverWorkView performs the authoritative snapshot step of an explicit
// frontend retry or automatic remount hydration. The frontend must first
// install a fresh Watch generation; this method then returns a typed resync
// event with a backend-global generation and EventID.
func (a *App) RecoverWorkView(tabID, workID string, input work.ViewRecoveryIntent) (*work.WorkViewEvent, error) {
	const maxSafeJSONInteger = uint64(1<<53 - 1)
	workID = strings.TrimSpace(workID)
	if workID == "" || (input.Reason != work.ViewResyncRetry && input.Reason != work.ViewResyncHydrate) || input.Generation == 0 || input.Generation > maxSafeJSONInteger {
		return nil, fmt.Errorf("work: valid recovery intent is required")
	}
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return a.authoritativeWorkViewResync(a.bootContext(), wc, workID, input.Reason, input.Generation)
}

// ListWorks returns a filtered summary page.
func (a *App) ListWorks(tabID string, filter work.WorkFilter) (work.WorkPage, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return work.WorkPage{}, err
	}
	return wc.ListWorks(a.bootContext(), filter)
}

// UpdateDraft updates editable draft fields with optimistic concurrency.
func (a *App) UpdateDraft(tabID string, input work.UpdateDraftInput) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.UpdateDraft(a.bootContext(), input)
}

// RunWork starts a Work through the shared Controller. Cornerstone preflight
// remains authoritative in internal/work.Service.
func (a *App) RunWork(tabID, workID, requestID string) (*work.WorkflowRun, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RunWork(a.bootContext(), workID, requestID)
}

// RetryWorkTask adds a new Attempt through the shared Controller.
func (a *App) RetryWorkTask(tabID string, input work.RetryTaskInput) (*work.Attempt, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RetryTask(a.bootContext(), input)
}

// WatchWork bridges the owning Controller's typed WorkView stream to one Wails
// event channel. subscriptionID is opaque UI identity, never business state.
// On overflow recovery signals, an authoritative GetWork snapshot is requested
// and emitted so the frontend never stays stale after a dropped terminal event.
func (a *App) WatchWork(tabID, workID, subscriptionID string) error {
	tabID = strings.TrimSpace(tabID)
	workID = strings.TrimSpace(workID)
	subscriptionID = strings.TrimSpace(subscriptionID)
	if tabID == "" || workID == "" || !validWorkSubscriptionID(subscriptionID) {
		return fmt.Errorf("work: tab, Work, and valid subscription IDs are required")
	}
	_, ctrl := a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return fmt.Errorf("workspace is still starting")
	}
	owner, ok := ctrl.(workController)
	if !ok || owner.WorkViews() == nil {
		return fmt.Errorf("work: event stream not available on this controller")
	}
	wctl := owner.WorkControl()

	a.workWatchMu.Lock()
	if existing := a.workWatches[subscriptionID]; existing != nil {
		a.workWatchMu.Unlock()
		if existing.tabID == tabID && existing.workID == workID {
			return nil
		}
		return fmt.Errorf("work: subscription ID is already in use")
	}
	broadcaster := owner.WorkViews()
	streamID, events, overflows := broadcaster.SubscribeWorkReliable(workID, 32)
	ctx, cancel := context.WithCancel(a.bootContext())
	watch := &workViewWatch{
		tabID: tabID, workID: workID,
		broadcaster: broadcaster, streamID: streamID, cancel: cancel,
	}
	if a.workWatches == nil {
		a.workWatches = map[string]*workViewWatch{}
	}
	a.workWatches[subscriptionID] = watch
	a.workWatchMu.Unlock()

	go func() {
		defer a.stopWorkWatch(subscriptionID, watch)
		for {
			select {
			case <-ctx.Done():
				return
			case _, open := <-overflows:
				if !open {
					return
				}
				if err := a.recoverWorkViewFromOverflow(ctx, wctl, workID, subscriptionID); err != nil {
					a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, work.WorkViewEvent{
						SchemaVersion: work.WorkViewSchemaVersion,
						Type:          work.ViewAttention,
						WorkID:        workID,
						EventID:       fmt.Sprintf("wv-recover-failed-%s-%d", workID, time.Now().UnixNano()),
						RequestID:     "overflow-recovery-failed",
						Object:        work.ObjectContext{Kind: work.ObjectWork, ID: workID},
						Payload:       json.RawMessage(`{"overflow":true,"recovery":"failed","retryable":true}`),
						CreatedAt:     time.Now().UTC(),
					})
				}
			case event, open := <-events:
				if !open {
					return
				}
				a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, event)
			}
		}
	}()
	return nil
}

func (a *App) recoverWorkViewFromOverflow(ctx context.Context, wc control.WorkControl, workID, subscriptionID string) error {
	event, err := a.authoritativeWorkViewResync(ctx, wc, workID, work.ViewResyncOverflow, 0)
	if err != nil {
		return err
	}
	a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, *event)
	return nil
}

func (a *App) authoritativeWorkViewResync(ctx context.Context, wc control.WorkControl, workID string, reason work.ViewResyncReason, minGeneration uint64) (*work.WorkViewEvent, error) {
	view, err := wc.GetWork(ctx, workID)
	if err != nil || view == nil {
		if err != nil {
			return nil, fmt.Errorf("work: recover authoritative snapshot: %w", err)
		}
		return nil, fmt.Errorf("work: recover authoritative snapshot: empty projection")
	}
	payload, err := json.Marshal(view)
	if err != nil {
		return nil, fmt.Errorf("work: encode authoritative snapshot: %w", err)
	}
	generation := a.nextWorkResyncGeneration(minGeneration)
	event := &work.WorkViewEvent{
		SchemaVersion: work.WorkViewSchemaVersion,
		Type:          work.ViewSnapshot,
		WorkID:        workID,
		EventID:       work.ResyncEventID(workID, view.Revision, reason, generation),
		Revision:      view.Revision,
		RequestID:     string(reason) + "-recovery",
		Object:        work.ObjectContext{Kind: work.ObjectWork, ID: workID},
		Resync: &work.ViewResync{
			Reason:        reason,
			Authoritative: true,
			Generation:    generation,
		},
		Payload:   json.RawMessage(payload),
		CreatedAt: time.Now().UTC(),
	}
	if err := event.Validate(); err != nil {
		return nil, fmt.Errorf("work: build authoritative resync: %w", err)
	}
	return event, nil
}

func (a *App) nextWorkResyncGeneration(min uint64) uint64 {
	for {
		current := a.workResyncGeneration.Load()
		next := current + 1
		if min > next {
			next = min
		}
		if a.workResyncGeneration.CompareAndSwap(current, next) {
			return next
		}
	}
}

// UnwatchWork idempotently removes a transient Wails WorkView subscription.
func (a *App) UnwatchWork(subscriptionID string) {
	a.stopWorkWatch(strings.TrimSpace(subscriptionID), nil)
}

func (a *App) stopWorkWatch(subscriptionID string, expected *workViewWatch) {
	a.workWatchMu.Lock()
	watch := a.workWatches[subscriptionID]
	if watch == nil || expected != nil && watch != expected {
		a.workWatchMu.Unlock()
		return
	}
	delete(a.workWatches, subscriptionID)
	a.workWatchMu.Unlock()
	watch.cancel()
	watch.broadcaster.Unsubscribe(watch.streamID)
}

func validWorkSubscriptionID(value string) bool {
	if len(value) < 8 || len(value) > 96 {
		return false
	}
	for _, char := range value {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '-' || char == '_' {
			continue
		}
		return false
	}
	return true
}

// ArchiveWork archives a Work and produces an immutable WorkRecord.
func (a *App) ArchiveWork(tabID, workID, requestID string) (*work.WorkRecord, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.ArchiveWork(a.bootContext(), workID, requestID)
}

// RestoreWork restores an archived Work to active.
func (a *App) RestoreWork(tabID, workID, requestID string) (*work.WorkView, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RestoreWork(a.bootContext(), workID, requestID)
}

// ResumeRun resumes a paused or waiting WorkflowRun.
func (a *App) ResumeRun(tabID string, input work.ResumeRunInput) (*work.WorkflowRun, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.ResumeRun(a.bootContext(), input)
}

// DeleteWork moves a Work to trash.
func (a *App) DeleteWork(tabID, workID, requestID string) error {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return err
	}
	return wc.DeleteWork(a.bootContext(), workID, requestID)
}

// ── Cornerstone bindings ─────────────────────────────────────────────────────

// PinCornerstone pins a typed long-term Cornerstone to a Work.
// input.RequestID enables idempotent retries; input.ExpectedRevision provides
// optimistic concurrency control.
func (a *App) PinCornerstone(tabID, workID string, input work.PinCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.PinCornerstone(a.bootContext(), workID, input)
}

// RefreshCornerstone re-resolves a live_ref Cornerstone's source status or
// verifies snapshot blob integrity.
func (a *App) RefreshCornerstone(tabID, workID string, input work.RefreshCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RefreshCornerstone(a.bootContext(), workID, input)
}

// RemoveCornerstone tombstone-removes a Cornerstone. It can be restored via UndoCornerstone.
func (a *App) RemoveCornerstone(tabID, workID string, input work.RemoveCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RemoveCornerstone(a.bootContext(), workID, input)
}

// UndoCornerstone restores a tombstoned Cornerstone. For snapshot cornerstones,
// blob integrity is verified during restore.
func (a *App) UndoCornerstone(tabID, workID string, input work.UndoCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.UndoCornerstone(a.bootContext(), workID, input)
}

// AcceptCornerstone accepts the newly-resolved content of a stale live_ref
// Cornerstone, transitioning it back to active. Only an exact candidate digest
// match is accepted to prevent TOCTOU.
func (a *App) AcceptCornerstone(tabID, workID string, input work.AcceptCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.AcceptCornerstone(a.bootContext(), workID, input)
}

// FreezeCornerstone freezes a live_ref Cornerstone into a snapshot so its
// content no longer follows upstream changes. UseLastKnown mode can freeze the
// last accepted content when the source is unreachable.
func (a *App) FreezeCornerstone(tabID, workID string, input work.FreezeCornerstoneInput) (*work.CornerstoneResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.FreezeCornerstone(a.bootContext(), workID, input)
}

// RepairCornerstone repairs a Cornerstone in missing, denied, invalid, or stale
// status. For live_ref cornerstones, this re-resolves the source. For snapshot
// cornerstones, this attempts blob recovery.
func (a *App) RepairCornerstone(tabID, workID string, input work.RepairCornerstoneInput) (*work.RepairResult, error) {
	wc, err := a.resolveWorkController(tabID)
	if err != nil {
		return nil, err
	}
	return wc.RepairCornerstone(a.bootContext(), workID, input)
}
