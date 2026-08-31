package assistant

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"workground2/internal/fileutil"
)

// SupervisorEventKind is the closed set of reasons the supervisor loop wakes up
// for. Lifecycle kinds mirror the Session subsystem's derived statuses; the
// remaining kinds are user input, routines, retries and the idle heartbeat.
// Everything else is deliberately not an event: the loop re-derives state from
// the Store and the Session subsystem when it does wake up.
type SupervisorEventKind string

const (
	// EventUserInput marks a durable direct user input (a Dispatch was opened).
	EventUserInput SupervisorEventKind = "user_input"
	// EventSessionStarted marks a managed Session that transitioned into a
	// running/waiting state from idle/queued.
	EventSessionStarted SupervisorEventKind = "session_started"
	// EventSessionProgressed marks a managed Session that made progress while
	// already running.
	EventSessionProgressed SupervisorEventKind = "session_progressed"
	// EventInteractionRequired marks a managed Session waiting on the user (ask
	// or approval). The supervisor may answer it itself when it is not a hard
	// gate.
	EventInteractionRequired SupervisorEventKind = "interaction_required"
	// EventSessionFailed marks a managed Session that failed.
	EventSessionFailed SupervisorEventKind = "session_failed"
	// EventSessionCompleted marks a managed Session that completed.
	EventSessionCompleted SupervisorEventKind = "session_completed"
	// EventSessionCancelled marks a managed Session that was cancelled.
	EventSessionCancelled SupervisorEventKind = "session_cancelled"
	// EventRoutineDue marks a routine fire that is due (Payload carries the
	// FireID; RequestID is the stable fire ID).
	EventRoutineDue SupervisorEventKind = "routine_due"
	// EventRetryDue marks a deferred decision whose deadline passed (Payload
	// carries the interaction ID).
	EventRetryDue SupervisorEventKind = "retry_due"
	// EventHeartbeat marks an idle heartbeat: the supervisor should re-evaluate
	// the mission when nothing else woke it for a long time.
	EventHeartbeat SupervisorEventKind = "heartbeat"
)

func validSupervisorEventKind(k SupervisorEventKind) bool {
	switch k {
	case EventUserInput, EventSessionStarted, EventSessionProgressed,
		EventInteractionRequired, EventSessionFailed, EventSessionCompleted,
		EventSessionCancelled, EventRoutineDue, EventRetryDue, EventHeartbeat:
		return true
	}
	return false
}

// SupervisorEvent is one durable, mergeable wake-up reason for one Assistant's
// supervisor session. Events are advisory inputs to the supervisor turn: they
// never mutate plan state themselves, so a duplicate, late or out-of-order
// event can at most trigger an extra (idempotent) turn — it can never regress
// the plan. Every event carries its assistant, optional session, revision
// context and a stable request ID so replays are recognizable.
type SupervisorEvent struct {
	ID          string              `json:"id"`
	Kind        SupervisorEventKind `json:"kind"`
	AssistantID string              `json:"assistant_id"`
	SessionID   string              `json:"session_id,omitempty"`
	// Revision is the monotonic context of the observed state (BranchMeta
	// revision or store revision). A pending event is only replaced by a newer
	// revision, never by an older one.
	Revision int64 `json:"revision,omitempty"`
	// RequestID is the stable idempotency key of the underlying action (fire
	// ID, dispatch request ID, ...).
	RequestID string `json:"request_id,omitempty"`
	// Payload is small bounded extra context (fire ID, interaction ID, status).
	Payload string `json:"payload,omitempty"`
	// Requeue marks a watermark event whose stable ID may be enqueued again
	// after it was consumed (the idle heartbeat). Without it the journal would
	// deduplicate the stable ID forever and the heartbeat could never fire
	// twice.
	Requeue bool      `json:"requeue,omitempty"`
	At      time.Time `json:"at"`
}

// eventRecord is one line of the per-assistant event journal: an "add" carries
// the event, a "consume" drops a previously added event ID.
type eventRecord struct {
	Op string           `json:"op"`
	ID string           `json:"id,omitempty"`
	Ev *SupervisorEvent `json:"ev,omitempty"`
}

// SupervisorEventState is the durable high-water mark the event collector
// persists per assistant so a restart re-derives only real transitions: the
// last observed status per managed Session plus the last heartbeat time. It is
// advanced strictly after the corresponding events are enqueued, so a crash in
// between re-enqueues the same transitions (deduplicated by the queue) instead
// of losing them.
type SupervisorEventState struct {
	Version       int               `json:"version"`
	SessionStatus map[string]string `json:"session_status,omitempty"`
	SessionTurns  map[string]int    `json:"session_turns,omitempty"`
	HeartbeatAt   time.Time         `json:"heartbeat_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at,omitempty"`
}

// queueEventsVersion is the state sidecar version; bump only on incompatible
// schema changes.
const queueEventsVersion = 1

// queueCompactionThreshold bounds the journal size. When the record count for
// an assistant exceeds it, the journal is rewritten keeping only pending adds,
// so consumed history never grows without bound while pending events survive.
const queueCompactionThreshold = 1024

// SupervisorEventQueue is the durable, mergeable, non-lossy event queue behind
// the supervisor loop. Each assistant owns an append-only JSONL journal under
// <root>/.events/<assistantID>.jsonl plus a small state sidecar. Enqueues are
// deduplicated by stable event ID and merged by (assistant, kind, session,
// request) keeping the newest revision; consumed events are recorded and
// compacted away. Reopening the queue after a restart replays the journal, so
// pending events survive process restarts. The queue also owns the durable
// supervisor turn checkpoint and the event-batch receipts (see
// SupervisorTurnCheckpoint / SupervisorBatchReceipt).
type SupervisorEventQueue struct {
	root string
	mu   sync.Mutex

	// Per-assistant in-memory state, loaded lazily under the file lock.
	pending map[string][]*SupervisorEvent
	seen    map[string]map[string]struct{}

	// failAppend and failStateSave are fault-injection hooks used by tests to
	// prove crash/IO-failure behavior; nil in production.
	failAppend    func(assistantID string, rec eventRecord) error
	failStateSave func(assistantID string) error
}

// NewSupervisorEventQueue opens the durable supervisor event queue under root.
// The root is the assistant Store root; events live in a hidden .events
// subdirectory so they never collide with aggregate files.
func NewSupervisorEventQueue(root string) (*SupervisorEventQueue, error) {
	if strings.TrimSpace(root) == "" {
		return nil, errors.New("assistant: supervisor event queue root is required")
	}
	dir := filepath.Join(root, ".events")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("assistant: create supervisor event queue: %w", err)
	}
	return &SupervisorEventQueue{
		root:    root,
		pending: map[string][]*SupervisorEvent{},
		seen:    map[string]map[string]struct{}{},
	}, nil
}

func (q *SupervisorEventQueue) eventsDir() string { return filepath.Join(q.root, ".events") }

func (q *SupervisorEventQueue) journalPath(assistantID string) string {
	return filepath.Join(q.eventsDir(), assistantID+".jsonl")
}

func (q *SupervisorEventQueue) statePath(assistantID string) string {
	return filepath.Join(q.eventsDir(), assistantID+".state.json")
}

// lockEventFile serializes journal/state mutations for one assistant across
// processes (desktop UI input vs a daemon leader). Writers are rare (one per
// event), so the lock is only held for the duration of one append.
func (q *SupervisorEventQueue) lockEventFile(assistantID string) (func(), error) {
	lockPath := filepath.Join(q.eventsDir(), assistantID+".lock")
	return lockStoreFile(lockPath, 5*time.Second)
}

// mergeKey is the dedup/merge identity of an event within the pending window.
// A newer event with the same key replaces the older one; an older one is
// dropped so late arrivals can never regress the observed state.
func mergeKey(ev *SupervisorEvent) string {
	return strings.Join([]string{
		ev.AssistantID, string(ev.Kind), ev.SessionID, ev.RequestID,
	}, "\x00")
}

// newerThan reports whether ev is strictly newer than existing for merge
// purposes: revision wins when both carry one, otherwise time.
func newerThan(ev, existing *SupervisorEvent) bool {
	if ev.Revision != 0 && existing.Revision != 0 {
		return ev.Revision > existing.Revision
	}
	return ev.At.After(existing.At)
}

// loadLocked replays the journal for one assistant into the in-memory pending
// list and seen set. The caller holds q.mu and the per-assistant file lock.
func (q *SupervisorEventQueue) loadLocked(assistantID string) error {
	if _, ok := q.pending[assistantID]; ok {
		return nil
	}
	q.pending[assistantID] = nil
	q.seen[assistantID] = map[string]struct{}{}
	f, err := os.Open(q.journalPath(assistantID))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("assistant: open supervisor event journal: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var rec eventRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue // tolerate a torn tail line from a crash mid-append
		}
		switch rec.Op {
		case "add":
			if rec.Ev == nil || !validSupervisorEventKind(rec.Ev.Kind) || rec.Ev.ID == "" {
				continue
			}
			if _, dup := q.seen[assistantID][rec.Ev.ID]; dup && !rec.Ev.Requeue {
				continue
			}
			if rec.Ev.Requeue && pendingHasID(q.pending[assistantID], rec.Ev.ID) {
				continue
			}
			q.seen[assistantID][rec.Ev.ID] = struct{}{}
			q.pending[assistantID] = append(q.pending[assistantID], rec.Ev)
		case "consume":
			if rec.ID == "" {
				continue
			}
			q.seen[assistantID][rec.ID] = struct{}{}
			q.pending[assistantID] = removeEvent(q.pending[assistantID], rec.ID)
		}
	}
	return scanner.Err()
}

func removeEvent(events []*SupervisorEvent, id string) []*SupervisorEvent {
	out := events[:0]
	for _, ev := range events {
		if ev.ID != id {
			out = append(out, ev)
		}
	}
	return out
}

// pendingHasID reports whether id is currently pending (used by Requeue events,
// which may be re-enqueued only after they were consumed).
func pendingHasID(events []*SupervisorEvent, id string) bool {
	for _, ev := range events {
		if ev.ID == id {
			return true
		}
	}
	return false
}

// appendRecordLocked appends one journal line durably. The caller holds q.mu
// and the per-assistant file lock.
func (q *SupervisorEventQueue) appendRecordLocked(assistantID string, rec eventRecord) error {
	if q.failAppend != nil {
		if err := q.failAppend(assistantID, rec); err != nil {
			return err
		}
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(q.journalPath(assistantID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("assistant: append supervisor event: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("assistant: append supervisor event: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("assistant: sync supervisor event: %w", err)
	}
	return f.Close()
}

// Enqueue durably records one supervisor event. It returns applied=true when
// the event was newly recorded (or replaced a pending one) and false when it
// was a duplicate or an older merge. Enqueuing never blocks on the supervisor:
// the caller (the runtime tick or a UI input path) wakes the loop separately.
func (q *SupervisorEventQueue) Enqueue(ev SupervisorEvent) (applied bool, err error) {
	ev.AssistantID = strings.TrimSpace(ev.AssistantID)
	if ev.AssistantID == "" {
		return false, errors.New("assistant: supervisor event requires an assistant id")
	}
	if !validSupervisorEventKind(ev.Kind) {
		return false, fmt.Errorf("assistant: invalid supervisor event kind %q", ev.Kind)
	}
	if ev.ID == "" {
		return false, errors.New("assistant: supervisor event requires a stable id")
	}
	if ev.At.IsZero() {
		ev.At = time.Now()
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	unlock, err := q.lockEventFile(ev.AssistantID)
	if err != nil {
		return false, err
	}
	defer unlock()
	if err := q.loadLocked(ev.AssistantID); err != nil {
		return false, err
	}
	// A stable ID already seen is a duplicate — unless the event is a Requeue
	// watermark (heartbeat): then only an actually-pending copy dedups, so a
	// consumed watermark can fire again with the same stable ID.
	if _, dup := q.seen[ev.AssistantID][ev.ID]; dup && !ev.Requeue {
		return false, nil
	}
	if ev.Requeue && pendingHasID(q.pending[ev.AssistantID], ev.ID) {
		return false, nil
	}
	// Merge: replace the last pending event with the same key only when the new
	// event is strictly newer; an older or equal one is dropped.
	key := mergeKey(&ev)
	replaced := false
	pending := q.pending[ev.AssistantID]
	for i := len(pending) - 1; i >= 0; i-- {
		if mergeKey(pending[i]) != key {
			continue
		}
		if !newerThan(&ev, pending[i]) {
			return false, nil // duplicate/older: never regress
		}
		q.seen[ev.AssistantID][pending[i].ID] = struct{}{}
		pending = removeEvent(pending, pending[i].ID)
		q.pending[ev.AssistantID] = pending
		replaced = true
		break
	}
	q.seen[ev.AssistantID][ev.ID] = struct{}{}
	q.pending[ev.AssistantID] = append(q.pending[ev.AssistantID], cloneSupervisorEvent(&ev))
	if err := q.appendRecordLocked(ev.AssistantID, eventRecord{Op: "add", Ev: &ev}); err != nil {
		// Roll back the in-memory state so a failed append is retryable.
		delete(q.seen[ev.AssistantID], ev.ID)
		q.pending[ev.AssistantID] = removeEvent(q.pending[ev.AssistantID], ev.ID)
		if replaced {
			q.pending[ev.AssistantID] = append(q.pending[ev.AssistantID], nil) // placeholder; recomputed below
			q.pending[ev.AssistantID] = q.pending[ev.AssistantID][:len(q.pending[ev.AssistantID])-1]
		}
		return false, err
	}
	_ = q.compactLocked(ev.AssistantID)
	return true, nil
}

// compactLocked rewrites the journal keeping only pending adds when it grew
// past the threshold. The caller holds q.mu and the file lock.
func (q *SupervisorEventQueue) compactLocked(assistantID string) error {
	if _, err := os.Stat(q.journalPath(assistantID)); os.IsNotExist(err) {
		return nil
	}
	// Count records cheaply; only rewrite when clearly oversized.
	info, err := os.Stat(q.journalPath(assistantID))
	if err != nil {
		return nil
	}
	if info.Size() < 64*1024 {
		return nil
	}
	var b strings.Builder
	for _, ev := range q.pending[assistantID] {
		rec := eventRecord{Op: "add", Ev: ev}
		data, err := json.Marshal(rec)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
	}
	return fileutil.AtomicWriteFile(q.journalPath(assistantID), []byte(b.String()), 0o600)
}

// Pending returns the currently pending events for one assistant in enqueue
// order, oldest first. A clone is returned so callers cannot mutate the queue.
func (q *SupervisorEventQueue) Pending(assistantID string) ([]SupervisorEvent, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err := q.loadLocked(assistantID); err != nil {
		return nil, err
	}
	pending := q.pending[assistantID]
	out := make([]SupervisorEvent, 0, len(pending))
	for _, ev := range pending {
		out = append(out, *cloneSupervisorEvent(ev))
	}
	return out, nil
}

// HasPending reports whether one assistant has unconsumed events.
func (q *SupervisorEventQueue) HasPending(assistantID string) (bool, error) {
	events, err := q.Pending(assistantID)
	if err != nil {
		return false, err
	}
	return len(events) > 0, nil
}

// MarkConsumed durably records that the given event IDs were consumed by a
// supervisor turn. It is idempotent: consuming an unknown or already-consumed
// ID is a no-op. A turn marks its trigger events consumed only after it
// succeeded; a failed turn leaves them pending for the next retry.
func (q *SupervisorEventQueue) MarkConsumed(assistantID string, ids ...string) error {
	if len(ids) == 0 {
		return nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return err
	}
	defer unlock()
	if err := q.loadLocked(assistantID); err != nil {
		return err
	}
	changed := false
	for _, id := range ids {
		if id == "" {
			continue
		}
		pending := q.pending[assistantID]
		found := false
		for _, ev := range pending {
			if ev.ID == id {
				found = true
				break
			}
		}
		if !found {
			continue
		}
		if err := q.appendRecordLocked(assistantID, eventRecord{Op: "consume", ID: id}); err != nil {
			return err
		}
		q.pending[assistantID] = removeEvent(q.pending[assistantID], id)
		changed = true
	}
	if changed {
		_ = q.compactLocked(assistantID)
	}
	return nil
}

// LoadState returns the durable event-collector high-water state for one
// assistant, or an empty state when none has been saved yet.
func (q *SupervisorEventQueue) LoadState(assistantID string) (SupervisorEventState, error) {
	data, err := os.ReadFile(q.statePath(assistantID))
	if os.IsNotExist(err) {
		return SupervisorEventState{}, nil
	}
	if err != nil {
		return SupervisorEventState{}, err
	}
	var state SupervisorEventState
	if err := json.Unmarshal(data, &state); err != nil {
		return SupervisorEventState{}, fmt.Errorf("assistant: parse supervisor event state: %w", err)
	}
	if state.Version != queueEventsVersion {
		return SupervisorEventState{}, nil // incompatible sidecar: start fresh
	}
	if state.SessionStatus == nil {
		state.SessionStatus = map[string]string{}
	}
	return state, nil
}

// SaveState durably persists the event-collector high-water state. Callers must
// only save AFTER the corresponding events were enqueued, so a crash between
// enqueue and save re-enqueues (and dedups) instead of losing transitions.
func (q *SupervisorEventQueue) SaveState(assistantID string, state SupervisorEventState) error {
	if q.failStateSave != nil {
		if err := q.failStateSave(assistantID); err != nil {
			return err
		}
	}
	state.Version = queueEventsVersion
	state.UpdatedAt = time.Now()
	if state.SessionStatus == nil {
		state.SessionStatus = map[string]string{}
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := fileutil.AtomicWriteFile(q.statePath(assistantID), data, 0o600); err != nil {
		return fmt.Errorf("assistant: save supervisor event state: %w", err)
	}
	return nil
}

func cloneSupervisorEvent(ev *SupervisorEvent) *SupervisorEvent {
	if ev == nil {
		return nil
	}
	out := *ev
	return &out
}

// SupervisorTurnCheckpoint is the durable record that one supervisor turn was
// submitted on the assistant's supervisor Session and has not been settled
// yet. It is written (first-write-wins) BEFORE the turn is submitted — the
// intent — so a concurrent tick or a second process settles the existing
// checkpoint instead of submitting a second turn, and it survives restarts:
// recovery never depends on in-process flags. HistoryLen is the durable
// Session transcript length right before the submission; the executor probes
// it BEFORE saving the intent (never the zero value on a Session that already
// carries history), and the host re-reports it when the turn is in flight. A
// settled turn whose history did not grow past it is treated as
// never-durably-submitted and re-submitted.
type SupervisorTurnCheckpoint struct {
	TurnID      string    `json:"turn_id"`
	SubmittedAt time.Time `json:"submitted_at"`
	PromptHash  string    `json:"prompt_hash,omitempty"`
	EventIDs    []string  `json:"event_ids,omitempty"`
	BatchID     string    `json:"batch_id,omitempty"`
	HistoryLen  int       `json:"history_len,omitempty"`
	// Ref is the actual supervisor Session this turn ran on. A snapshot
	// recovery can move the turn onto a new physical Session file; persisting
	// the ref lets the next settle follow the same Session instead of
	// re-opening the discoverable root. Empty for legacy checkpoints written
	// before this field existed.
	Ref SupervisorSessionRef `json:"ref,omitempty"`
	// EvidenceObservedThrough is the newest evidence CreatedAt this turn
	// actually saw (snapshot-derived). The settle advances the evidence
	// watermark to exactly this boundary — never wall-clock now — so a
	// concurrent/late evidence record written after the snapshot but stamped
	// earlier is not swallowed. Zero means the snapshot had no evidence, or the
	// checkpoint is legacy (written before this field existed): the watermark is
	// left untouched.
	EvidenceObservedThrough time.Time `json:"evidence_observed_through,omitempty"`
}

// SupervisorBatchReceipt durably links one event batch to the decision/action
// routed for it. It is written after the acting phase succeeds and BEFORE the
// trigger events are consumed, so a crash between the two replays as
// already_applied: the events are consumed without re-running the model turn
// or the external action.
type SupervisorBatchReceipt struct {
	BatchID     string               `json:"batch_id"`
	AssistantID string               `json:"assistant_id"`
	EventIDs    []string             `json:"event_ids,omitempty"`
	Decision    SupervisorDecision   `json:"decision"`
	Outcome     DecisionRouteOutcome `json:"outcome"`
	RoutedAt    time.Time            `json:"routed_at"`
}

// checkpointPath is the durable per-assistant turn checkpoint file.
func (q *SupervisorEventQueue) checkpointPath(assistantID string) string {
	return filepath.Join(q.eventsDir(), assistantID+".turn.json")
}

// SaveTurnCheckpoint durably records that a supervisor turn submission is in
// flight for the assistant. It is first-write-wins (O_EXCL): a concurrent tick
// or a second process that loses the race returns created=false and must
// settle the existing checkpoint instead of submitting a second turn.
func (q *SupervisorEventQueue) SaveTurnCheckpoint(assistantID string, cp SupervisorTurnCheckpoint) (created bool, err error) {
	if strings.TrimSpace(assistantID) == "" || cp.TurnID == "" {
		return false, errors.New("assistant: supervisor turn checkpoint requires assistant and turn id")
	}
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return false, err
	}
	defer unlock()
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return false, err
	}
	data = append(data, '\n')
	f, err := os.OpenFile(q.checkpointPath(assistantID), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("assistant: save supervisor turn checkpoint: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(q.checkpointPath(assistantID))
		return false, fmt.Errorf("assistant: save supervisor turn checkpoint: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(q.checkpointPath(assistantID))
		return false, fmt.Errorf("assistant: save supervisor turn checkpoint: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(q.checkpointPath(assistantID))
		return false, fmt.Errorf("assistant: save supervisor turn checkpoint: %w", err)
	}
	return true, nil
}

// UpdateTurnCheckpoint rewrites the checkpoint of the turn this process owns
// (e.g. to fill in the Session history baseline once the host reports the turn
// in flight).
func (q *SupervisorEventQueue) UpdateTurnCheckpoint(assistantID string, cp SupervisorTurnCheckpoint) error {
	if strings.TrimSpace(assistantID) == "" || cp.TurnID == "" {
		return errors.New("assistant: supervisor turn checkpoint requires assistant and turn id")
	}
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return err
	}
	defer unlock()
	data, err := json.MarshalIndent(cp, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if err := fileutil.AtomicWriteFile(q.checkpointPath(assistantID), data, 0o600); err != nil {
		return fmt.Errorf("assistant: update supervisor turn checkpoint: %w", err)
	}
	return nil
}

// LoadTurnCheckpoint returns the durable in-flight turn checkpoint, or
// ok=false when no turn is checkpointed.
func (q *SupervisorEventQueue) LoadTurnCheckpoint(assistantID string) (SupervisorTurnCheckpoint, bool, error) {
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return SupervisorTurnCheckpoint{}, false, err
	}
	defer unlock()
	data, err := os.ReadFile(q.checkpointPath(assistantID))
	if os.IsNotExist(err) {
		return SupervisorTurnCheckpoint{}, false, nil
	}
	if err != nil {
		return SupervisorTurnCheckpoint{}, false, err
	}
	var cp SupervisorTurnCheckpoint
	if err := json.Unmarshal(data, &cp); err != nil {
		return SupervisorTurnCheckpoint{}, false, fmt.Errorf("assistant: parse supervisor turn checkpoint: %w", err)
	}
	if cp.TurnID == "" {
		return SupervisorTurnCheckpoint{}, false, fmt.Errorf("assistant: supervisor turn checkpoint is corrupt (empty turn id)")
	}
	return cp, true, nil
}

// ClearTurnCheckpoint removes the in-flight checkpoint after the turn settled.
// It is idempotent: clearing a missing checkpoint is a no-op.
func (q *SupervisorEventQueue) ClearTurnCheckpoint(assistantID string) error {
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return err
	}
	defer unlock()
	err = os.Remove(q.checkpointPath(assistantID))
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// batchReceiptPath maps an event-batch ID to a hash-derived file name; the raw
// ID never touches the file system.
func (q *SupervisorEventQueue) batchReceiptPath(assistantID, batchID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(assistantID) + "/" + batchID))
	return filepath.Join(q.eventsDir(), "batches", hex.EncodeToString(sum[:16])+".json")
}

// SaveBatchReceipt durably records that one event batch's decision was routed.
// It is first-write-wins: a replay writes nothing and keeps the original
// outcome, so the action is never re-executed.
func (q *SupervisorEventQueue) SaveBatchReceipt(assistantID string, rec SupervisorBatchReceipt) error {
	if strings.TrimSpace(assistantID) == "" || strings.TrimSpace(rec.BatchID) == "" {
		return errors.New("assistant: supervisor batch receipt requires assistant and batch id")
	}
	if rec.RoutedAt.IsZero() {
		rec.RoutedAt = time.Now()
	}
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return err
	}
	defer unlock()
	if _, ok, err := q.loadBatchReceiptLocked(assistantID, rec.BatchID); err != nil {
		return err
	} else if ok {
		return nil // first-write-wins: a replay keeps the original outcome
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	path := q.batchReceiptPath(assistantID, rec.BatchID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if os.IsExist(err) {
		return nil // raced with another writer: first-write-wins
	}
	if err != nil {
		return fmt.Errorf("assistant: save supervisor batch receipt: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("assistant: save supervisor batch receipt: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		_ = os.Remove(path)
		return fmt.Errorf("assistant: save supervisor batch receipt: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return fmt.Errorf("assistant: save supervisor batch receipt: %w", err)
	}
	return nil
}

// loadBatchReceiptLocked reads one batch receipt. The caller holds the file
// lock.
func (q *SupervisorEventQueue) loadBatchReceiptLocked(assistantID, batchID string) (SupervisorBatchReceipt, bool, error) {
	data, err := os.ReadFile(q.batchReceiptPath(assistantID, batchID))
	if os.IsNotExist(err) {
		return SupervisorBatchReceipt{}, false, nil
	}
	if err != nil {
		return SupervisorBatchReceipt{}, false, err
	}
	var rec SupervisorBatchReceipt
	if err := json.Unmarshal(data, &rec); err != nil {
		return SupervisorBatchReceipt{}, false, fmt.Errorf("assistant: parse supervisor batch receipt: %w", err)
	}
	return rec, true, nil
}

// LoadBatchReceipt returns the routed-decision receipt of one event batch, or
// ok=false when this batch was never routed.
func (q *SupervisorEventQueue) LoadBatchReceipt(assistantID, batchID string) (SupervisorBatchReceipt, bool, error) {
	unlock, err := q.lockEventFile(assistantID)
	if err != nil {
		return SupervisorBatchReceipt{}, false, err
	}
	defer unlock()
	return q.loadBatchReceiptLocked(assistantID, batchID)
}

// sortEventsByAt orders events oldest first; used by tests and the collector
// to render a stable summary.
func sortEventsByAt(events []SupervisorEvent) {
	sort.SliceStable(events, func(i, j int) bool { return events[i].At.Before(events[j].At) })
}
