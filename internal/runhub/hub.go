package runhub

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

// Hub is the single-writer coordinator behind RunHub. It owns the durable
// store, an in-memory index of runs and receipts, and the change subscription
// fan-out. All mutations flow through Launch/Report under one mutex, and every
// run state change flows through Reduce.
type Hub struct {
	store *Store

	mu       sync.Mutex
	runs     map[RunID]AgentRun
	launches map[string]LaunchReceipt
	events   map[EventID]EventReceipt

	subs    map[uint64]chan Change
	nextSub uint64
}

// New opens the durable store under dir, reloads runs and receipts, and
// reconciles any write that was interrupted between its event-receipt claim and
// its run snapshot. Corrupt or inconsistent durable input is returned as an
// error, never silently skipped.
func New(dir string) (*Hub, error) {
	store, err := Open(dir)
	if err != nil {
		return nil, err
	}
	h := &Hub{
		store:    store,
		runs:     make(map[RunID]AgentRun),
		launches: make(map[string]LaunchReceipt),
		events:   make(map[EventID]EventReceipt),
		subs:     make(map[uint64]chan Change),
	}
	if err := h.reload(); err != nil {
		return nil, err
	}
	return h, nil
}

// reload rebuilds the in-memory index from durable state and replays any
// accepted event whose receipt was persisted before its run snapshot.
func (h *Hub) reload() error {
	runs, err := h.store.ListRuns()
	if err != nil {
		return err
	}
	for _, r := range runs {
		if err := r.validate(); err != nil {
			return err
		}
		h.runs[r.ID] = r
	}

	launches, err := h.store.ListLaunchReceipts()
	if err != nil {
		return err
	}
	for _, rec := range launches {
		if err := validateLaunchReceipt(rec); err != nil {
			return err
		}
		if _, ok := h.runs[rec.RunID]; !ok {
			return fmt.Errorf("runhub: launch receipt %q references missing run %q", rec.RequestID, rec.RunID)
		}
		h.launches[rec.RequestID] = rec
	}

	events, err := h.store.ListEventReceipts()
	if err != nil {
		return err
	}
	for _, rec := range events {
		if err := validateEventReceipt(rec); err != nil {
			return err
		}
		if _, ok := h.runs[rec.RunID]; !ok {
			return fmt.Errorf("runhub: event receipt %q references missing run %q", rec.EventID, rec.RunID)
		}
		h.events[rec.EventID] = rec
	}
	return h.reconcile()
}

// reconcile replays accepted receipts that have not been materialized into the
// run snapshot, rebuilding revision and terminal state after a crash. A receipt
// whose replay does not land on its recorded revision is inconsistent and fails.
func (h *Hub) reconcile() error {
	for id, run := range h.runs {
		var accepted []EventReceipt
		for _, rec := range h.events {
			if rec.RunID == id && rec.Status == ReceiptAccepted {
				accepted = append(accepted, rec)
			}
		}
		sort.Slice(accepted, func(i, j int) bool { return accepted[i].Revision < accepted[j].Revision })

		next := run
		canonical := make([]RunEvent, 0, len(accepted))
		for i, rec := range accepted {
			wantRevision := uint64(i + 2) // Launch creates revision 1.
			if rec.Revision != wantRevision {
				return fmt.Errorf("runhub: accepted event %q has revision %d, want %d", rec.EventID, rec.Revision, wantRevision)
			}
			canonical = append(canonical, rec.Event)
			if rec.Revision <= next.Revision {
				continue
			}
			applied, err := Reduce(next, rec.Event)
			if err != nil {
				return fmt.Errorf("runhub: replay event %q for run %q: %w", rec.EventID, id, err)
			}
			if applied.Revision != rec.Revision {
				return fmt.Errorf("runhub: event receipt %q revision %d does not match replay %d", rec.EventID, rec.Revision, applied.Revision)
			}
			next = applied
		}
		if next.Revision != uint64(len(canonical)+1) {
			return fmt.Errorf("runhub: run %q revision %d does not match %d accepted events", id, next.Revision, len(canonical))
		}
		if next != run {
			if err := h.store.SaveRun(next); err != nil {
				return fmt.Errorf("runhub: reconcile run %q: %w", id, err)
			}
			h.runs[id] = next
		}
		if err := h.store.RepairEventLog(id, canonical); err != nil {
			return fmt.Errorf("runhub: reconcile event log %q: %w", id, err)
		}
	}
	return nil
}

// Launch idempotently creates one managed run for intent.RequestID. The run id
// is derived from the request id, so the same request id always yields the same
// run and never a second launch record, even across restarts.
func (h *Hub) Launch(intent LaunchIntent) (Receipt, AgentRun) {
	if err := ValidateLaunchIntent(intent); err != nil {
		return Receipt{Status: ReceiptInvalid, Message: err.Error()}, AgentRun{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	id := deriveRunID(intent.RequestID)
	if run, ok := h.runs[id]; ok {
		if err := h.ensureLaunchReceipt(intent, run); err != nil {
			return Receipt{Status: ReceiptRetryable, RunID: id, Message: err.Error()}, AgentRun{}
		}
		return Receipt{Status: ReceiptAlreadyApplied, RunID: id, Revision: run.Revision}, run
	}

	now := time.Now()
	run := AgentRun{
		ID:         id,
		Source:     intent.Source,
		Ownership:  OwnershipManaged,
		Workspace:  intent.Workspace,
		State:      StateQueued,
		Activity:   ActivityIdle,
		Revision:   1,
		LastSeenAt: now,
		CreatedAt:  now,
		UpdatedAt:  now,
	}
	if err := h.store.SaveRun(run); err != nil {
		return Receipt{Status: ReceiptRetryable, RunID: id, Message: err.Error()}, AgentRun{}
	}

	rec := LaunchReceipt{
		RequestID: intent.RequestID,
		RunID:     id,
		Status:    ReceiptAccepted,
		Intent:    intent,
		CreatedAt: now,
	}
	if err := h.store.SaveLaunchReceipt(intent.RequestID, rec); err != nil {
		return Receipt{Status: ReceiptRetryable, RunID: id, Message: err.Error()}, AgentRun{}
	}

	h.runs[id] = run
	h.launches[intent.RequestID] = rec
	h.broadcast(ChangeCreated, run)
	return Receipt{Status: ReceiptAccepted, RunID: id, Revision: run.Revision}, run
}

// ensureLaunchReceipt backfills a launch receipt if a prior launch wrote the run
// snapshot but crashed before writing its receipt. It runs with the lock held
// and returns any persistence failure so the caller can surface a retryable
// error instead of a false already-applied success.
func (h *Hub) ensureLaunchReceipt(intent LaunchIntent, run AgentRun) error {
	if _, ok := h.launches[intent.RequestID]; ok {
		return nil
	}
	rec := LaunchReceipt{
		RequestID: intent.RequestID,
		RunID:     run.ID,
		Status:    ReceiptAccepted,
		Intent:    intent,
		CreatedAt: run.CreatedAt,
	}
	if err := h.store.SaveLaunchReceipt(intent.RequestID, rec); err != nil {
		return err
	}
	h.launches[intent.RequestID] = rec
	return nil
}

// Report idempotently reduces one event. The same event id returns the same
// verdict without re-reducing; terminal runs reject later events as stale.
func (h *Hub) Report(evt RunEvent) (Receipt, AgentRun) {
	if err := ValidateEvent(evt); err != nil {
		return Receipt{Status: ReceiptInvalid, EventID: evt.EventID, Message: err.Error()}, AgentRun{}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if rec, ok := h.events[evt.EventID]; ok {
		run := h.runs[rec.RunID]
		status := rec.Status
		if status == ReceiptAccepted {
			if err := h.materializeRun(rec.RunID); err != nil {
				return Receipt{Status: ReceiptRetryable, RunID: rec.RunID, EventID: evt.EventID, Revision: rec.Revision, Message: err.Error()}, run
			}
			status = ReceiptAlreadyApplied
		}
		return Receipt{
			Status:   status,
			RunID:    rec.RunID,
			EventID:  evt.EventID,
			Revision: rec.Revision,
		}, run
	}

	run, ok := h.runs[evt.RunID]
	if !ok {
		return Receipt{
			Status:  ReceiptInvalid,
			RunID:   evt.RunID,
			EventID: evt.EventID,
			Message: "unknown run",
		}, AgentRun{}
	}

	next, rerr := Reduce(run, evt)
	if rerr != nil {
		var te *TransitionError
		if !errors.As(rerr, &te) {
			return Receipt{Status: ReceiptRetryable, RunID: evt.RunID, EventID: evt.EventID, Message: rerr.Error()}, run
		}
		return h.recordRejected(evt, run, te)
	}

	// Claim the event id first so a crash after this point never re-applies the
	// event on redelivery; reconcile() materializes the run snapshot on reload.
	rec := EventReceipt{
		EventID:   evt.EventID,
		RunID:     evt.RunID,
		Status:    ReceiptAccepted,
		Revision:  next.Revision,
		AppliedAt: time.Now(),
		Event:     evt,
	}
	if err := h.store.SaveEventReceipt(evt.EventID, rec); err != nil {
		return Receipt{Status: ReceiptRetryable, RunID: evt.RunID, EventID: evt.EventID, Message: err.Error()}, run
	}
	// The durable receipt is the commit point. Advance memory immediately so a
	// materialization failure cannot let a later event reuse the same revision.
	// A duplicate delivery retries snapshot/log materialization without reducing.
	h.runs[evt.RunID] = next
	h.events[evt.EventID] = rec
	h.broadcast(ChangeUpdated, next)
	if err := h.materializeRun(evt.RunID); err != nil {
		return Receipt{Status: ReceiptRetryable, RunID: evt.RunID, EventID: evt.EventID, Revision: next.Revision, Message: err.Error()}, next
	}
	return Receipt{Status: ReceiptAccepted, RunID: evt.RunID, EventID: evt.EventID, Revision: next.Revision}, next
}

// materializeRun writes the current snapshot and repairs the append-only event
// log from accepted receipts. It runs with h.mu held.
func (h *Hub) materializeRun(id RunID) error {
	run, ok := h.runs[id]
	if !ok {
		return fmt.Errorf("runhub: materialize unknown run %q", id)
	}
	canonical, err := h.acceptedEvents(id, run.Revision)
	if err != nil {
		return err
	}
	if err := h.store.SaveRun(run); err != nil {
		return err
	}
	return h.store.RepairEventLog(id, canonical)
}

// acceptedEvents returns the authoritative applied event sequence for one run.
func (h *Hub) acceptedEvents(id RunID, revision uint64) ([]RunEvent, error) {
	var receipts []EventReceipt
	for _, rec := range h.events {
		if rec.RunID == id && rec.Status == ReceiptAccepted {
			receipts = append(receipts, rec)
		}
	}
	sort.Slice(receipts, func(i, j int) bool { return receipts[i].Revision < receipts[j].Revision })
	canonical := make([]RunEvent, 0, len(receipts))
	for i, rec := range receipts {
		want := uint64(i + 2)
		if rec.Revision != want {
			return nil, fmt.Errorf("runhub: accepted event %q has revision %d, want %d", rec.EventID, rec.Revision, want)
		}
		canonical = append(canonical, rec.Event)
	}
	if revision != uint64(len(canonical)+1) {
		return nil, fmt.Errorf("runhub: run %q revision %d does not match %d accepted events", id, revision, len(canonical))
	}
	return canonical, nil
}

// recordRejected persists a stale/invalid verdict so a redelivery returns the
// same outcome deterministically, without mutating the run.
func (h *Hub) recordRejected(evt RunEvent, run AgentRun, te *TransitionError) (Receipt, AgentRun) {
	status := ReceiptStale
	if te.Code == TransitionInvalid {
		status = ReceiptInvalid
	}
	rec := EventReceipt{
		EventID:  evt.EventID,
		RunID:    evt.RunID,
		Status:   status,
		Revision: run.Revision,
		Event:    evt,
	}
	if err := h.store.SaveEventReceipt(evt.EventID, rec); err != nil {
		return Receipt{Status: ReceiptRetryable, RunID: evt.RunID, EventID: evt.EventID, Message: err.Error()}, run
	}
	h.events[evt.EventID] = rec
	return Receipt{
		Status:   status,
		RunID:    evt.RunID,
		EventID:  evt.EventID,
		Revision: run.Revision,
		Message:  te.Msg,
	}, run
}

// Get returns the current run snapshot, if present.
func (h *Hub) Get(id RunID) (AgentRun, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	r, ok := h.runs[id]
	return r, ok
}

// List returns runs matching filter, ordered by CreatedAt.
func (h *Hub) List(f Filter) []RunProjection {
	h.mu.Lock()
	defer h.mu.Unlock()

	var out []RunProjection
	for _, r := range h.runs {
		if f.Source != "" && r.Source != f.Source {
			continue
		}
		if f.Ownership != "" && r.Ownership != f.Ownership {
			continue
		}
		if f.State != "" && r.State != f.State {
			continue
		}
		if f.Active && r.State.IsTerminal() {
			continue
		}
		out = append(out, r.Projection())
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAt.Before(out[j].CreatedAt)
	})
	return out
}

// Subscribe returns a change channel and a cancel func. Notifications are
// dropped (never blocking) for a consumer that stops draining; consumers can
// always re-List to recover the full projection.
func (h *Hub) Subscribe() (<-chan Change, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ch := make(chan Change, 64)
	id := h.nextSub
	h.nextSub++
	h.subs[id] = ch
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			h.mu.Lock()
			defer h.mu.Unlock()
			if c, ok := h.subs[id]; ok {
				close(c)
				delete(h.subs, id)
			}
		})
	}
	return ch, cancel
}

// broadcast fans a change out to subscribers. It must be called with the lock
// held; slow subscribers are skipped rather than stalling the writer.
func (h *Hub) broadcast(kind ChangeKind, run AgentRun) {
	c := Change{Kind: kind, Run: run.Projection()}
	for _, ch := range h.subs {
		select {
		case ch <- c:
		default:
		}
	}
}

// deriveRunID maps a launch request id to a stable run id, making Launch
// structurally idempotent without relying on a separate claim file.
func deriveRunID(requestID string) RunID {
	sum := sha256.Sum256([]byte(requestID))
	return RunID("run_" + hex.EncodeToString(sum[:16]))
}
