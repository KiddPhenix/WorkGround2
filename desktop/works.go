package main

import (
	"fmt"

	"workground2/internal/control"
	"workground2/internal/work"
)

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
