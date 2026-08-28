package assistant

import (
	"sync"
	"time"
)

// ViewportSnapshot is a short-lived UI observation of what the user is looking
// at. It is deliberately not business state: the backend only uses the IDs to
// read the authoritative Session subsystem, and an expired snapshot is treated
// as unknown rather than a stale guess about "current" work.
type ViewportSnapshot struct {
	WindowID          string    `json:"window_id"`
	WorkspaceID       string    `json:"workspace_id,omitempty"`
	VisibleSessionIDs []string  `json:"visible_session_ids,omitempty"`
	SelectedSessionID string    `json:"selected_session_id,omitempty"`
	ObservedAt        time.Time `json:"observed_at" ts_type:"string"`
	UIRevision        int64     `json:"ui_revision,omitempty"`
}

// viewportTTL bounds how long a snapshot stays usable. Scrolling refreshes the
// snapshot without starting a model turn; the next user input or run event then
// reads the still-valid snapshot to resolve "these tasks I'm looking at".
const viewportTTL = 30 * time.Second

// Viewport holds the latest viewport snapshots keyed by window and resolves the
// most recently focused still-valid one. It is process-local, bounded, and
// never persists: UI observation is not durable business state.
type Viewport struct {
	mu      sync.Mutex
	windows map[string]ViewportSnapshot
	focused string
}

func NewViewport() *Viewport {
	return &Viewport{windows: map[string]ViewportSnapshot{}}
}

// Publish records a snapshot for a window and marks it focused. A replay is
// rejected when it is late on BOTH the monotonic ui_revision and the observed
// time (an older revision, or the same revision observed earlier, never
// regresses the view). A genuinely newer observation — higher revision, or the
// same revision with a later observed_at — overwrites, so a real re-observation
// always wins.
func (v *Viewport) Publish(s ViewportSnapshot) {
	if s.WindowID == "" || s.ObservedAt.IsZero() {
		return
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.windows == nil {
		v.windows = map[string]ViewportSnapshot{}
	}
	if prev, ok := v.windows[s.WindowID]; ok {
		if prev.UIRevision > s.UIRevision {
			return
		}
		if prev.UIRevision == s.UIRevision && prev.ObservedAt.After(s.ObservedAt) {
			return
		}
	}
	v.windows[s.WindowID] = s
	v.focused = s.WindowID
}

// Current returns the most recently focused snapshot that is still valid at
// now. When nothing is valid it returns ok=false — the Assistant must then
// treat the viewport as unknown rather than invent a selection.
func (v *Viewport) Current(now time.Time) (ViewportSnapshot, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	if s, ok := v.windows[v.focused]; ok && viewportFresh(s, now) {
		return s, true
	}
	var best ViewportSnapshot
	found := false
	for _, s := range v.windows {
		if !viewportFresh(s, now) {
			continue
		}
		if !found || s.ObservedAt.After(best.ObservedAt) {
			best, found = s, true
		}
	}
	return best, found
}

// CurrentForWindow returns one window's snapshot if it is still valid.
func (v *Viewport) CurrentForWindow(windowID string, now time.Time) (ViewportSnapshot, bool) {
	v.mu.Lock()
	defer v.mu.Unlock()
	s, ok := v.windows[windowID]
	if !ok || !viewportFresh(s, now) {
		return ViewportSnapshot{}, false
	}
	return s, true
}

func viewportFresh(s ViewportSnapshot, now time.Time) bool {
	return !now.Before(s.ObservedAt) && now.Sub(s.ObservedAt) <= viewportTTL
}
