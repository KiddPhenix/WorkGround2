package main

import (
	"log/slog"
	"sync"
	"time"
)

const (
	// widgetRedrawInterval spaces the short post-switch repaint window. With
	// widgetRedrawTicks the window repaints at 2/4/6/8/10/12/14 seconds and then
	// stops, without ever repainting the synchronous open path itself.
	widgetRedrawInterval = 2 * time.Second
	widgetRedrawWindow   = 15 * time.Second
	widgetRedrawTicks    = 7
)

// widgetRedrawScheduler is the single-flight short redraw window that follows a
// successful widget/main window switch. It repaints the native window a few
// times to clear the occasional stale rectangle left by the DWM/layered-window
// transition. A newer switch resets the window; a late tick from a superseded
// switch is fenced off by a generation check. An already executing repaint
// finishes against the current window without restoring old geometry or HRGN.
type widgetRedrawScheduler struct {
	mu         sync.Mutex
	paintMu    sync.Mutex
	generation uint64
	running    bool
	closed     bool
	started    time.Time
	timer      *time.Timer

	// after/redraw are test seams; nil uses time.AfterFunc and the platform
	// repaint respectively. They are set once at construction and never mutated.
	after  func(time.Duration, func()) *time.Timer
	now    func() time.Time
	redraw func() error
}

func newWidgetRedrawScheduler() *widgetRedrawScheduler {
	return &widgetRedrawScheduler{}
}

// stopWidgetRedraw cancels the pending post-switch redraw window. Safe when the
// scheduler is nil (tests and pre-NewApp construction).
func (a *App) stopWidgetRedraw() {
	if a.widgetRedraw != nil {
		a.widgetRedraw.stop()
	}
}

// reset starts, or restarts, the post-switch redraw window. It never blocks the
// synchronous switch path: it only arms a timer.
func (s *widgetRedrawScheduler) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.generation++
	gen := s.generation
	s.running = true
	s.started = s.clock()
	s.cancelTimerLocked()
	s.scheduleLocked(gen)
}

// stop cancels the pending window and fences every in-flight tick. It is called
// on app shutdown. Native calls already executing cannot be cancelled; stop
// never waits on them because the window thread may be handling shutdown.
func (s *widgetRedrawScheduler) stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.generation++
	s.running = false
	s.closed = true
	s.cancelTimerLocked()
}

func (s *widgetRedrawScheduler) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

func (s *widgetRedrawScheduler) afterFunc() func(time.Duration, func()) *time.Timer {
	if s.after != nil {
		return s.after
	}
	return time.AfterFunc
}

func (s *widgetRedrawScheduler) redrawFunc() func() error {
	if s.redraw != nil {
		return s.redraw
	}
	return redrawWidgetWindowForRefresh
}

func (s *widgetRedrawScheduler) cancelTimerLocked() {
	if s.timer != nil {
		s.timer.Stop()
		s.timer = nil
	}
}

func (s *widgetRedrawScheduler) scheduleLocked(gen uint64) {
	now := s.clock()
	elapsed := now.Sub(s.started)
	next := s.started.Add((elapsed/widgetRedrawInterval + 1) * widgetRedrawInterval)
	if !next.Before(s.started.Add(widgetRedrawWindow)) {
		s.running = false
		return
	}
	s.timer = s.afterFunc()(next.Sub(now), func() {
		s.fire(gen)
	})
}

func (s *widgetRedrawScheduler) fire(gen uint64) {
	s.mu.Lock()
	if !s.running || s.generation != gen {
		s.mu.Unlock()
		return
	}
	s.timer = nil
	if !s.clock().Before(s.started.Add(widgetRedrawWindow)) {
		s.running = false
		s.mu.Unlock()
		return
	}
	// Skip a busy slot rather than overlap with an old in-flight repaint or
	// accumulate catch-up work. Reset only takes mu, so it remains responsive.
	if !s.paintMu.TryLock() {
		s.scheduleLocked(gen)
		s.mu.Unlock()
		return
	}
	s.mu.Unlock()

	if err := s.redrawFunc()(); err != nil {
		// Observable and non-fatal: the next scheduled tick retries, and the
		// deadline still applies after a slow or failed repaint.
		slog.Warn("widget: post-switch redraw failed", "err", err)
	}
	s.paintMu.Unlock()
	s.mu.Lock()
	if s.running && s.generation == gen {
		s.scheduleLocked(gen)
	}
	s.mu.Unlock()
}
