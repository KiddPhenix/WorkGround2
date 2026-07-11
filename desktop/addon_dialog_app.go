package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// ── addon dialog manager ─────────────────────────────────────────────────

type addonDialogRequest struct {
	PluginName string
	PanelID    string
	Message    string
	ResultCh   chan addonDialogResult
	CreatedAt  time.Time
}

type addonDialogResult struct {
	Submitted bool           `json:"submitted"`
	Form      map[string]any `json:"form,omitempty"`
	ActionID  string         `json:"actionId,omitempty"`
}

type addonDialogManager struct {
	mu      sync.Mutex
	pending map[string]*addonDialogRequest // key: "pluginName:panelID"
	events  *addonDialogEvents
}

var dialogMgr = &addonDialogManager{
	pending: make(map[string]*addonDialogRequest),
	events:  &addonDialogEvents{},
}

// show blocks until the frontend dismisses the dialog or timeout expires.
func (m *addonDialogManager) show(pluginName, panelID, message string) addonDialogResult {
	key := pluginName + ":" + panelID
	req := &addonDialogRequest{
		PluginName: pluginName,
		PanelID:    panelID,
		Message:    message,
		ResultCh:   make(chan addonDialogResult, 1),
		CreatedAt:  time.Now(),
	}
	m.mu.Lock()
	m.pending[key] = req
	m.mu.Unlock()

	// Signal frontend
	m.events.notify()

	select {
	case result := <-req.ResultCh:
		return result
	case <-time.After(5 * time.Minute):
		m.mu.Lock()
		delete(m.pending, key)
		m.mu.Unlock()
		return addonDialogResult{Submitted: false}
	}
}

// dismiss closes the pending dialog for the given key.
func (m *addonDialogManager) dismiss(pluginName, panelID string, result addonDialogResult) error {
	key := pluginName + ":" + panelID
	m.mu.Lock()
	req, ok := m.pending[key]
	delete(m.pending, key)
	m.mu.Unlock()
	if !ok {
		return fmt.Errorf("no pending dialog for %s", key)
	}
	select {
	case req.ResultCh <- result:
	default:
	}
	return nil
}

// ── addon dialog events (polled by frontend) ────────────────────────────

type addonDialogEvents struct {
	mu   sync.Mutex
	tick chan struct{}
}

func (e *addonDialogEvents) notify() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tick == nil {
		e.tick = make(chan struct{}, 1)
	}
	select {
	case e.tick <- struct{}{}:
	default:
	}
}

func (e *addonDialogEvents) getCh() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tick == nil {
		e.tick = make(chan struct{}, 1)
	}
	return e.tick
}

// ── Wails Host API ──────────────────────────────────────────────────────

// GetPendingAddOnDialog returns the oldest pending dialog request, or null.
func (a *App) GetPendingAddOnDialog() (map[string]any, error) {
	dialogMgr.mu.Lock()
	defer dialogMgr.mu.Unlock()
	// Return the oldest pending request
	var oldest *addonDialogRequest
	for _, req := range dialogMgr.pending {
		if oldest == nil || req.CreatedAt.Before(oldest.CreatedAt) {
			oldest = req
		}
	}
	if oldest == nil {
		// Return null explicitly so frontend knows there is no dialog.
		return nil, nil
	}
	return map[string]any{
		"pluginName": oldest.PluginName,
		"panelID":    oldest.PanelID,
		"message":    oldest.Message,
	}, nil
}

// DismissAddOnDialog is called by the frontend after the user
// submits the dialog form or cancels.
func (a *App) DismissAddOnDialog(pluginName, panelID string, submitted bool, form map[string]any, actionID string) error {
	return dialogMgr.dismiss(pluginName, panelID, addonDialogResult{
		Submitted: submitted,
		Form:      form,
		ActionID:  actionID,
	})
}

// WaitAddOnDialogChange blocks until a dialog is posted or the frontend
// should re-check. Used by the frontend long-poll loop.
func (a *App) WaitAddOnDialogChange() error {
	select {
	case <-dialogMgr.events.getCh():
		return nil
	case <-time.After(5 * time.Second):
		return nil
	}
}

// AddOnDialogQuery queries panel data for a dialog that is about to be shown.
func (a *App) AddOnDialogQuery(pluginName, panelID string) (AddOnPanelQueryResult, error) {
	adapter := pluginName + "/credentials.json"
	return a.AddOnPanelQuery(pluginName, panelID, adapter)
}

// AddOnDialogAction forwards a dialog form action to the MCP runtime.
func (a *App) AddOnDialogAction(pluginName, panelID string, action AddOnPanelActionInput) (AddOnPanelActionResult, error) {
	adapter := pluginName + "/credentials.json"
	return a.AddOnPanelAction(pluginName, panelID, adapter, action)
}

// TriggerAddOnDialog is called from MCP panel/dialog handling.
// It stores the request, signals the frontend, and blocks until dismissed.
func (a *App) TriggerAddOnDialog(mcpServer, pluginName, panelID, message string) (addonDialogResult, error) {
	// Pre-load panel data to warm up the cache
	a.mcpPanelQuery(mcpServer, pluginName+"/credentials.json")

	result := dialogMgr.show(pluginName, panelID, message)

	// If user submitted a form and asked to test/save, execute the action
	if result.Submitted && result.Form != nil && result.ActionID != "" {
		actionInput := AddOnPanelActionInput{
			ActionID: result.ActionID,
			Form:     result.Form,
		}
		if result.ActionID == "test" || result.ActionID == "save" {
			_, _ = a.mcpPanelAction(mcpServer, pluginName+"/credentials.json", actionInput)
		}
	}
	return result, nil
}

// Ensure json is used.
var _ = json.Marshal
var _ = context.Background
