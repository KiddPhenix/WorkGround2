package main

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"workground2/internal/bot"
	"workground2/internal/unread"
)

const unreadStateChannel = "unread:state"

type UnreadState struct {
	Available bool           `json:"available"`
	Error     string         `json:"error,omitempty"`
	Summary   unread.Summary `json:"summary"`
}

type MarkUnreadReadInput struct {
	ConversationKey string `json:"conversationKey"`
	UpToSequence    uint64 `json:"upToSequence"`
}

// UnreadState returns the durable data-layer projection. The frontend does not
// consume it yet; keeping this API stable lets the later presentation layer stay
// read-only apart from the explicit cursor command below.
func (a *App) UnreadState() UnreadState {
	store, _ := a.currentUnreadStore()
	a.unreadMu.RLock()
	err := a.unreadErr
	a.unreadMu.RUnlock()
	state := UnreadState{Available: store != nil}
	if store != nil {
		state.Summary = store.Summary()
	}
	if err != nil {
		state.Error = err.Error()
	}
	return state
}

// MarkUnreadRead monotonically advances an actual visible watermark supplied
// by a future Room/IM presentation layer.
func (a *App) MarkUnreadRead(input MarkUnreadReadInput) (UnreadState, error) {
	store, err := a.currentUnreadStore()
	if err != nil {
		return a.UnreadState(), err
	}
	if store == nil {
		return a.UnreadState(), errors.New("unread store is unavailable")
	}
	before := store.Summary().Revision
	if _, err := store.MarkRead(input.ConversationKey, input.UpToSequence); err != nil {
		a.recordUnreadError(err)
		return a.UnreadState(), err
	}
	a.recordUnreadError(nil)
	state := a.UnreadState()
	if state.Summary.Revision != before {
		a.emitUnreadState(state)
	}
	return state, nil
}

func (a *App) currentUnreadStore() (*unread.Store, error) {
	if a == nil {
		return nil, errors.New("desktop application is unavailable")
	}
	a.unreadMu.RLock()
	defer a.unreadMu.RUnlock()
	if a.unreadStore == nil {
		if a.unreadErr != nil {
			return nil, a.unreadErr
		}
		return nil, errors.New("unread store is unavailable")
	}
	return a.unreadStore, nil
}

func (a *App) recordUnreadError(err error) {
	if a == nil {
		return
	}
	a.unreadMu.Lock()
	a.unreadErr = err
	a.unreadMu.Unlock()
}

func (a *App) acceptIMUnread(msg bot.InboundMessage) (bot.InboundAcceptance, error) {
	store, err := a.currentUnreadStore()
	if err != nil {
		return bot.InboundAcceptance{}, err
	}
	if store == nil {
		return bot.InboundAcceptance{}, errors.New("unread store is unavailable")
	}
	receipt, err := store.AcceptIM(unread.IMInput{
		ConversationKey: bot.BuildSessionKey(msg.Session()),
		MessageID:       msg.MessageID,
		Title:           imUnreadTitle(msg),
		AuthorID:        firstNonEmpty(strings.TrimSpace(msg.OperatorID), strings.TrimSpace(msg.UserID)),
		ReceivedAt:      msg.ReceivedAt,
	})
	if err != nil {
		a.recordUnreadError(err)
		return bot.InboundAcceptance{}, err
	}
	a.recordUnreadError(nil)
	if !receipt.Duplicate {
		a.emitUnreadState(a.UnreadState())
	}
	return bot.InboundAcceptance{Duplicate: receipt.Duplicate}, nil
}

func imUnreadTitle(msg bot.InboundMessage) string {
	if msg.ChatType == bot.ChatDM || msg.ChatType == bot.ChatDirect {
		return firstNonEmpty(strings.TrimSpace(msg.UserName), strings.TrimSpace(msg.ChatID))
	}
	return firstNonEmpty(strings.TrimSpace(msg.ChatID), strings.TrimSpace(msg.UserName))
}

func (a *App) bindIMUnread(msg bot.InboundMessage, sessionID string) error {
	store, err := a.currentUnreadStore()
	if err != nil {
		return err
	}
	if store == nil {
		return errors.New("unread store is unavailable")
	}
	key := "im:" + bot.BuildSessionKey(msg.Session())
	before := store.Summary().Revision
	if err := store.BindSession(key, sessionID); err != nil {
		a.recordUnreadError(err)
		return err
	}
	a.recordUnreadError(nil)
	state := a.UnreadState()
	if state.Summary.Revision != before {
		a.emitUnreadState(state)
	}
	return nil
}

func (c *desktopCollaboration) observeUnread() {
	if c == nil || c.app == nil {
		return
	}
	store, err := c.app.currentUnreadStore()
	if err != nil || store == nil {
		return
	}
	c.mu.RLock()
	room := c.state.Room
	if room == "" {
		room = c.state.Snapshot.Room.ID
	}
	snapshot := cloneCollaborationState(CollaborationState{Snapshot: c.state.Snapshot}).Snapshot
	memberID, agentID := c.state.MemberID, c.state.AgentID
	sessionID := firstNonEmpty(strings.TrimSpace(c.ownerSessionID), strings.TrimSpace(c.state.SessionID))
	persistenceKey := collaborationPersistenceKey(c.ownerSessionID, c.ownerSessionPath)
	c.mu.RUnlock()
	if strings.TrimSpace(room) == "" || strings.TrimSpace(memberID) == "" {
		return
	}
	before := store.Summary().Revision
	created := snapshot.Room.CreatedAt.UTC().Format(time.RFC3339Nano)
	identity := strings.Join([]string{persistenceKey, room, created}, "\x00")
	_, err = store.ObserveRoom(unread.RoomInput{
		ConversationKey: stableCollaborationID("room_unread", identity),
		SessionID:       sessionID,
		Title:           firstNonEmpty(strings.TrimSpace(snapshot.Room.Name), strings.TrimSpace(room)),
		LocalMemberID:   memberID,
		LocalAgentID:    agentID,
		Snapshot:        snapshot,
		ObservedAt:      time.Now().UTC(),
	})
	if err != nil {
		c.app.recordUnreadError(fmt.Errorf("project Room unread: %w", err))
		return
	}
	c.app.recordUnreadError(nil)
	state := c.app.UnreadState()
	if state.Summary.Revision != before {
		c.app.emitUnreadState(state)
	}
}

func (a *App) emitUnreadState(state UnreadState) {
	if a == nil || a.ctx == nil {
		return
	}
	a.runtimeEvents.Emit(a.ctx, unreadStateChannel, state)
}
