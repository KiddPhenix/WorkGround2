// Package workgate is a dependency-free reader for the persistent global work
// gate shared by the Assistant runtime and ordinary (non-Assistant) sessions.
// It reads the same workcontrol.json the Assistant Store writes, so a pause or
// resume requested through the Assistant surface also fences plain chat
// sessions that mount the same file.
//
// It depends on neither internal/assistant nor internal/control: the Assistant
// Store owns the writer (its workcontrol.go), while this package only needs the
// file path and the stable JSON shape {state, epoch}.
package workgate

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// State is the persistent work gate state. The string values match the
// assistant.WorkControlState constants so both readers and writers agree on the
// wire representation.
type State string

const (
	Running    State = "running"
	Quiescing  State = "quiescing"
	Paused     State = "paused"
	Recovering State = "recovering"
)

// ErrPaused reports that work was refused because the gate is not RUNNING.
var ErrPaused = errors.New("work paused")

// Gate is the read-only view of the persistent work fence. State/Epoch/Revision
// are reads (never blocking) — callers fence writes and execution on Allowed().
type Gate interface {
	State() State
	Epoch() int64
	Revision() int64
	// Allowed reports whether brand-new work may start: true only while RUNNING.
	// Recovery-driven work (checkpoint resume, safe retry, subscription restore)
	// additionally consults AllowedResume, which also admits RECOVERING.
	Allowed() bool
	// AllowedResume reports whether recovery-driven work may proceed. It is true
	// while RUNNING or RECOVERING — during RECOVERING the host is scanning
	// interrupted sessions and re-driving checkpointed turns, which must not be
	// blocked by the same fence that refuses new model turns.
	AllowedResume() bool
}

// File is a Gate backed by a workcontrol.json file on disk. It re-reads the
// file on every call, so it always reflects the latest persisted transition.
// A missing file is the default RUNNING generation (epoch 1); a read or parse
// failure fails closed to PAUSED and records the error for observability.
type File struct {
	path string

	mu  sync.Mutex
	err error
}

// OpenFile returns a gate reading the workcontrol.json at path.
func OpenFile(path string) *File {
	return &File{path: path}
}

// Path returns the watched file path.
func (f *File) Path() string {
	return f.path
}

// LastErr reports the most recent read/parse failure, or nil when the last read
// succeeded (including the missing-file default).
func (f *File) LastErr() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.err
}

func (f *File) setErr(err error) {
	f.mu.Lock()
	f.err = err
	f.mu.Unlock()
}

func (f *File) read() (State, int64, int64, error) {
	data, err := os.ReadFile(f.path)
	if os.IsNotExist(err) {
		f.setErr(nil)
		return Running, 1, 1, nil
	}
	if err != nil {
		f.setErr(err)
		return Paused, 1, 1, err // fail-closed
	}
	var wc struct {
		State    State `json:"state"`
		Epoch    int64 `json:"epoch"`
		Revision int64 `json:"revision"`
	}
	if err := json.Unmarshal(data, &wc); err != nil {
		f.setErr(err)
		return Paused, 1, 1, err // fail-closed
	}
	if wc.Epoch < 1 {
		wc.Epoch = 1
	}
	if wc.Revision < 1 {
		wc.Revision = 1
	}
	switch wc.State {
	case Running, Quiescing, Paused, Recovering:
	default:
		err := fmt.Errorf("workgate: invalid work state %q", wc.State)
		f.setErr(err)
		return Paused, wc.Epoch, wc.Revision, err // fail-closed
	}
	f.setErr(nil)
	return wc.State, wc.Epoch, wc.Revision, nil
}

// State returns the current gate state, defaulting to Running when the file is
// missing and Paused when it cannot be read or parsed.
func (f *File) State() State {
	s, _, _, _ := f.read()
	return s
}

// Epoch returns the current monotonic work generation. A missing file yields
// the default epoch 1.
func (f *File) Epoch() int64 {
	_, e, _, _ := f.read()
	return e
}

// Revision returns the persisted write revision of the gate (each transition
// bumps it), or 1 for the default generation.
func (f *File) Revision() int64 {
	_, _, r, _ := f.read()
	return r
}

// Allowed reports whether new work may start: true only while RUNNING.
func (f *File) Allowed() bool {
	return f.State() == Running
}

// AllowedResume reports whether recovery-driven work may proceed: RUNNING or
// RECOVERING. During RECOVERING the host re-drives checkpointed turns and
// restores subscriptions, which must not be refused like brand-new work.
func (f *File) AllowedResume() bool {
	s := f.State()
	return s == Running || s == Recovering
}
