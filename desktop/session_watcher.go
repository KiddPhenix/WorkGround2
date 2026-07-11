package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

// sessionWatcher monitors session directories for file changes and notifies the
// frontend via Wails events so the session list stays in sync when external
// processes (CLI, another Desktop window) modify session files.
type sessionWatcher struct {
	app     *App
	w       *fsnotify.Watcher
	dirs    map[string]bool
	timer   *time.Timer
	mu      sync.Mutex
	stopped bool
}

const sessionWatchDebounce = 300 * time.Millisecond

// startSessionWatcher creates and starts a filesystem watcher on all known
// session directories. Call from startup(); the watcher stops when the Wails
// context is cancelled or shutdown() is called.
func (a *App) startSessionWatcher() {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("[session-watcher] fsnotify init: %v", err)
		return
	}
	sw := &sessionWatcher{
		app:  a,
		w:    w,
		dirs: map[string]bool{},
	}
	a.sessionWatcher = sw

	// Watch initial set of known session dirs.
	for _, dir := range a.knownSessionDirs() {
		sw.addDir(dir)
	}

	// Process events in a goroutine.
	a.goSafe("sessionWatcherLoop", func() {
		sw.loop()
	})
}

// addDir adds a directory and its .trash subdirectory to the watcher.
func (sw *sessionWatcher) addDir(dir string) {
	dir = filepath.Clean(dir)
	if sw.dirs[dir] {
		return
	}
	if err := sw.w.Add(dir); err != nil {
		if !os.IsNotExist(err) {
			log.Printf("[session-watcher] watch %s: %v", dir, err)
		}
		return
	}
	sw.dirs[dir] = true

	// Also watch .trash/ if it exists.
	trash := filepath.Join(dir, ".trash")
	if info, err := os.Stat(trash); err == nil && info.IsDir() {
		if err := sw.w.Add(trash); err != nil {
			log.Printf("[session-watcher] watch %s: %v", trash, err)
		}
	}
}

// refreshDirs re-scans known session directories and adds any new ones.
func (sw *sessionWatcher) refreshDirs() {
	for _, dir := range sw.app.knownSessionDirs() {
		sw.addDir(dir)
	}
}

func (sw *sessionWatcher) loop() {
	for {
		select {
		case ev, ok := <-sw.w.Events:
			if !ok {
				return
			}
			if sw.isRelevant(ev) {
				sw.scheduleNotify()
			}
		case err, ok := <-sw.w.Errors:
			if !ok {
				return
			}
			log.Printf("[session-watcher] error: %v", err)
		case <-sw.app.ctx.Done():
			sw.stop()
			return
		}
	}
}

// isRelevant reports whether the event is on a file that should trigger a
// session-list refresh: .jsonl transcripts, .titles.json, or trash contents.
func (sw *sessionWatcher) isRelevant(ev fsnotify.Event) bool {
	name := filepath.Base(ev.Name)
	if name == "" {
		return false
	}

	// .titles.json sidecar changes.
	if name == ".titles.json" {
		return true
	}
	// .jsonl files (transcripts) — create / write / remove.
	if filepath.Ext(name) == ".jsonl" {
		return true
	}
	// .trash/ directory changes (items added/removed).
	if strings.HasSuffix(filepath.ToSlash(ev.Name), "/.trash") ||
		strings.Contains(filepath.ToSlash(ev.Name), "/.trash/") {
		return true
	}
	// .jsonl.meta sidecar changes.
	if filepath.Ext(name) == ".meta" && strings.HasSuffix(name, ".jsonl.meta") {
		return true
	}
	return false
}

// scheduleNotify debounces change events so a batch of rapid writes (e.g. saving
// a transcript that also touches .meta and .goal-state) triggers a single
// frontend refresh.
func (sw *sessionWatcher) scheduleNotify() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.stopped {
		return
	}
	if sw.timer != nil {
		sw.timer.Stop()
	}
	sw.timer = time.AfterFunc(sessionWatchDebounce, func() {
		sw.notifyFrontend()
	})
}

// notifyFrontend emits a Wails event so the React UI can refresh its session
// list. This runs on the timer goroutine; EventsEmit is safe to call from any
// goroutine.
func (sw *sessionWatcher) notifyFrontend() {
	ctx := sw.app.ctx
	if ctx == nil {
		return
	}
	// Refresh watched directories in case new projects were opened.
	sw.refreshDirs()
	runtime.EventsEmit(ctx, "session:changed")
}

// stop closes the watcher and cancels any pending timer.
func (sw *sessionWatcher) stop() {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if sw.stopped {
		return
	}
	sw.stopped = true
	if sw.timer != nil {
		sw.timer.Stop()
		sw.timer = nil
	}
	_ = sw.w.Close()
}
