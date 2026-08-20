package runhub

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"workground2/internal/fileutil"
)

// Store is the durable file store behind RunHub. It is a single-writer store:
// the Hub serializes all mutations under its own lock, so Store methods need no
// cross-process coordination beyond the repository's atomic-write convention.
//
// Layout (relative to root):
//
//	runs/<runId>/meta.json        run snapshot, atomically replaced
//	runs/<runId>/events.jsonl      applied events, append-only
//	launches/<hash(requestId)>.json launch idempotency receipt
//	inbox/<hash(eventId)>.json      event idempotency receipt
//
// meta and receipt files are written via fileutil.AtomicWriteFile so a crash
// leaves either the old file or the complete new file. events.jsonl is a log,
// so it appends + fsyncs one full line per applied event.
type Store struct {
	root string
}

const maxStoredEventLine = 1 << 20

// LaunchReceipt is the durable record proving a requestId maps to exactly one
// run, even across restarts.
type LaunchReceipt struct {
	RequestID string        `json:"requestId"`
	RunID     RunID         `json:"runId"`
	Status    ReceiptStatus `json:"status"`
	Intent    LaunchIntent  `json:"intent,omitempty"`
	CreatedAt time.Time     `json:"createdAt"`
}

// EventReceipt is the durable record proving an eventId was already evaluated,
// so a re-delivery returns the same verdict without re-reducing.
type EventReceipt struct {
	EventID   EventID       `json:"eventId"`
	RunID     RunID         `json:"runId"`
	Status    ReceiptStatus `json:"status"`
	Revision  uint64        `json:"revision"`
	AppliedAt time.Time     `json:"appliedAt,omitempty"`
	Event     RunEvent      `json:"event,omitempty"`
}

// Open prepares the store directories under root. It does not load or validate
// existing content; the Hub reloads and surfaces corruption explicitly.
func Open(root string) (*Store, error) {
	s := &Store{root: root}
	for _, dir := range []string{s.runDir(""), s.launchDir(), s.inboxDir()} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("runhub: create store dir %s: %w", dir, err)
		}
	}
	return s, nil
}

func (s *Store) runDir(id RunID) string {
	return filepath.Join(s.root, "runs", string(id))
}

func (s *Store) runMetaPath(id RunID) string {
	return filepath.Join(s.runDir(id), "meta.json")
}

func (s *Store) runEventsPath(id RunID) string {
	return filepath.Join(s.runDir(id), "events.jsonl")
}

func (s *Store) launchDir() string {
	return filepath.Join(s.root, "launches")
}

// receiptFileName maps an opaque request/event id to a filesystem-safe receipt
// filename. The original id is stored inside the receipt and verified on load so
// colons and other opaque punctuation never appear on disk.
func receiptFileName(id string) string {
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:]) + ".json"
}

func (s *Store) launchPath(requestID string) string {
	return filepath.Join(s.launchDir(), receiptFileName(requestID))
}

func (s *Store) inboxDir() string {
	return filepath.Join(s.root, "inbox")
}

func (s *Store) inboxPath(eventID EventID) string {
	return filepath.Join(s.inboxDir(), receiptFileName(string(eventID)))
}

// LoadRun reads a run snapshot. ok is false when the run does not exist. A
// corrupt or unreadable file is an error, never a silent skip.
func (s *Store) LoadRun(id RunID) (AgentRun, bool, error) {
	var run AgentRun
	ok, err := s.readJSON(s.runMetaPath(id), &run)
	return run, ok, err
}

// SaveRun atomically replaces the run snapshot.
func (s *Store) SaveRun(run AgentRun) error {
	return s.writeJSON(s.runMetaPath(run.ID), run)
}

// AppendEvent appends one applied event to the run's event log.
func (s *Store) AppendEvent(evt RunEvent) error {
	line, err := json.Marshal(evt)
	if err != nil {
		return fmt.Errorf("runhub: marshal event: %w", err)
	}
	line = append(line, '\n')

	dir := s.runDir(evt.RunID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("runhub: create run dir %s: %w", dir, err)
	}
	f, err := os.OpenFile(s.runEventsPath(evt.RunID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("runhub: open events log: %w", err)
	}
	if _, err := f.Write(line); err != nil {
		f.Close()
		return fmt.Errorf("runhub: append event: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("runhub: sync events log: %w", err)
	}
	return f.Close()
}

// ListEvents validates and returns the append-only event log for one run. A
// partial or malformed stable line is corruption and fails reload explicitly.
func (s *Store) ListEvents(id RunID) ([]RunEvent, error) {
	f, err := os.Open(s.runEventsPath(id))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runhub: open event log for %q: %w", id, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStoredEventLine)
	seen := make(map[EventID]struct{})
	var events []RunEvent
	for line := 1; scanner.Scan(); line++ {
		var evt RunEvent
		if err := json.Unmarshal(scanner.Bytes(), &evt); err != nil {
			return nil, fmt.Errorf("runhub: corrupt event log for %q at line %d: %w", id, line, err)
		}
		if err := ValidateEvent(evt); err != nil {
			return nil, fmt.Errorf("runhub: corrupt event log for %q at line %d: %w", id, line, err)
		}
		if evt.RunID != id {
			return nil, fmt.Errorf("runhub: corrupt event log for %q at line %d: event references run %q", id, line, evt.RunID)
		}
		if _, ok := seen[evt.EventID]; ok {
			return nil, fmt.Errorf("runhub: corrupt event log for %q: duplicate event %q", id, evt.EventID)
		}
		seen[evt.EventID] = struct{}{}
		events = append(events, evt)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("runhub: read event log for %q: %w", id, err)
	}
	return events, nil
}

// RepairEventLog verifies that the durable log is a prefix of canonical and
// appends any missing tail. Accepted event receipts are authoritative, so a
// crash between receipt persistence and log append can self-heal without
// accepting divergent or reordered stable content.
func (s *Store) RepairEventLog(id RunID, canonical []RunEvent) error {
	logged, err := s.ListEvents(id)
	if err != nil {
		return err
	}
	if len(logged) > len(canonical) {
		return fmt.Errorf("runhub: event log for %q has %d entries, receipts have %d", id, len(logged), len(canonical))
	}
	for i := range logged {
		if logged[i] != canonical[i] {
			return fmt.Errorf("runhub: event log %q event %q diverges from its receipt", id, logged[i].EventID)
		}
	}
	for _, evt := range canonical[len(logged):] {
		if err := s.AppendEvent(evt); err != nil {
			return err
		}
	}
	return nil
}

// SaveLaunchReceipt persists the launch idempotency receipt.
func (s *Store) SaveLaunchReceipt(requestID string, rec LaunchReceipt) error {
	return s.writeJSON(s.launchPath(requestID), rec)
}

// LoadLaunchReceipt reads a launch receipt; ok is false when absent. It verifies
// that the record's original request id matches the requested id.
func (s *Store) LoadLaunchReceipt(requestID string) (LaunchReceipt, bool, error) {
	var rec LaunchReceipt
	ok, err := s.readJSON(s.launchPath(requestID), &rec)
	if err != nil || !ok {
		return rec, ok, err
	}
	if rec.RequestID != requestID {
		return rec, false, fmt.Errorf("runhub: corrupt launch receipt %s: record id %q does not match %q", s.launchPath(requestID), rec.RequestID, requestID)
	}
	return rec, true, nil
}

// SaveEventReceipt persists the event idempotency receipt.
func (s *Store) SaveEventReceipt(eventID EventID, rec EventReceipt) error {
	return s.writeJSON(s.inboxPath(eventID), rec)
}

// LoadEventReceipt reads an event receipt; ok is false when absent. It verifies
// that the record's original event id matches the requested id.
func (s *Store) LoadEventReceipt(eventID EventID) (EventReceipt, bool, error) {
	var rec EventReceipt
	ok, err := s.readJSON(s.inboxPath(eventID), &rec)
	if err != nil || !ok {
		return rec, ok, err
	}
	if rec.EventID != eventID {
		return rec, false, fmt.Errorf("runhub: corrupt event receipt %s: record id %q does not match %q", s.inboxPath(eventID), rec.EventID, eventID)
	}
	return rec, true, nil
}

// ListRuns returns every stored run ordered by CreatedAt, oldest first.
func (s *Store) ListRuns() ([]AgentRun, error) {
	entries, err := os.ReadDir(s.runDir(""))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runhub: read runs dir: %w", err)
	}
	var runs []AgentRun
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		run, ok, err := s.LoadRun(RunID(e.Name()))
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		if run.ID != RunID(e.Name()) {
			return nil, fmt.Errorf("runhub: corrupt run %s: meta id %q does not match directory name", e.Name(), run.ID)
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		if runs[i].CreatedAt.Equal(runs[j].CreatedAt) {
			return runs[i].ID < runs[j].ID
		}
		return runs[i].CreatedAt.Before(runs[j].CreatedAt)
	})
	return runs, nil
}

// ListLaunchReceipts returns every persisted launch receipt.
func (s *Store) ListLaunchReceipts() ([]LaunchReceipt, error) {
	entries, err := os.ReadDir(s.launchDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runhub: read launches dir: %w", err)
	}
	var receipts []LaunchReceipt
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(s.launchDir(), e.Name())
		var rec LaunchReceipt
		if _, err := s.readJSON(path, &rec); err != nil {
			return nil, err
		}
		if e.Name() != receiptFileName(rec.RequestID) {
			return nil, fmt.Errorf("runhub: corrupt launch receipt %s: filename does not match record id %q", path, rec.RequestID)
		}
		receipts = append(receipts, rec)
	}
	return receipts, nil
}

// ListEventReceipts returns every persisted event receipt.
func (s *Store) ListEventReceipts() ([]EventReceipt, error) {
	entries, err := os.ReadDir(s.inboxDir())
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("runhub: read inbox dir: %w", err)
	}
	var receipts []EventReceipt
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		path := filepath.Join(s.inboxDir(), e.Name())
		var rec EventReceipt
		if _, err := s.readJSON(path, &rec); err != nil {
			return nil, err
		}
		if e.Name() != receiptFileName(string(rec.EventID)) {
			return nil, fmt.Errorf("runhub: corrupt event receipt %s: filename does not match record id %q", path, rec.EventID)
		}
		receipts = append(receipts, rec)
	}
	return receipts, nil
}

func (s *Store) writeJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("runhub: marshal %s: %w", path, err)
	}
	return fileutil.AtomicWriteFile(path, data, 0o600)
}

func (s *Store) readJSON(path string, dst any) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("runhub: read %s: %w", path, err)
	}
	if err := json.Unmarshal(data, dst); err != nil {
		return false, fmt.Errorf("runhub: corrupt %s: %w", path, err)
	}
	return true, nil
}

// validateLaunchReceipt reports durable-content corruption in a reloaded launch
// receipt.
func validateLaunchReceipt(rec LaunchReceipt) error {
	if !validOpaqueID(rec.RequestID) {
		return fmt.Errorf("runhub: launch receipt has invalid request id %q", rec.RequestID)
	}
	if !validRunID(string(rec.RunID)) {
		return fmt.Errorf("runhub: launch receipt %q has invalid run id %q", rec.RequestID, rec.RunID)
	}
	if rec.Status != ReceiptAccepted {
		return fmt.Errorf("runhub: launch receipt %q has invalid status %q", rec.RequestID, rec.Status)
	}
	if err := ValidateLaunchIntent(rec.Intent); err != nil {
		return fmt.Errorf("runhub: launch receipt %q has invalid intent: %w", rec.RequestID, err)
	}
	if rec.Intent.RequestID != rec.RequestID {
		return fmt.Errorf("runhub: launch receipt %q intent request id %q diverges", rec.RequestID, rec.Intent.RequestID)
	}
	if deriveRunID(rec.RequestID) != rec.RunID {
		return fmt.Errorf("runhub: launch receipt %q run id %q does not match derived id", rec.RequestID, rec.RunID)
	}
	return nil
}

// validateEventReceipt reports durable-content corruption in a reloaded event
// receipt, including a nested event whose id or run diverges from the receipt.
func validateEventReceipt(rec EventReceipt) error {
	if !validOpaqueID(string(rec.EventID)) {
		return fmt.Errorf("runhub: event receipt has invalid event id %q", rec.EventID)
	}
	if !validRunID(string(rec.RunID)) {
		return fmt.Errorf("runhub: event receipt %q has invalid run id %q", rec.EventID, rec.RunID)
	}
	if rec.Status != ReceiptAccepted && rec.Status != ReceiptStale && rec.Status != ReceiptInvalid {
		return fmt.Errorf("runhub: event receipt %q has invalid status %q", rec.EventID, rec.Status)
	}
	if rec.Revision == 0 {
		return fmt.Errorf("runhub: event receipt %q has zero revision", rec.EventID)
	}
	if err := ValidateEvent(rec.Event); err != nil {
		return fmt.Errorf("runhub: event receipt %q has invalid event: %w", rec.EventID, err)
	}
	if rec.Event.EventID != rec.EventID {
		return fmt.Errorf("runhub: event receipt %q nested event id %q diverges", rec.EventID, rec.Event.EventID)
	}
	if rec.Event.RunID != rec.RunID {
		return fmt.Errorf("runhub: event receipt %q nested event run %q diverges from receipt run %q", rec.EventID, rec.Event.RunID, rec.RunID)
	}
	return nil
}
