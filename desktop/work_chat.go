package main

import (
	"fmt"
	"strings"

	"workground2/internal/agent"
	"workground2/internal/control"
)

// SendWorkChat sends a user message in a Work Session with the authoritative
// Work context injected into the model input. The UI transcript and history
// preserve only the user's original text.
//
// The tab must be a writable Work Session bound to the given workID. The
// Work context is built from a fresh GetWork snapshot and is bounded to
// prevent oversized requests. Repeated calls are safe and read-only.
func (a *App) SendWorkChat(tabID, workID, display, text string) error {
	tabID = strings.TrimSpace(tabID)
	workID = strings.TrimSpace(workID)
	display = strings.TrimSpace(display)
	text = strings.TrimSpace(text)
	if tabID == "" {
		return fmt.Errorf("work: SendWorkChat: tabID is required")
	}
	if workID == "" {
		return fmt.Errorf("work: SendWorkChat: workID is required")
	}
	if display == "" || text == "" {
		return nil // empty message is a no-op
	}

	tab, ctrl := a.tabAndCtrlByID(tabID)
	if tab == nil {
		return fmt.Errorf("work: SendWorkChat: tab %q not found", tabID)
	}
	if tab.ReadOnly {
		return readOnlyChannelErr()
	}
	if tab.sessionKind != agent.SessionKindWork {
		return fmt.Errorf("work: SendWorkChat: tab %q is not a Work Session", tabID)
	}
	if tab.workID != workID {
		return fmt.Errorf("work: SendWorkChat: tab %q is bound to Work %q, not %q", tabID, tab.workID, workID)
	}
	if err := a.applyPendingModelForTab(tab); err != nil {
		return err
	}
	tab, ctrl = a.tabAndCtrlByID(tabID)
	if ctrl == nil {
		return workspaceNotReadyErr(tab)
	}
	if err := a.ensureTabControllerWorkspace(tab); err != nil {
		return err
	}
	ctrl = tab.Ctrl
	if ctrl == nil {
		return workspaceNotReadyErr(tab)
	}
	if err := a.takeoverFromCLI(tab); err != nil {
		return err
	}
	owner, ok := ctrl.(workController)
	if !ok {
		return fmt.Errorf("work: SendWorkChat: feature not available on tab %q", tabID)
	}

	view, err := owner.WorkControl().GetWork(a.bootContext(), workID)
	if err != nil {
		return fmt.Errorf("work: SendWorkChat: GetWork %q: %w", workID, err)
	}
	if view == nil || view.Work == nil {
		return fmt.Errorf("work: SendWorkChat: Work %q not found", workID)
	}

	ctxBlock := control.BuildWorkChatContext(view)
	a.ensureTabTopicIndexedForUserTurn(tab)
	ctrl.SubmitWorkChat(display, text, ctxBlock)
	a.emitProjectTreeChanged()
	return nil
}
