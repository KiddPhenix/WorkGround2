package main

import (
	"errors"
	"strings"
	"time"

	"workground2/internal/agent"
	"workground2/internal/assistant"
	"workground2/internal/config"
	"workground2/internal/control"
)

// AssistantActiveWork is one still-active object observed during a pause
// quiesce or a resume scan. Kind distinguishes a live session controller from
// a supervisor session; ID is the stable BranchID.
type AssistantActiveWork struct {
	Kind  string `json:"kind"`
	ID    string `json:"id"`
	State string `json:"state"`
}

// AssistantWorkControlView is the authoritative work-control status returned to
// the UI and other hosts: state/epoch/revision from the single persistent gate
// plus host-observed active work and a next-step hint. Error carries an
// explicit, retryable failure (e.g. a quiesce timeout that left the gate
// QUIESCING instead of pretending PAUSED).
type AssistantWorkControlView struct {
	State    assistant.WorkControlState `json:"state"`
	Epoch    int64                      `json:"epoch"`
	Revision int64                      `json:"revision"`
	Active   []AssistantActiveWork      `json:"active"`
	NextHint string                     `json:"next_hint"`
	Error    string                     `json:"error,omitempty"`
}

func workControlView(wc assistant.WorkControl, active []AssistantActiveWork, nextHint string) AssistantWorkControlView {
	return AssistantWorkControlView{
		State: wc.State, Epoch: wc.Epoch, Revision: wc.Revision,
		Active: active, NextHint: nextHint,
	}
}

// activeWorkForSession resolves one durable session to its live controller and
// reports it as active work when it is still running.
func (a *App) activeWorkForSession(path string, kind string) (AssistantActiveWork, bool) {
	if a == nil || strings.TrimSpace(path) == "" {
		return AssistantActiveWork{}, false
	}
	_, ctrl := a.sessionCtrlByID(agent.BranchID(path))
	if ctrl == nil {
		return AssistantActiveWork{}, false
	}
	state := "idle"
	if ctrl.Running() {
		state = "running"
	} else if _, pending := ctrl.PendingInteraction(); pending {
		state = "waiting"
	}
	return AssistantActiveWork{Kind: kind, ID: agent.BranchID(path), State: state}, ctrl.Running()
}

// activeHostWork scans every registered live session (ordinary tabs, managed
// Assistant sessions, and supervisor sessions) for controllers that are still
// running. It is the host-observed half of pause quiesce and resume recovery.
func (a *App) activeHostWork() []AssistantActiveWork {
	if a == nil {
		return nil
	}
	var active []AssistantActiveWork
	seen := map[string]bool{}
	for _, tab := range a.sessions.all() {
		if tab == nil {
			continue
		}
		path := strings.TrimSpace(tab.currentSessionPath())
		if path == "" {
			continue
		}
		id := agent.BranchID(path)
		if seen[id] {
			continue
		}
		seen[id] = true
		if work, running := a.activeWorkForSession(path, "session"); running {
			active = append(active, work)
		}
	}
	// Supervisor sessions live outside the tab registry; resolve them through
	// the assistant store's assistant list so they are never missed.
	if a.assistant != nil && a.assistant.store != nil {
		if assistants, err := a.assistant.store.List(); err == nil {
			for _, as := range assistants {
				sessions, err := agent.ListSessionsByOwner(config.SessionDir(), as.ID)
				if err != nil {
					continue
				}
				for _, s := range sessions {
					if s.Purpose != agent.PurposeSupervisor && s.Purpose != agent.PurposeManaged {
						continue
					}
					id := agent.BranchID(s.Path)
					if seen[id] {
						continue
					}
					seen[id] = true
					if work, running := a.activeWorkForSession(s.Path, string(s.Purpose)); running {
						active = append(active, work)
					}
				}
			}
		}
	}
	return active
}

// quiesceHostWork cancels every running live session so it checkpoints at a
// safe point. Cancel is a request: the controller stops at its next safe
// boundary and keeps its in-flight marker (the recovery intent).
func (a *App) quiesceHostWork() {
	if a == nil {
		return
	}
	for _, tab := range a.sessions.all() {
		if tab == nil {
			continue
		}
		_, ctrl := a.tabAndCtrlByID(tab.ID)
		if ctrl != nil && ctrl.Running() {
			ctrl.Cancel()
		}
	}
}

// hostWorkQuiet reports whether every live session controller has stopped,
// polling until the bound elapses.
func (a *App) hostWorkQuiet(bound time.Duration) bool {
	deadline := time.Now().Add(bound)
	for {
		if len(a.activeHostWork()) == 0 {
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// resumeHostWork scans interrupted sessions (an unfinished round left by a
// pause or crash) and re-drives them from their checkpoint. It only resumes
// what is durably recoverable — sessions whose transcript ends on a user turn
// with no reply — and never re-submits the last user turn. Pending interactions
// (asks/approvals) are left waiting: the user must answer them.
func (a *App) resumeHostWork() []AssistantActiveWork {
	if a == nil {
		return nil
	}
	adapter := &appAssistantSessionControl{app: a}
	var recovered []AssistantActiveWork
	for _, tab := range a.sessions.all() {
		if tab == nil {
			continue
		}
		path := strings.TrimSpace(tab.currentSessionPath())
		if path == "" || !hasUnfinishedRound(path) {
			continue
		}
		_, ctrl := a.tabAndCtrlByID(tab.ID)
		if ctrl == nil || ctrl.Running() {
			continue
		}
		if _, pending := ctrl.PendingInteraction(); pending {
			continue // waiting on the user; resume leaves it waiting
		}
		if err := adapter.resumeFromCheckpoint(ctrl); err != nil {
			continue
		}
		recovered = append(recovered, AssistantActiveWork{Kind: "session", ID: agent.BranchID(path), State: "recovering"})
	}
	return recovered
}

// errWorkControlUnavailable is returned when the assistant runtime (and its
// store) is not started, so hosts can surface an explicit error instead of
// silently reporting a fake gate.
var errWorkControlUnavailable = errors.New("assistant runtime is not started")

// requireStore returns the runtime's store or a typed error.
func (r *AssistantRuntime) requireStore() (*assistant.Store, error) {
	if r == nil || r.store == nil {
		return nil, errWorkControlUnavailable
	}
	return r.store, nil
}

var _ = control.SessionAPI(nil) // keep control import used by future extension
