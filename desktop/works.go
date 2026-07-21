package main

import (
	"context"
	"fmt"
	"strings"

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

	a.workWatchMu.Lock()
	if existing := a.workWatches[subscriptionID]; existing != nil {
		a.workWatchMu.Unlock()
		if existing.tabID == tabID && existing.workID == workID {
			return nil
		}
		return fmt.Errorf("work: subscription ID is already in use")
	}
	broadcaster := owner.WorkViews()
	streamID, events := broadcaster.Subscribe(32)
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
			case event, open := <-events:
				if !open {
					return
				}
				if event.WorkID == workID {
					a.runtimeEvents.Emit(a.bootContext(), workViewEventPrefix+subscriptionID, event)
				}
			}
		}
	}()
	return nil
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
