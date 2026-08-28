package assistant

import (
	"testing"
	"time"
)

func TestViewportMostRecentlyFocusedWins(t *testing.T) {
	v := NewViewport()
	now := testEpoch

	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "s1", ObservedAt: now.Add(time.Second), UIRevision: 1})
	v.Publish(ViewportSnapshot{WindowID: "w2", SelectedSessionID: "s2", ObservedAt: now.Add(2 * time.Second), UIRevision: 1})

	cur, ok := v.Current(now.Add(3 * time.Second))
	if !ok || cur.WindowID != "w2" || cur.SelectedSessionID != "s2" {
		t.Fatalf("Current = %+v ok=%v, want w2/s2", cur, ok)
	}
}

func TestViewportExpiresToUnknown(t *testing.T) {
	v := NewViewport()
	now := testEpoch

	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "s1", ObservedAt: now, UIRevision: 1})

	// Still valid inside the TTL.
	if _, ok := v.Current(now.Add(viewportTTL)); !ok {
		t.Fatal("snapshot should still be valid at the TTL boundary")
	}
	// Expired after the TTL -> unknown.
	if _, ok := v.Current(now.Add(viewportTTL + time.Nanosecond)); ok {
		t.Fatal("snapshot should be unknown after the TTL")
	}
}

func TestViewportIgnoresStaleRevision(t *testing.T) {
	v := NewViewport()
	now := testEpoch

	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "new", ObservedAt: now.Add(time.Second), UIRevision: 5})
	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "old", ObservedAt: now.Add(2 * time.Second), UIRevision: 3})

	cur, ok := v.Current(now.Add(3 * time.Second))
	if !ok || cur.SelectedSessionID != "new" || cur.UIRevision != 5 {
		t.Fatalf("stale publish regressed the view: %+v", cur)
	}
}

// TestViewportIgnoresSameRevisionEarlierObservation proves the late-snapshot
// rejection also guards on observed_at: a publish carrying the same ui_revision
// but an earlier observed_at (an out-of-order replay of the same observation)
// must not replace the newer observation.
func TestViewportIgnoresSameRevisionEarlierObservation(t *testing.T) {
	v := NewViewport()
	now := testEpoch

	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "s1", ObservedAt: now.Add(2 * time.Second), UIRevision: 4})
	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "s1", ObservedAt: now.Add(time.Second), UIRevision: 4})

	cur, ok := v.Current(now.Add(3 * time.Second))
	if !ok || cur.ObservedAt != now.Add(2*time.Second) {
		t.Fatalf("same-revision earlier observation regressed the view: %+v", cur)
	}
}

// TestViewportNewerRevisionWinsEvenWhenObservedEarlier: the monotonic revision
// is the primary ordering; a higher revision represents a newer UI state and
// wins even when the client's clock delivered it with an earlier observed_at.
func TestViewportNewerRevisionWinsEvenWhenObservedEarlier(t *testing.T) {
	v := NewViewport()
	now := testEpoch

	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "old", ObservedAt: now.Add(2 * time.Second), UIRevision: 3})
	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "new", ObservedAt: now.Add(time.Second), UIRevision: 4})

	cur, ok := v.Current(now.Add(3 * time.Second))
	if !ok || cur.SelectedSessionID != "new" || cur.UIRevision != 4 {
		t.Fatalf("newer revision lost to an earlier observation: %+v", cur)
	}
}

func TestViewportCurrentForWindow(t *testing.T) {
	v := NewViewport()
	now := testEpoch

	v.Publish(ViewportSnapshot{WindowID: "w1", SelectedSessionID: "s1", ObservedAt: now, UIRevision: 1})
	if s, ok := v.CurrentForWindow("w1", now.Add(time.Second)); !ok || s.SelectedSessionID != "s1" {
		t.Fatalf("CurrentForWindow = %+v ok=%v", s, ok)
	}
	if _, ok := v.CurrentForWindow("missing", now); ok {
		t.Fatal("missing window should be unknown")
	}
	if _, ok := v.CurrentForWindow("w1", now.Add(viewportTTL+time.Second)); ok {
		t.Fatal("expired window should be unknown")
	}
}
