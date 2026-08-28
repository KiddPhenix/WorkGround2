package agent

import "strings"

// SessionStatus is the lifecycle of one session as seen by the Assistant. It is
// the vocabulary the Session subsystem uses as the single source of truth for
// execution state, replacing the parallel Run/Job state machines that used to
// duplicate it.
type SessionStatus string

const (
	SessionStatusQueued    SessionStatus = "queued"
	SessionStatusRunning   SessionStatus = "running"
	SessionStatusWaiting   SessionStatus = "waiting"
	SessionStatusCompleted SessionStatus = "completed"
	SessionStatusFailed    SessionStatus = "failed"
	SessionStatusCancelled SessionStatus = "cancelled"
	// SessionStatusIdle is the derived fallback for a session with no durable
	// status and no in-flight turn or pending attention.
	SessionStatusIdle SessionStatus = "idle"
)

func validSessionStatus(s SessionStatus) bool {
	switch s {
	case SessionStatusQueued, SessionStatusRunning, SessionStatusWaiting,
		SessionStatusCompleted, SessionStatusFailed, SessionStatusCancelled,
		SessionStatusIdle:
		return true
	}
	return false
}

// DeriveSessionStatus returns the durable status when the Assistant host
// recorded one, and otherwise derives a safe read-only approximation from
// durable sidecar signals. It never fabricates a terminal status: without an
// explicit record a session is running (in-flight turn), waiting (attention),
// or idle, never completed/failed/cancelled.
func DeriveSessionStatus(meta BranchMeta) SessionStatus {
	if meta.Status != "" && validSessionStatus(meta.Status) {
		return meta.Status
	}
	if meta.InFlightTurn != nil {
		return SessionStatusRunning
	}
	if meta.NeedsAttention {
		return SessionStatusWaiting
	}
	return SessionStatusIdle
}

// ListSessionsByOwner returns the visible sessions owned by assistantID, most
// recently active first. It reuses ListSessions so the Session subsystem stays
// the single source of truth: Assistant-owned sessions are ordinary session
// files whose BranchMeta records AssistantID, not a separate task list.
func ListSessionsByOwner(dir, assistantID string) ([]SessionInfo, error) {
	assistantID = strings.TrimSpace(assistantID)
	if assistantID == "" {
		return nil, nil
	}
	all, err := ListSessions(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, s := range all {
		if s.AssistantID == assistantID {
			out = append(out, s)
		}
	}
	return out, nil
}

// IsSupervisorSession reports whether meta is the durable identity of an
// Assistant's single supervisor session.
func IsSupervisorSession(meta BranchMeta) bool {
	return meta.SessionKind == SessionKindAssistant && meta.Purpose == PurposeSupervisor
}

// FindSupervisorSession returns the most recently active supervisor session
// owned by assistantID, or ok=false. The supervisor session is identified by
// BranchMeta (SessionKind=assistant, Purpose=supervisor, AssistantID), never by
// a separate index, so uniqueness stays a property of the Session subsystem.
func FindSupervisorSession(dir, assistantID string) (SessionInfo, bool) {
	owned, err := ListSessionsByOwner(dir, assistantID)
	if err != nil {
		return SessionInfo{}, false
	}
	for _, s := range owned {
		if s.Purpose == PurposeSupervisor {
			return s, true
		}
	}
	return SessionInfo{}, false
}

// ListSessionsByOwnerByMeta returns the assistant-owned sessions by scanning
// session metadata directly, including transcripts that have no content yet.
// ListSessions skips zero-turn transcripts, but a managed or supervisor Session
// is created empty and only gains content as its turn runs — the supervisor
// loop must observe it from creation, or concurrent ticks would re-create it.
func ListSessionsByOwnerByMeta(dir, assistantID string) ([]SessionInfo, error) {
	assistantID = strings.TrimSpace(assistantID)
	if assistantID == "" {
		return nil, nil
	}
	ordered, err := ListSessionOrder(dir)
	if err != nil {
		return nil, err
	}
	var out []SessionInfo
	for _, s := range ordered {
		if s.AssistantID != assistantID {
			continue
		}
		out = append(out, SessionInfo{
			Path: s.Path, CreatedAt: s.CreatedAt, LastActivityAt: s.LastActivityAt,
			Preview: s.Preview, Turns: s.Turns, CustomTitle: s.CustomTitle,
			SessionSource: s.SessionSource, SessionKind: s.SessionKind,
			AssistantID: s.AssistantID, Purpose: s.Purpose,
		})
	}
	return out, nil
}

// FindSupervisorSessionByMeta returns the assistant's supervisor session by
// scanning session metadata directly. ListSessions skips transcripts with zero
// turns, but a supervisor Session is created empty and only gains content as
// the loop runs — so the listing-based FindSupervisorSession would make a
// freshly-created supervisor Session invisible until its first turn and let
// concurrent ticks create duplicates. This scan is the discovery the supervisor
// loop uses.
func FindSupervisorSessionByMeta(dir, assistantID string) (SessionInfo, bool) {
	owned, err := ListSessionsByOwnerByMeta(dir, assistantID)
	if err != nil {
		return SessionInfo{}, false
	}
	for _, s := range owned {
		if s.Purpose == PurposeSupervisor {
			return s, true
		}
	}
	return SessionInfo{}, false
}
