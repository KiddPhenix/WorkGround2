package main

import (
	"errors"
	"testing"
	"time"
)

// redrawFake captures the timers a widgetRedrawScheduler arms so tests drive
// the schedule synchronously instead of waiting out the real 15-second window.
type redrawFake struct {
	now     time.Time
	pending []func()
	delays  []time.Duration
	redraws int
}

func (f *redrawFake) after(d time.Duration, fn func()) *time.Timer {
	f.delays = append(f.delays, d)
	due := f.now.Add(d)
	f.pending = append(f.pending, func() {
		if f.now.Before(due) {
			f.now = due
		}
		fn()
	})
	return nil // scheduler only calls Stop on a non-nil timer
}

func (f *redrawFake) redraw() error {
	f.redraws++
	return nil
}

func (f *redrawFake) next() func() {
	if len(f.pending) == 0 {
		return nil
	}
	fn := f.pending[0]
	f.pending = f.pending[1:]
	return fn
}

func TestWidgetRedrawTicksAtExpectedCadenceAndStops(t *testing.T) {
	f := &redrawFake{}
	s := &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: f.redraw}

	s.reset()
	for i := 0; i < widgetRedrawTicks; i++ {
		fn := f.next()
		if fn == nil {
			t.Fatalf("tick %d: no pending redraw scheduled", i)
		}
		fn()
	}
	if f.redraws != widgetRedrawTicks {
		t.Fatalf("redraws = %d, want %d", f.redraws, widgetRedrawTicks)
	}
	if len(f.pending) != 0 {
		t.Fatalf("scheduler kept ticking after the window: %d pending", len(f.pending))
	}
	for _, d := range f.delays {
		if d != widgetRedrawInterval {
			t.Fatalf("tick interval = %v, want %v", d, widgetRedrawInterval)
		}
	}
}

func TestWidgetRedrawResetFencesLateTick(t *testing.T) {
	f := &redrawFake{}
	s := &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: f.redraw}

	s.reset()
	stale := f.next()
	s.reset()
	current := f.next()
	if stale == nil || current == nil {
		t.Fatalf("reset did not arm a tick (stale=%v current=%v)", stale != nil, current != nil)
	}

	// A late tick from the superseded switch must not repaint.
	stale()
	if f.redraws != 0 {
		t.Fatalf("late tick redrew after a newer switch: %d", f.redraws)
	}
	// The current switch still repaints.
	current()
	if f.redraws != 1 {
		t.Fatalf("current switch redraws = %d, want 1", f.redraws)
	}
}

func TestWidgetRedrawContinuesAfterFailure(t *testing.T) {
	f := &redrawFake{}
	calls := 0
	s := &widgetRedrawScheduler{
		after: f.after,
		now:   func() time.Time { return f.now },
		redraw: func() error {
			calls++
			if calls == 1 {
				return errors.New("transient repaint failure")
			}
			return nil
		},
	}

	s.reset()
	first := f.next()
	first()
	second := f.next()
	if second == nil {
		t.Fatalf("scheduler stopped after a failed repaint instead of retrying")
	}
	second()
	if calls != 2 {
		t.Fatalf("repaint calls = %d, want 2 (failed first, retried second)", calls)
	}
}

func TestStopWidgetRedrawFencesPendingTick(t *testing.T) {
	f := &redrawFake{}
	app := &App{widgetRedraw: &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: f.redraw}}

	app.widgetRedraw.reset()
	tick := f.next()
	app.stopWidgetRedraw()
	tick()
	if f.redraws != 0 {
		t.Fatalf("stopped scheduler still repainted: %d", f.redraws)
	}
}

func TestTransitionWidgetModeArmsAndResetsRedrawWindow(t *testing.T) {
	f := &redrawFake{}
	app := &App{widgetRedraw: &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: f.redraw}}

	changed, err := app.transitionWidgetMode(true, func() error { return nil })
	if err != nil || !changed {
		t.Fatalf("enter transition: changed=%v err=%v", changed, err)
	}
	stale := f.next()
	if stale == nil {
		t.Fatalf("enter transition did not arm the redraw window")
	}

	changed, err = app.transitionWidgetMode(false, func() error { return nil })
	if err != nil || !changed {
		t.Fatalf("exit transition: changed=%v err=%v", changed, err)
	}
	current := f.next()
	if current == nil {
		t.Fatalf("exit transition did not re-arm the redraw window")
	}

	// The enter switch's tick is now stale and must be ignored.
	stale()
	if f.redraws != 0 {
		t.Fatalf("superseded enter tick redrew: %d", f.redraws)
	}
	current()
	if f.redraws != 1 {
		t.Fatalf("exit switch redraws = %d, want 1", f.redraws)
	}
}

func TestWidgetRedrawSlowPaintKeepsOriginalDeadline(t *testing.T) {
	f := &redrawFake{}
	start := f.now
	var at []time.Duration
	s := &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: func() error {
		at = append(at, f.now.Sub(start))
		f.now = f.now.Add(500 * time.Millisecond)
		return nil
	}}
	s.reset()
	for len(f.pending) > 0 {
		if len(at) > 7 {
			t.Fatal("redraw window did not stop")
		}
		f.next()()
	}
	if len(at) != 7 {
		t.Fatalf("redraws = %d, want 7", len(at))
	}
	for i, elapsed := range at {
		if want := time.Duration(i+1) * 2 * time.Second; elapsed != want {
			t.Fatalf("tick %d drifted to %v, want %v", i, elapsed, want)
		}
	}
}

func TestWidgetRedrawLateDeliveryStopsAtDeadline(t *testing.T) {
	f := &redrawFake{}
	s := &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: f.redraw}
	s.reset()
	tick := f.next()
	f.now = f.now.Add(15 * time.Second)
	tick()
	if f.redraws != 0 || len(f.pending) != 0 || s.running {
		t.Fatal("expired timer repainted or continued scheduling")
	}
}

func TestWidgetRedrawResetDuringPaintDoesNotOverlap(t *testing.T) {
	f := &redrawFake{}
	var s *widgetRedrawScheduler
	calls := 0
	s = &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: func() error {
		calls++
		if calls == 1 {
			s.reset()
			f.next()() // New generation's first tick while the old paint is busy.
			if calls != 1 {
				t.Fatal("redraws overlapped across reset")
			}
		}
		return nil
	}}
	s.reset()
	f.next()()
	if len(f.pending) != 1 {
		t.Fatalf("pending timers = %d, want one new-generation timer", len(f.pending))
	}
	f.next()()
	if calls != 2 {
		t.Fatalf("redraws = %d, want recovery after busy slot", calls)
	}
}

func TestWidgetRedrawStopDuringPaintCannotRestart(t *testing.T) {
	f := &redrawFake{}
	var s *widgetRedrawScheduler
	s = &widgetRedrawScheduler{after: f.after, now: func() time.Time { return f.now }, redraw: func() error {
		s.stop()
		return nil
	}}
	s.reset()
	f.next()()
	s.reset()
	if len(f.pending) != 0 || s.running {
		t.Fatal("shutdown scheduler restarted")
	}
}
