package assistant

import (
	"time"

	"workground2/internal/event"
)

// SessionControl is the host capability the supervisor acting phase needs. It
// deliberately mirrors the sessiontool.SessionControl contract (same method
// set and shapes) so the assistant package stays free of an import cycle:
// sessiontool imports agent, and agent imports assistant. Desktop and daemon
// wrap their sessiontool.SessionControl in a thin adapter; the two interfaces
// are kept in sync by compile-time witnesses on both sides.
type SessionControl interface {
	Steer(sessionID, text, requestID string) error
	AnswerQuestion(sessionID, questionID string, answers []event.AskAnswer, requestID string) error
	Cancel(sessionID, requestID string) error
	Resume(sessionID, requestID string) error
	Retry(sessionID, requestID string) error
	Fork(sessionID, requestID string) (newID string, err error)
	Create(req SessionCreateRequest) (newID string, err error)
	PendingInteractions(sessionID string) ([]SessionInteraction, error)
}

// SessionCreateRequest is the intent for a new managed Session, mirroring
// sessiontool.SessionCreateRequest.
type SessionCreateRequest struct {
	Title     string
	Prompt    string
	OwnerID   string
	ParentID  string
	Purpose   string // "managed" | "supervisor" | ...
	Workspace string
	RequestID string
	// ResponsibilityID optionally binds the Session to one plan responsibility
	// (persisted in the Session meta by the host).
	ResponsibilityID string
}

// SessionInteraction is a bounded view of one pending ask/approval on a
// Session, mirroring sessiontool.SessionInteraction.
type SessionInteraction struct {
	Kind      string // "ask" | "approval"
	ID        string
	Questions []event.AskQuestion
	DueAt     time.Time
}
