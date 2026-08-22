package browser

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// generateSessionID creates a short unique session id.
func generateSessionID(ownerID string) string {
	h := sha256.Sum256([]byte(ownerID + time.Now().UTC().Format(time.RFC3339Nano)))
	return fmt.Sprintf("bs_%x", h[:6])
}

// Session manages one browser instance for a single owner.
type Session struct {
	id       string
	ownerID  string
	driver   Driver
	lease    ProfileLease
	hasLease bool
	opts     Options

	opMu               sync.Mutex
	mu                 sync.RWMutex
	state              SessionState
	revision           uint64
	generation         uint64
	snapshot           *Snapshot
	requests           *RequestCache
	lastUsed           time.Time
	lastInvalidation   time.Time
	invalidationSignal chan struct{}
	watchCancel        context.CancelFunc
}

// newSession creates a Session in starting state. The driver is set later during
// ensureDriver.
func newSession(ownerID string, opts Options) *Session {
	s := &Session{
		id:                 generateSessionID(ownerID),
		ownerID:            ownerID,
		opts:               opts,
		state:              SessionStarting,
		revision:           0,
		requests:           NewRequestCache(),
		lastUsed:           time.Now(),
		invalidationSignal: make(chan struct{}, 1),
	}
	return s
}

// State returns the current session state.
func (s *Session) State() SessionState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.state
}

// Snapshot returns a copy of the current snapshot, or nil.
func (s *Session) Snapshot() *Snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.snapshot == nil {
		return nil
	}
	// Return a shallow copy of the state with a new elements slice.
	snap := *s.snapshot
	snap.State.Elements = make([]Element, len(s.snapshot.State.Elements))
	copy(snap.State.Elements, s.snapshot.State.Elements)
	for i := range snap.State.Elements {
		if checked := snap.State.Elements[i].Checked; checked != nil {
			value := *checked
			snap.State.Elements[i].Checked = &value
		}
	}
	snap.State.Tabs = make([]TabInfo, len(s.snapshot.State.Tabs))
	copy(snap.State.Tabs, s.snapshot.State.Tabs)
	snap.State.Warnings = make([]StateWarning, len(s.snapshot.State.Warnings))
	copy(snap.State.Warnings, s.snapshot.State.Warnings)
	snap.Nodes = make(map[int]NodeRef, len(s.snapshot.Nodes))
	for k, v := range s.snapshot.Nodes {
		snap.Nodes[k] = v
	}
	return &snap
}

// Revision returns the current revision.
func (s *Session) Revision() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.revision
}

// Generation returns the current generation counter.
func (s *Session) Generation() uint64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.generation
}

// bumpGeneration increments the generation and invalidates the snapshot.
func (s *Session) bumpGeneration() {
	s.mu.Lock()
	s.generation++
	s.snapshot = nil
	s.lastInvalidation = time.Now()
	s.mu.Unlock()
	select {
	case s.invalidationSignal <- struct{}{}:
	default:
	}
}

// publishSnapshot installs a new snapshot from an observation, bumping revision
// if the fingerprint changed or the snapshot was invalidated.
func (s *Session) publishSnapshot(sessionID string, obs Observation, genBefore uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishSnapshotLocked(sessionID, obs, genBefore, false)
}

// publishSnapshotAfterWrite is like publishSnapshot but always bumps revision
// after a write action, even if the fingerprint is unchanged.
func (s *Session) publishSnapshotAfterWrite(sessionID string, obs Observation, genBefore uint64) (uint64, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.publishSnapshotLocked(sessionID, obs, genBefore, true)
}

func (s *Session) publishSnapshotLocked(sessionID string, obs Observation, genBefore uint64, forceBump bool) (uint64, bool) {
	if s.generation != genBefore {
		return s.revision, false
	}

	now := time.Now().UTC()

	elements := make([]Element, len(obs.Nodes))
	nodes := make(map[int]NodeRef, len(obs.Nodes))
	for i, nd := range obs.Nodes {
		idx := i + 1
		var checked *bool
		if nd.Checked != nil {
			value := *nd.Checked
			checked = &value
		}
		elements[i] = Element{
			Index:       idx,
			Role:        nd.Role,
			Tag:         nd.Tag,
			InputType:   nd.InputType,
			Name:        nd.Name,
			Placeholder: nd.Placeholder,
			Href:        nd.Href,
			Disabled:    nd.Disabled,
			Checked:     checked,
			Editable:    nd.Editable,
			Bounds:      nd.Ref.Bounds,
		}
		nodes[idx] = nd.Ref
	}

	tabs := append([]TabInfo(nil), obs.Tabs...)
	warnings := append([]StateWarning(nil), obs.Warnings...)
	state := PageState{
		SessionID:  sessionID,
		URL:        obs.URL,
		Title:      obs.Title,
		ActiveTab:  obs.ActiveTab,
		Tabs:       tabs,
		Text:       obs.Text,
		Elements:   elements,
		Warnings:   warnings,
		Truncated:  obs.Truncated,
		CapturedAt: now,
	}

	// Always bump after a write action, or when state has changed.
	fingerprintChanged := s.snapshot == nil || s.snapshot.Fingerprint != obs.Fingerprint

	if forceBump || s.revision == 0 || fingerprintChanged {
		s.revision++
	}
	state.Revision = s.revision

	snp := &Snapshot{
		State:       state,
		Nodes:       nodes,
		Fingerprint: obs.Fingerprint,
		Generation:  s.generation,
	}
	s.snapshot = snp
	s.state = SessionReady
	return s.revision, true
}

func (s *Session) waitForQuiet(ctx context.Context, window time.Duration) error {
	if window <= 0 {
		return nil
	}
	for {
		s.mu.RLock()
		last := s.lastInvalidation
		s.mu.RUnlock()
		if last.IsZero() {
			return nil
		}
		remaining := window - time.Since(last)
		if remaining <= 0 {
			return nil
		}
		timer := time.NewTimer(remaining)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-s.invalidationSignal:
			if !timer.Stop() {
				<-timer.C
			}
			// A newer invalidation resets the quiet window.
		case <-timer.C:
		}
	}
}

// validateRevision checks that rev matches the current snapshot revision and
// the snapshot is not invalidated.
func (s *Session) validateRevision(rev uint64) (snap *Snapshot, err error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.state != SessionReady {
		return nil, NewError(ErrBrowserNotOpen, "browser session is not ready", nil)
	}
	if s.snapshot == nil {
		return nil, NewError(ErrStaleState, "page state was invalidated; call browser_state", nil)
	}
	if s.snapshot.Generation != s.generation {
		return nil, NewError(ErrStaleState, "snapshot was invalidated by page changes", nil)
	}
	if s.snapshot.State.Revision != rev {
		return nil, NewError(ErrStaleState, fmt.Sprintf("revision %d does not match current %d", rev, s.snapshot.State.Revision), nil)
	}
	// Return a defensive copy.
	copied := *s.snapshot
	copied.Nodes = make(map[int]NodeRef, len(s.snapshot.Nodes))
	for k, v := range s.snapshot.Nodes {
		copied.Nodes[k] = v
	}
	return &copied, nil
}

// resolveNode returns the NodeRef for an element index in the current snapshot.
func (s *Session) resolveNode(index int) (NodeRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.snapshot == nil {
		return NodeRef{}, NewError(ErrBrowserNotOpen, "no snapshot available", nil)
	}
	ref, ok := s.snapshot.Nodes[index]
	if !ok {
		return NodeRef{}, NewError(ErrElementNotFound, fmt.Sprintf("element index %d not found", index), nil)
	}
	return ref, nil
}

// setState transitions the session state.
func (s *Session) setState(target SessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state = target
}

// markBroken transitions the session to broken state.
func (s *Session) markBroken() {
	s.setState(SessionBroken)
}

// touch updates the last-used timestamp.
func (s *Session) touch() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastUsed = time.Now()
}

// idleDuration returns how long the session has been idle.
func (s *Session) idleDuration() time.Duration {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Since(s.lastUsed)
}

// listenInvalidations runs a goroutine that watches the driver's invalidation
// channel and bumps the session generation.
func (s *Session) listenInvalidations(ctx context.Context) {
	if s.driver == nil {
		return
	}
	ch := s.driver.Invalidations()
	if ch == nil {
		return
	}
	s.mu.Lock()
	if s.watchCancel != nil {
		s.watchCancel()
	}
	watchCtx, cancel := context.WithCancel(ctx)
	s.watchCancel = cancel
	s.mu.Unlock()
	go func() {
		for {
			select {
			case <-watchCtx.Done():
				return
			case inv, ok := <-ch:
				if !ok {
					// Channel closed: driver disconnected.
					state := s.State()
					if state == SessionClosing || state == SessionClosed {
						return
					}
					slog.Warn("browser invalidation channel closed", "session", s.id)
					s.markBroken()
					return
				}
				s.bumpGeneration()
				_ = inv
			}
		}
	}()
}
