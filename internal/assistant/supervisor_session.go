package assistant

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// SupervisorSessionRef is the stable identity of one Assistant's supervisor
// Session. The Path is the durable session file, so a host can restore the
// live Controller from it after a restart.
type SupervisorSessionRef struct {
	ID   string
	Path string
}

// SupervisorTurnOutcome is what the host observed after submitting one real
// supervisor turn on the supervisor Session's Controller. It is the bounded
// execution result the executor routes on.
type SupervisorTurnOutcome struct {
	// Text is the final assistant message of the turn (the decision JSON).
	Text string
	// Pending is true when the turn ended waiting on a PendingInteraction (the
	// supervisor asked the user a question through the ask tool).
	Pending bool
	// Running is true when the turn was still in flight when the budget
	// elapsed. The executor leaves trigger events pending and re-checks on the
	// next tick instead of double-submitting.
	Running bool
	// ToolNames lists the tool calls the supervisor made this turn. They are
	// part of the Session history (checkpointed with it).
	ToolNames []string
	// HistoryLen is the durable Session transcript length the host observed.
	// For a Running outcome it is the length right before the submission (the
	// checkpoint baseline); for a settled outcome it is the post-turn length.
	// The executor uses it to confirm a checkpointed turn durably produced
	// output before re-submitting.
	HistoryLen int
	// Err is a turn-level failure (model error, host restore failure). A
	// failure keeps trigger events pending so the turn is retried.
	Err error
}

// ManagedSessionSummary is the bounded view of one Purpose=managed Session the
// host derives from the Session subsystem. Status is the derived lifecycle as
// a string ("running", "waiting", "completed", ...) so the executor stays
// decoupled from the agent package while the Session subsystem stays the
// single source of truth.
type ManagedSessionSummary struct {
	ID      string
	Path    string
	Title   string
	Preview string
	Status  string
	Turns   int
}

// SupervisorHost is the host capability that maps the durable supervisor
// Session to a live Controller. Desktop and daemon implement it with their own
// session machinery (tabs vs headless controllers); the executor only sees the
// stable Session and submits turns through it.
type SupervisorHost interface {
	// FindSupervisorSession locates the assistant's supervisor Session.
	FindSupervisorSession(assistantID string) (SupervisorSessionRef, bool)
	// EnsureSupervisorSession returns the assistant's unique supervisor
	// Session, creating it atomically when missing (a concurrent or replayed
	// call resolves to the same Session).
	EnsureSupervisorSession(a Assistant) (SupervisorSessionRef, error)
	// ManagedSessions lists the assistant's Purpose=managed Sessions with their
	// derived status, deduplicated by Session ID.
	ManagedSessions(assistantID string) []ManagedSessionSummary
	// SupervisorHistoryLen returns the durable transcript length of the
	// supervisor Session — how many messages are durably persisted in its
	// session file — as a read-only probe. The executor captures it as the
	// turn-checkpoint baseline BEFORE the submission, so a settle that shows
	// no growth proves the submission never durably landed even when the
	// Session already carries older history. A non-nil error means the durable
	// state could not be read; the executor then skips the turn instead of
	// degrading into an unsafe zero baseline.
	SupervisorHistoryLen(ref SupervisorSessionRef) (int, error)
	// RunSupervisorTurn runs one real Controller turn on the supervisor Session
	// with the given prompt, bounded by budget. The turn's model history, tool
	// calls, pending interaction and checkpoint all land in that Session file.
	RunSupervisorTurn(ref SupervisorSessionRef, prompt string, budget time.Duration) SupervisorTurnOutcome
	// SettleSupervisorTurn reads the current state of a previously submitted
	// supervisor turn WITHOUT submitting a new prompt: a still-running turn
	// reports Running, a pending interaction reports Pending, otherwise the
	// finished outcome is read from the durable Session transcript. It is the
	// restart-safe continuation of a checkpointed turn.
	SettleSupervisorTurn(ref SupervisorSessionRef) SupervisorTurnOutcome
}

// SupervisorExecutor is the shared, host-agnostic core of the supervisor loop:
// it guarantees the unique supervisor Session, collects durable events into the
// mergeable queue, and runs each supervisor reasoning turn through that
// Session's live Controller. Desktop and daemon both drive it; a restart
// resumes the same Session file and the same pending events.
type SupervisorExecutor struct {
	store  *Store
	events *SupervisorEventQueue
	host   SupervisorHost

	// turnMu serializes the submit decision so concurrent ticks (a wake from
	// user input racing the interval tick) or a second process can never
	// double-submit one supervisor turn: whoever wins saves the intent
	// checkpoint first, everyone else settles it. It is held only around the
	// checkpoint check + intent write, never across the model turn.
	turnMu sync.Mutex

	// control resolves the SessionControl used by the acting phase (advance
	// creates a managed Session, steer/answer target an existing one). It is a
	// hook so hosts can hand over their session adapter lazily and tests can
	// inject fakes after construction.
	control func() SessionControl
	ideator *Ideator
	// autoAnswer resolves the model-driven auto-answer for the answer action.
	autoAnswer func() *AutoAnswer
	// trialStatus resolves the experiment trial fork status.
	trialStatus func() TrialStatusResolver
	// constraints is an optional host hook returning the bounded summary and
	// revision of the authoritative project constraints for an assistant.
	constraints func(assistantID string) (summary string, revision int64)

	// viewport is an optional host hook for the current UI viewport.
	viewport func(now time.Time) (ViewportSnapshot, bool)
	// diagnostic is an optional host hook that observes failures.
	diagnostic func(operation string, err error)
	// wake is the host hook that requests an immediate loop pass when an event
	// is enqueued from outside the tick (user input).
	wake func()

	// turnBudget bounds one supervisor turn; running longer leaves the turn in
	// flight and returns (the loop re-checks on the next tick).
	turnBudget time.Duration
	// heartbeatInterval is how long an assistant may stay fully idle before an
	// EventHeartbeat re-evaluates the mission.
	heartbeatInterval time.Duration
	// experimentMaxAge bounds one isolated-trial race: a fork still running
	// past this age is timed out (terminal), so the winner comparison (or the
	// rollback-safe fallback) always settles and the original session is never
	// left pending forever.
	experimentMaxAge time.Duration
}

// SupervisorExecutorOptions carries the injectable dependencies of the
// executor. Only Store, Events and Host are required; Control/AutoAnswer/
// TrialStatus may be nil (the acting phase then degrades explicitly).
type SupervisorExecutorOptions struct {
	Store       *Store
	Events      *SupervisorEventQueue
	Host        SupervisorHost
	Control     func() SessionControl
	Ideator     *Ideator
	AutoAnswer  func() *AutoAnswer
	TrialStatus func() TrialStatusResolver
	Viewport    func(now time.Time) (ViewportSnapshot, bool)
	Diagnostic  func(operation string, err error)
	Wake        func()
	// Constraints optionally provides the bounded project-constraints summary
	// and its revision for the supervisor's implicit context.
	Constraints       func(assistantID string) (summary string, revision int64)
	TurnBudget        time.Duration
	HeartbeatInterval time.Duration
	// ExperimentMaxAge bounds one isolated-trial race (default 15 minutes):
	// still-running forks past this age count as timed out, so a race always
	// settles and the original session is never permanently pending.
	ExperimentMaxAge time.Duration
}

// NewSupervisorExecutor builds the shared supervisor executor.
func NewSupervisorExecutor(opts SupervisorExecutorOptions) (*SupervisorExecutor, error) {
	if opts.Store == nil {
		return nil, errors.New("assistant: supervisor executor requires a store")
	}
	if opts.Events == nil {
		return nil, errors.New("assistant: supervisor executor requires an event queue")
	}
	if opts.Host == nil {
		return nil, errors.New("assistant: supervisor executor requires a host")
	}
	if opts.TurnBudget <= 0 {
		opts.TurnBudget = 10 * time.Minute
	}
	if opts.HeartbeatInterval <= 0 {
		opts.HeartbeatInterval = 4 * time.Hour
	}
	if opts.ExperimentMaxAge <= 0 {
		opts.ExperimentMaxAge = defaultExperimentMaxAge
	}
	return &SupervisorExecutor{
		store: opts.Store, events: opts.Events, host: opts.Host,
		control: opts.Control, ideator: opts.Ideator, autoAnswer: opts.AutoAnswer,
		trialStatus: opts.TrialStatus, viewport: opts.Viewport,
		diagnostic: opts.Diagnostic, wake: opts.Wake, constraints: opts.Constraints,
		turnBudget: opts.TurnBudget, heartbeatInterval: opts.HeartbeatInterval,
		experimentMaxAge: opts.ExperimentMaxAge,
	}, nil
}

// SessionSummaries derives the running and failed managed-session summaries
// for the assistant's current execution view (used by the cycle next-step hint
// and hosts). It reads through the host's Session subsystem scan.
func (e *SupervisorExecutor) SessionSummaries(assistantID string) (running, failed []SupervisorSessionSummary) {
	return e.supervisorSessions(assistantID)
}

// Host returns the host the executor drives (used by host wiring and tests).
func (e *SupervisorExecutor) Host() SupervisorHost {
	if e == nil {
		return nil
	}
	return e.host
}

// Events returns the durable event queue (used by host wiring and tests).
func (e *SupervisorExecutor) Events() *SupervisorEventQueue {
	if e == nil {
		return nil
	}
	return e.events
}

// sessionControl resolves the acting-phase SessionControl hook.
func (e *SupervisorExecutor) sessionControl() SessionControl {
	if e == nil || e.control == nil {
		return nil
	}
	return e.control()
}

// resolveAutoAnswer resolves the auto-answer model hook.
func (e *SupervisorExecutor) resolveAutoAnswer() *AutoAnswer {
	if e == nil || e.autoAnswer == nil {
		return nil
	}
	return e.autoAnswer()
}

// trialStatusResolver resolves the experiment trial status hook.
func (e *SupervisorExecutor) trialStatusResolver() TrialStatusResolver {
	if e == nil || e.trialStatus == nil {
		return nil
	}
	return e.trialStatus()
}

// eventID builds a stable event id for dedup. Replaying the same underlying
// action (same key) with the same context produces the same id.
func eventID(kind SupervisorEventKind, assistantID, key string) string {
	return StableID("event", string(kind)+"/"+assistantID+"/"+key)
}

// eventBatchID derives the stable identity of one pending-event batch. It is
// the durable link between the turn's trigger events and the routed
// decision/action receipts: the same batch always yields the same ID, so a
// replay after a crash resolves the batch and settles it instead of re-running
// the model turn. An empty batch (a turn triggered by an expansion trigger or
// attention, not by events) yields "" — nothing to consume, nothing to replay.
func eventBatchID(events []SupervisorEvent) string {
	if len(events) == 0 {
		return ""
	}
	ids := make([]string, 0, len(events))
	for _, ev := range events {
		ids = append(ids, ev.ID)
	}
	sort.Strings(ids)
	return StableID("event-batch", strings.Join(ids, "\x00"))
}

func (e *SupervisorExecutor) recordDiagnostic(operation string, err error) {
	if e == nil || err == nil {
		return
	}
	if e.diagnostic != nil {
		e.diagnostic(operation, err)
	}
}

// enqueue records one event durably and wakes the loop when it was newly
// recorded. Errors are returned, never swallowed: a user input lost to an IO
// failure must surface to the caller, and a lifecycle diff failure keeps the
// high-water state un-advanced so the next tick re-enqueues (and dedups)
// instead of losing the transition.
func (e *SupervisorExecutor) enqueue(ev SupervisorEvent) error {
	applied, err := e.events.Enqueue(ev)
	if err != nil {
		e.recordDiagnostic("supervisor_event", err)
		return err
	}
	if applied && e.wake != nil {
		e.wake()
	}
	return nil
}

// EnsureSupervisorSessions gives every active assistant exactly one durable
// Purpose=supervisor Session. Uniqueness lives in the Session subsystem: the
// host creates it atomically (deterministic file + receipt), so concurrent
// ticks or a crash between find and create can never produce a second Session.
func (e *SupervisorExecutor) EnsureSupervisorSessions(assistants []Assistant) {
	for i := range assistants {
		a := assistants[i]
		if a.Lifecycle != LifecycleActive {
			continue
		}
		if _, ok := e.host.FindSupervisorSession(a.ID); ok {
			continue
		}
		if _, err := e.host.EnsureSupervisorSession(a); err != nil {
			e.recordDiagnostic("supervisor_create", fmt.Errorf("assistant %s: %w", a.ID, err))
		}
	}
}

// EnqueueUserInput durably records a user-input event for one assistant and
// wakes the supervisor loop. Hosts call it right after a Dispatch is opened so
// the supervisor observes the input even when it does not create a managed
// Session (questions, feedback, control intents). The real enqueue error is
// returned; a user input that failed to persist must never look successful.
func (e *SupervisorExecutor) EnqueueUserInput(assistantID, requestID, input string) error {
	if e == nil || e.events == nil {
		return errors.New("assistant: supervisor executor is unavailable")
	}
	return e.enqueue(SupervisorEvent{
		ID:          eventID(EventUserInput, assistantID, requestID),
		Kind:        EventUserInput,
		AssistantID: assistantID,
		RequestID:   requestID,
		Payload:     truncateString(input, 256),
		At:          time.Now(),
	})
}

// EnqueueRoutineFires durably records routine_due events for the fires the
// scheduler produced and wakes the loop. The fires themselves still become
// managed Sessions through the host; the event makes the wake explicit. All
// per-fire enqueue errors are returned (joined), never silently dropped.
func (e *SupervisorExecutor) EnqueueRoutineFires(fires []RoutineFire, now time.Time) error {
	if e == nil || e.events == nil {
		return errors.New("assistant: supervisor executor is unavailable")
	}
	var errs []error
	for _, fire := range fires {
		if err := e.enqueue(SupervisorEvent{
			ID:          eventID(EventRoutineDue, fire.AssistantID, fire.FireID),
			Kind:        EventRoutineDue,
			AssistantID: fire.AssistantID,
			RequestID:   fire.FireID,
			Payload:     fire.FireID,
			At:          now,
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// CollectSupervisorEvents diffs the Session subsystem and the store against the
// durable high-water state and enqueues every real transition: managed Session
// lifecycle events, deferred-decision retries, and the idle heartbeat. The
// state sidecar only advances after enqueue, so a crash between the two
// re-enqueues (and dedups) the same transitions instead of losing them.
func (e *SupervisorExecutor) CollectSupervisorEvents(now time.Time) int {
	assistants, err := e.store.List()
	if err != nil {
		e.recordDiagnostic("supervisor_event_list", err)
		return 0
	}
	enqueued := 0
	for _, a := range assistants {
		if a.Lifecycle != LifecycleActive {
			continue
		}
		n := e.collectEventsFor(a, now)
		enqueued += n
	}
	return enqueued
}

func (e *SupervisorExecutor) collectEventsFor(a Assistant, now time.Time) int {
	enqueued := 0
	state, err := e.events.LoadState(a.ID)
	if err != nil {
		e.recordDiagnostic("supervisor_event_state", err)
		return 0
	}
	if state.SessionStatus == nil {
		state.SessionStatus = map[string]string{}
	}
	if state.SessionTurns == nil {
		state.SessionTurns = map[string]int{}
	}
	sessions := e.managedSessions(a.ID)
	nextStatus := map[string]string{}
	nextTurns := map[string]int{}
	for _, s := range sessions {
		id := s.ID
		status := s.Status
		turns := s.Turns
		nextStatus[id] = status
		nextTurns[id] = turns
		prev := state.SessionStatus[id]
		if status == prev {
			// Same state: still waiting on the user (no new event) or the same
			// running turn. Progress is observed through the turn counter.
			if status == "running" && turns > state.SessionTurns[id] {
				if e.enqueueLifecycle(EventSessionProgressed, a.ID, id, turns, state.SessionTurns[id]) {
					enqueued++
				}
			}
			continue
		}
		switch status {
		case "running":
			kind := EventSessionProgressed
			if prev == "" || prev == "idle" || prev == "queued" {
				kind = EventSessionStarted
			}
			if e.enqueueLifecycle(kind, a.ID, id, turns, 0) {
				enqueued++
			}
		case "waiting":
			if prev == "" || prev == "idle" || prev == "queued" {
				if e.enqueueLifecycle(EventSessionStarted, a.ID, id, turns, 0) {
					enqueued++
				}
			} else if e.enqueueLifecycle(EventSessionProgressed, a.ID, id, turns, 0) {
				enqueued++
			}
			if e.interactionStillOpen(a.ID, id) && e.enqueueLifecycle(EventInteractionRequired, a.ID, id, turns, 0) {
				enqueued++
			}
		case "completed":
			if e.enqueueLifecycle(EventSessionCompleted, a.ID, id, turns, 0) {
				enqueued++
			}
		case "failed":
			if e.enqueueLifecycle(EventSessionFailed, a.ID, id, turns, 0) {
				enqueued++
			}
		case "cancelled":
			if e.enqueueLifecycle(EventSessionCancelled, a.ID, id, turns, 0) {
				enqueued++
			}
		default:
			// queued/idle: nothing actionable yet.
		}
	}
	// Deferred decisions whose deadline passed wake the supervisor for a retry.
	for _, s := range sessions {
		id := s.ID
		items, err := e.pendingInteractions(id)
		if err != nil {
			continue
		}
		for _, item := range items {
			if item.Kind != "ask" || strings.TrimSpace(item.ID) == "" {
				continue
			}
			prior, has, err := e.store.LatestDecision(a.ID, id, item.ID)
			if err != nil || !has || prior.Source != DecisionDeferred || prior.DueAt.IsZero() {
				continue
			}
			if now.Before(prior.DueAt) {
				continue
			}
			ev := SupervisorEvent{
				ID:          eventID(EventRetryDue, a.ID, id+"/"+item.ID),
				Kind:        EventRetryDue,
				AssistantID: a.ID,
				SessionID:   id,
				RequestID:   "retry/" + id + "/" + item.ID,
				Payload:     item.ID,
				Revision:    int64(s.Turns),
				At:          now,
			}
			if applied, err := e.events.Enqueue(ev); err != nil {
				e.recordDiagnostic("supervisor_event", err)
			} else if applied {
				enqueued++
			}
		}
	}
	// Heartbeat: long idle with nothing pending re-evaluates the mission. The
	// event ID is a stable watermark (not a per-tick timestamp) and the event
	// is Requeue, so a SaveState failure between enqueue and the state sidecar
	// can never mint a fresh event ID every tick (flood): the pending copy
	// dedups the next collect, and after a consume the same ID fires again.
	if e.heartbeatDue(a.ID, now) {
		ev := SupervisorEvent{
			ID:          eventID(EventHeartbeat, a.ID, "idle"),
			Kind:        EventHeartbeat,
			AssistantID: a.ID,
			Requeue:     true,
			At:          now,
		}
		if applied, err := e.events.Enqueue(ev); err != nil {
			e.recordDiagnostic("supervisor_event", err)
		} else if applied {
			enqueued++
			state.HeartbeatAt = now
		}
	}
	state.SessionStatus = nextStatus
	state.SessionTurns = nextTurns
	if err := e.events.SaveState(a.ID, state); err != nil {
		e.recordDiagnostic("supervisor_event_state", err)
	}
	return enqueued
}

// enqueueLifecycle records one session lifecycle event with a monotonic
// revision context (the session's turn count), merging away duplicates.
func (e *SupervisorExecutor) enqueueLifecycle(kind SupervisorEventKind, assistantID, sessionID string, revision, prevRevision int) bool {
	ev := SupervisorEvent{
		ID:          eventID(kind, assistantID, sessionID+"/"+fmt.Sprint(revision)),
		Kind:        kind,
		AssistantID: assistantID,
		SessionID:   sessionID,
		Revision:    int64(revision),
		At:          time.Now(),
	}
	if prevRevision > 0 {
		ev.Payload = fmt.Sprintf("progress %d -> %d", prevRevision, revision)
	}
	applied, err := e.events.Enqueue(ev)
	if err != nil {
		e.recordDiagnostic("supervisor_event", err)
		return false
	}
	return applied
}

// interactionStillOpen reports whether a session's pending interaction has no
// terminal decision yet, so interaction_required is not re-enqueued forever
// while a hard gate waits for the user.
func (e *SupervisorExecutor) interactionStillOpen(assistantID, sessionID string) bool {
	items, err := e.pendingInteractions(sessionID)
	if err != nil || len(items) == 0 {
		return false
	}
	for _, item := range items {
		if item.Kind != "ask" || strings.TrimSpace(item.ID) == "" {
			continue
		}
		prior, has, err := e.store.LatestDecision(assistantID, sessionID, item.ID)
		if err != nil || !has || prior.Source == DecisionDeferred {
			return true
		}
	}
	return false
}

// heartbeatDue reports whether the assistant has been fully idle long enough
// and has nothing else pending.
func (e *SupervisorExecutor) heartbeatDue(assistantID string, now time.Time) bool {
	state, err := e.events.LoadState(assistantID)
	if err != nil {
		return false
	}
	if !state.HeartbeatAt.IsZero() && now.Sub(state.HeartbeatAt) < e.heartbeatInterval {
		return false
	}
	hasPending, err := e.events.HasPending(assistantID)
	if err != nil || hasPending {
		return false
	}
	snapshot, err := e.store.Get(assistantID)
	if err != nil {
		return false
	}
	return len(snapshot.Attention) == 0
}

// pendingInteractions reads one session's pending asks through the host's
// SessionControl when the controller is live; a nil control or a not-running
// session yields nothing.
func (e *SupervisorExecutor) pendingInteractions(sessionID string) ([]SessionInteraction, error) {
	control := e.sessionControl()
	if control == nil {
		return nil, nil
	}
	return control.PendingInteractions(sessionID)
}

// managedSessions lists the assistant's Purpose=managed Sessions through the
// host (deduplicated there), oldest activity first.
func (e *SupervisorExecutor) managedSessions(assistantID string) []ManagedSessionSummary {
	out := e.host.ManagedSessions(assistantID)
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

// supervisorSessions derives the running and failed managed-session summaries
// for the supervisor's implicit context from the Session subsystem.
func (e *SupervisorExecutor) supervisorSessions(assistantID string) (running, failed []SupervisorSessionSummary) {
	for _, s := range e.host.ManagedSessions(assistantID) {
		summary := SupervisorSessionSummary{
			ID: s.ID, Title: s.Title, Status: s.Status,
			Purpose: "managed", Preview: s.Preview,
		}
		switch s.Status {
		case "running", "waiting":
			running = append(running, summary)
		case "failed":
			failed = append(failed, summary)
		}
	}
	return running, failed
}

// nextStep derives the durable next-step hint for the cycle checkpoint from the
// plan plus the Session-derived execution state.
func (e *SupervisorExecutor) nextStep(snapshot Snapshot, running, failed int, now time.Time) string {
	if triggers := EvaluateExpansion(snapshot, now, ExpansionLive{Running: running, Failed: failed}); len(triggers) > 0 {
		return "expand:" + string(triggers[0])
	}
	executable := 0
	for _, resp := range snapshot.Plan.Responsibilities {
		if resp.Status == RespReady || resp.Status == RespActive {
			executable++
		}
	}
	return fmt.Sprintf("advance %d executable responsibilities (%d running, %d failed sessions)", executable, running, failed)
}

// RunTurns runs one real supervisor reasoning turn per active assistant that
// has a decision-worthy signal (pending events, expansion trigger, or pending
// attention). The turn executes through the assistant's supervisor Session
// Controller; the resulting decision is parsed from the Session's final
// message and routed. Events are consumed only after a successfully routed
// turn, so a failure keeps them pending for the next tick. A checkpointed
// (previously submitted, still in flight) turn is settled instead of
// re-submitted.
func (e *SupervisorExecutor) RunTurns(ctx context.Context, now time.Time) {
	assistants, err := e.store.List()
	if err != nil {
		e.recordDiagnostic("supervisor_list", err)
		return
	}
	for _, a := range assistants {
		if a.Lifecycle != LifecycleActive {
			continue
		}
		e.runTurnFor(ctx, a, now)
	}
}

func (e *SupervisorExecutor) runTurnFor(ctx context.Context, a Assistant, now time.Time) {
	// turnMu serializes the submit decision: whoever saves the intent
	// checkpoint first owns the turn; everyone else settles it.
	e.turnMu.Lock()

	// Restart/overlap recovery: a checkpointed turn is settled, never
	// re-submitted, so a turn that outlived its budget (or a crash mid-turn)
	// can never run a second model round.
	if cp, ok, err := e.events.LoadTurnCheckpoint(a.ID); err != nil {
		e.recordDiagnostic("supervisor_turn_checkpoint", err)
		e.turnMu.Unlock()
		return
	} else if ok {
		e.turnMu.Unlock()
		e.settleCheckpointedTurn(a, cp, now)
		return
	}

	snapshot, err := e.store.Get(a.ID)
	if err != nil {
		e.turnMu.Unlock()
		return
	}
	running, failed := e.supervisorSessions(a.ID)
	triggers := EvaluateExpansion(snapshot, now, ExpansionLive{Running: len(running), Failed: len(failed)})
	events, err := e.events.Pending(a.ID)
	if err != nil {
		e.recordDiagnostic("supervisor_events", err)
		e.turnMu.Unlock()
		return
	}
	// Bounded expansion backoff: if the only wake-up is an expansion trigger and
	// a recent expansion pass set a backoff, skip the model turn (the loop would
	// otherwise spin on every tick with nothing new to adopt).
	if len(events) == 0 && len(snapshot.Attention) == 0 && len(triggers) > 0 {
		due, _, dueErr := e.store.ExpansionDue(a.ID, now)
		if dueErr != nil {
			e.recordDiagnostic("supervisor_expansion_due", dueErr)
		} else if !due {
			e.turnMu.Unlock()
			return
		}
	}
	if len(triggers) == 0 && len(events) == 0 && len(snapshot.Attention) == 0 {
		e.turnMu.Unlock()
		return
	}

	ref, ok := e.host.FindSupervisorSession(a.ID)
	if !ok {
		// Atomic fallback: the tick's EnsureSupervisorSessions should have
		// created it; a race between ensure and here re-creates atomically.
		var ensureErr error
		ref, ensureErr = e.host.EnsureSupervisorSession(a)
		if ensureErr != nil {
			e.recordDiagnostic("supervisor_ensure", ensureErr)
			e.turnMu.Unlock()
			return
		}
	}

	// Ensure a current cycle exists (production ticks open one per tick; the
	// executor opens it when none is open yet, so the acting phase always has a
	// fence to complete under).
	if cycle, ok := e.store.LatestCycle(a.ID); !ok || cycle.State == CycleCompleted {
		wc, wcErr := e.store.WorkControl()
		if wcErr != nil {
			e.recordDiagnostic("cycle_workcontrol", wcErr)
			e.turnMu.Unlock()
			return
		}
		if _, err := e.store.OpenCycle(OpenCycleInput{
			AssistantID: a.ID,
			RequestID:   StableID("request", fmt.Sprintf("cycle/%s/%d", a.ID, now.UnixNano())),
			Observed: CycleObservation{
				PlanRevision:      snapshot.Plan.Revision,
				AssistantRevision: snapshot.Assistant.Revision,
				MemoryRevision:    snapshot.Memory.Revision,
				WorkEpoch:         wc.Epoch,
			},
			Now: now,
		}); err != nil {
			e.recordDiagnostic("cycle_open", err)
			e.turnMu.Unlock()
			return
		}
	}

	// Replay guard: this exact event batch was already routed (a crash between
	// the action and MarkConsumed). The batch receipt settles it as
	// already_applied: consume the events, complete the cycle, and never run
	// the model turn or the external action again.
	batchID := eventBatchID(events)
	if batchID != "" {
		if rec, ok, err := e.events.LoadBatchReceipt(a.ID, batchID); err == nil && ok && rec.Outcome.ConsumesEvents() {
			e.turnMu.Unlock()
			e.consumeAndComplete(a, rec.EventIDs, now)
			return
		}
	}

	eventIDs := make([]string, 0, len(events))
	for _, ev := range events {
		eventIDs = append(eventIDs, ev.ID)
	}
	// Durable history baseline captured BEFORE the intent is claimed. The
	// settle logic decides "submitted or not" by comparing the settled
	// transcript length against this baseline, so it must be the length right
	// before this submission — never the zero value on a Session that already
	// carries older history. A failed probe is retryable: skip this tick
	// without a checkpoint or a submit.
	baseline, err := e.host.SupervisorHistoryLen(ref)
	if err != nil {
		e.recordDiagnostic("supervisor_turn_checkpoint", fmt.Errorf("assistant %s: history baseline probe: %w", a.ID, err))
		e.turnMu.Unlock()
		return
	}
	// Intent checkpoint: durably claimed BEFORE the submission, so a crash
	// between here and the submit, or a concurrent tick/process, can never
	// double-submit.
	cp := SupervisorTurnCheckpoint{
		TurnID:      StableID("supervisor-turn", a.ID+"/"+batchID+"/"+fmt.Sprint(now.UnixNano())),
		SubmittedAt: now,
		EventIDs:    eventIDs,
		BatchID:     batchID,
		HistoryLen:  baseline,
	}
	created, err := e.events.SaveTurnCheckpoint(a.ID, cp)
	if err != nil {
		e.recordDiagnostic("supervisor_turn_checkpoint", err)
		e.turnMu.Unlock()
		return
	}
	if !created {
		// Another process owns the in-flight turn: settle it, never submit.
		if existing, ok, err := e.events.LoadTurnCheckpoint(a.ID); err == nil && ok {
			e.turnMu.Unlock()
			e.settleCheckpointedTurn(a, existing, now)
			return
		}
		e.turnMu.Unlock()
		return
	}
	e.turnMu.Unlock()

	var viewport ViewportSnapshot
	if e.viewport != nil {
		viewport, _ = e.viewport(now)
	}
	// Bounded implicit context: plan + sessions + attention + memory + work
	// gate + routines/retry + project constraints, each with its revision so the
	// decision is traceable to the exact observed state.
	wc, wcErr := e.store.WorkControl()
	if wcErr != nil {
		e.recordDiagnostic("supervisor_context_workcontrol", wcErr)
		wc = WorkControl{}
	}
	retryDue := 0
	if due, err := e.store.RetryDue(now); err == nil {
		retryDue = len(due)
	} else {
		e.recordDiagnostic("supervisor_context_retry", err)
	}
	constraints, constraintsRev := "", int64(0)
	if e.constraints != nil {
		constraints, constraintsRev = e.constraints(a.ID)
	}
	prompt := BuildSupervisorContext(SupervisorContextInput{
		Assistant:                  snapshot.Assistant,
		Plan:                       snapshot.Plan,
		RunningSessions:            running,
		FailedSessions:             failed,
		PendingAttention:           len(snapshot.Attention),
		Memory:                     snapshot.Memory,
		WorkControl:                wc,
		Routines:                   snapshot.Routines,
		RetryDue:                   retryDue,
		ProjectConstraints:         constraints,
		ProjectConstraintsRevision: constraintsRev,
		PendingEvents:              events,
		Viewport:                   viewport,
		Now:                        now,
	}) + supervisorDecisionInstruction

	outcome := e.host.RunSupervisorTurn(ref, prompt, e.turnBudget)
	if outcome.Err != nil {
		e.recordDiagnostic("supervisor_turn", outcome.Err)
		// The submission did not happen: drop the intent so the next tick
		// retries cleanly.
		if err := e.events.ClearTurnCheckpoint(a.ID); err != nil {
			e.recordDiagnostic("supervisor_turn_checkpoint", err)
		}
		return
	}
	if outcome.Running {
		// Still in flight past the budget: the host reports the transcript
		// length right before the submission. The checkpoint baseline was
		// already captured pre-submit; this keeps the host's own authoritative
		// observation so a restart can confirm the submission durably landed
		// and settle instead of double-submitting.
		cp.HistoryLen = outcome.HistoryLen
		if err := e.events.UpdateTurnCheckpoint(a.ID, cp); err != nil {
			e.recordDiagnostic("supervisor_turn_checkpoint", err)
		}
		return
	}
	e.finishTurn(a, cp, outcome, now)
}

// settleCheckpointedTurn continues a previously submitted supervisor turn from
// its durable checkpoint: it reads the current turn state WITHOUT submitting a
// new prompt, routes the finished outcome (or waits), and only re-submits when
// the checkpoint proves the submission never durably landed.
func (e *SupervisorExecutor) settleCheckpointedTurn(a Assistant, cp SupervisorTurnCheckpoint, now time.Time) {
	ref, ok := e.host.FindSupervisorSession(a.ID)
	if !ok {
		e.recordDiagnostic("supervisor_turn_settle", errors.New("assistant: supervisor session disappeared while a turn was in flight"))
		return // keep the checkpoint: the turn must settle before anything new
	}
	outcome := e.host.SettleSupervisorTurn(ref)
	if outcome.Err != nil {
		e.recordDiagnostic("supervisor_turn", outcome.Err)
		return // keep the checkpoint; the next tick retries the settle
	}
	if outcome.Running {
		return // still in flight; events stay pending
	}
	if outcome.Pending {
		// The supervisor asked the user: the turn consumed the trigger events.
		// The user's answer arrives as a user_input event and wakes the loop.
		e.consumeAndComplete(a, cp.EventIDs, now)
		e.clearCheckpoint(a.ID)
		return
	}
	if outcome.HistoryLen <= cp.HistoryLen {
		// The durable Session history did not grow: the submission never
		// landed (crash between the intent and the submit). Confirmed
		// not-submitted: clear the checkpoint and let the next tick submit.
		e.recordDiagnostic("supervisor_turn_settle", errors.New("assistant: checkpointed turn produced no durable history; re-submitting"))
		e.clearCheckpoint(a.ID)
		return
	}
	e.routeAndConsume(a, outcome, cp.EventIDs, cp.BatchID, now)
	e.clearCheckpoint(a.ID)
}

// finishTurn settles a turn that finished within the budget: a pending
// interaction consumes the events and waits for the user; otherwise the
// decision is routed under the batch/action receipts and the trigger events
// are consumed only on a consuming outcome.
func (e *SupervisorExecutor) finishTurn(a Assistant, cp SupervisorTurnCheckpoint, outcome SupervisorTurnOutcome, now time.Time) {
	if outcome.Pending {
		e.consumeAndComplete(a, cp.EventIDs, now)
		e.clearCheckpoint(a.ID)
		return
	}
	e.routeAndConsume(a, outcome, cp.EventIDs, cp.BatchID, now)
	e.clearCheckpoint(a.ID)
}

func (e *SupervisorExecutor) clearCheckpoint(assistantID string) {
	if err := e.events.ClearTurnCheckpoint(assistantID); err != nil {
		e.recordDiagnostic("supervisor_turn_checkpoint", err)
	}
}

// consumeAndComplete consumes the trigger events of a routed turn and completes
// the current cycle under its fence. Even when the consume write fails, the
// batch receipt already records the routed outcome, so a replay settles as
// already_applied instead of re-running the action.
func (e *SupervisorExecutor) consumeAndComplete(a Assistant, eventIDs []string, now time.Time) {
	if err := e.events.MarkConsumed(a.ID, eventIDs...); err != nil {
		e.recordDiagnostic("supervisor_events_consume", err)
	}
	e.completeCurrentCycle(a, now)
}

// routeAndConsume parses the decision from the settled turn output, routes the
// acting phase, and consumes the trigger events only for a consuming outcome.
// A failed route keeps the events pending (the observable retry state) and
// leaves the cycle open.
func (e *SupervisorExecutor) routeAndConsume(a Assistant, outcome SupervisorTurnOutcome, eventIDs []string, batchID string, now time.Time) {
	decision, err := ParseSupervisorDecision(outcome.Text)
	if err != nil {
		e.recordDiagnostic("supervisor_decision", err)
		return // events stay pending; the next tick retries
	}
	res := e.RouteDecision(a, decision, batchID, eventIDs, now)
	if res.Err != nil || !res.Outcome.ConsumesEvents() {
		// Failed route (or a missing capability): the trigger events stay
		// pending and the cycle stays open — the durable, observable retry
		// state. The specific diagnostic was recorded by the acting phase.
		return
	}
	e.consumeAndComplete(a, eventIDs, now)
}

// completeCurrentCycle marks the assistant's current cycle completed under its
// fence after a routed decision, so the durable resume point reflects the
// acting phase. It is a no-op when the cycle is already completed.
func (e *SupervisorExecutor) completeCurrentCycle(a Assistant, now time.Time) {
	cycle, ok := e.store.LatestCycle(a.ID)
	if !ok || cycle.State == CycleCompleted {
		return
	}
	if _, err := e.store.CompleteCycle(CompleteCycleInput{
		AssistantID: a.ID, CycleID: cycle.ID, Fence: cycle.Fence,
		RequestID: StableID("request", fmt.Sprintf("cycle-complete/%s/%d/%d", a.ID, cycle.Fence, now.UnixNano())),
		Now:       now,
	}); err != nil {
		e.recordDiagnostic("cycle_complete", err)
	}
}

// AutoAnswerPending is the exported acting-phase entry used by hosts to answer
// one managed Session's pending asks. It resolves the host's SessionControl and
// AutoAnswer lazily so tests can inject fakes after construction.
func (e *SupervisorExecutor) AutoAnswerPending(a Assistant, sessionID string) {
	e.autoAnswerPending(a, sessionID) // failures are recorded inside
}

// AutoAnswerInteraction is the exported single-interaction auto-answer entry.
func (e *SupervisorExecutor) AutoAnswerInteraction(a Assistant, sessionID string, item SessionInteraction, now time.Time) {
	e.autoAnswerInteraction(a, sessionID, item, now)
}

// ResolveExperimentTrials is the exported experiment winner sweep. The host's
// trial status resolver hook is resolved lazily.
func (e *SupervisorExecutor) ResolveExperimentTrials() {
	e.resolveExperimentTrials()
}

// AdvanceResponsibility is the exported supervisor advance entry (used by hosts
// and tests directly); it resolves the SessionControl lazily.
func (e *SupervisorExecutor) AdvanceResponsibility(a Assistant, alias string) {
	e.advanceResponsibility(a, alias) // failures are recorded inside
}

// DecisionRouteOutcome is the typed acting-phase result of one routed
// supervisor decision. Only outcomes that ConsumesEvents() let the caller drop
// the turn's trigger events and complete the cycle; a failure or a missing
// capability keeps the events pending as the observable retry state.
type DecisionRouteOutcome string

const (
	// RouteApplied means the external action executed successfully.
	RouteApplied DecisionRouteOutcome = "applied"
	// RouteAlreadyApplied means the same event batch was already routed (a
	// crash between action and consume replayed); the events are consumed
	// without re-executing the action or the model turn.
	RouteAlreadyApplied DecisionRouteOutcome = "already_applied"
	// RouteHardGatePending means the decision hit a hard gate and was left for
	// the user; the trigger events are consumed and the user's answer wakes the
	// next turn.
	RouteHardGatePending DecisionRouteOutcome = "hard_gate_pending"
	// RouteNoOp means nothing actionable (wait, no ready responsibility, no
	// pending asks): a safe no-op consumes the events.
	RouteNoOp DecisionRouteOutcome = "noop"
	// RouteFailed means the action failed or a required capability is missing;
	// the events stay pending for the next tick.
	RouteFailed DecisionRouteOutcome = "failed"
)

// ConsumesEvents reports whether the caller may consume the trigger events and
// complete the cycle after this outcome. Failed outcomes never consume.
func (o DecisionRouteOutcome) ConsumesEvents() bool {
	switch o {
	case RouteApplied, RouteAlreadyApplied, RouteHardGatePending, RouteNoOp:
		return true
	}
	return false
}

// DecisionRouteResult is the typed result of one routed decision.
type DecisionRouteResult struct {
	Outcome DecisionRouteOutcome
	Err     error
	BatchID string
}

// RouteDecision executes the acting phase of one supervisor decision: expand
// triggers ideation, steer steers a managed Session, advance creates a managed
// Session for the targeted responsibility, answer runs the auto-answer loop,
// and wait is a no-op. The result is typed: only consuming outcomes let the
// caller drop the trigger events; a failure keeps them pending as the
// observable retry state. All acting goes through idempotent Session-control
// writes with stable request IDs, and the event batch is durably linked to the
// routed decision (SupervisorBatchReceipt) BEFORE the caller consumes, so a
// crash between action and consume replays as already_applied instead of
// re-executing the external action.
func (e *SupervisorExecutor) RouteDecision(a Assistant, decision SupervisorDecision, batchID string, eventIDs []string, now time.Time) DecisionRouteResult {
	batchID = strings.TrimSpace(batchID)
	if batchID != "" {
		if rec, ok, err := e.events.LoadBatchReceipt(a.ID, batchID); err == nil && ok && rec.Outcome.ConsumesEvents() {
			return DecisionRouteResult{Outcome: RouteAlreadyApplied, BatchID: batchID}
		}
	}
	res := e.routeAction(a, decision, batchID, now)
	if res.Outcome.ConsumesEvents() && batchID != "" {
		// Durable link between the event batch and the routed decision: written
		// BEFORE the caller consumes, so a crash in between settles as
		// already_applied on replay.
		if err := e.events.SaveBatchReceipt(a.ID, SupervisorBatchReceipt{
			BatchID: batchID, AssistantID: a.ID, EventIDs: append([]string(nil), eventIDs...),
			Decision: decision, Outcome: res.Outcome, RoutedAt: now,
		}); err != nil {
			e.recordDiagnostic("supervisor_batch_receipt", err)
		}
	}
	return res
}

// routeAction runs one decision's acting phase without the batch-receipt layer.
func (e *SupervisorExecutor) routeAction(a Assistant, decision SupervisorDecision, batchID string, now time.Time) DecisionRouteResult {
	switch decision.Action {
	case ActionWait:
		return DecisionRouteResult{Outcome: RouteNoOp, BatchID: batchID}
	case ActionExpand:
		return e.expand(a, batchID, now)
	case ActionAdopt:
		return e.adoptOpportunity(a, decision.Target, batchID, now)
	case ActionSteer:
		control := e.sessionControl()
		if control == nil {
			res := DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID,
				Err: errors.New("assistant: supervisor steer capability is unavailable")}
			e.recordDiagnostic("supervisor_steer", res.Err)
			return res
		}
		requestID := StableID("request", "supervisor-steer/"+a.ID+"/"+decision.Target)
		if err := control.Steer(decision.Target, decision.Rationale, requestID); err != nil {
			e.recordDiagnostic("supervisor_steer", err)
			return DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID, Err: err}
		}
		return DecisionRouteResult{Outcome: RouteApplied, BatchID: batchID}
	case ActionAdvance:
		return e.advanceResponsibility(a, decision.Target)
	case ActionAnswer:
		return e.autoAnswerPending(a, decision.Target).toRouteResult(batchID)
	default:
		res := DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID,
			Err: fmt.Errorf("assistant: unknown supervisor action %q", decision.Action)}
		e.recordDiagnostic("supervisor_route", res.Err)
		return res
	}
}

// expand runs the Discover stage (Ideator) and records the expansion outcome
// with bounded backoff, so a fruitless or failed pass does not spin on every
// tick. finite-mode assistants never reach here for auto-expansion: the plan
// gate treats plan_empty as terminal per the completion strategy.
func (e *SupervisorExecutor) expand(a Assistant, batchID string, now time.Time) DecisionRouteResult {
	if e.ideator == nil {
		res := DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID,
			Err: errors.New("assistant: supervisor expand capability is unavailable")}
		e.recordDiagnostic("supervisor_expand", res.Err)
		return res
	}
	requestID := actionRequestID("supervisor-expand", a.ID, batchID, now)
	if _, err := e.ideator.Ideate(context.Background(), OpenIdeaInput{
		AssistantID: a.ID,
		RequestID:   requestID,
		Trigger:     IdeaTriggerManual, Now: now,
	}); err != nil {
		e.recordDiagnostic("supervisor_expand", err)
		_, _ = e.store.RecordExpansion(RecordExpansionInput{
			RequestID:   actionRequestID("expansion-fail", a.ID, batchID, now),
			AssistantID: a.ID, Trigger: ExpansionPlanEmpty, Err: err.Error(), Now: now,
		})
		return DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID, Err: err}
	}
	_, _ = e.store.RecordExpansion(RecordExpansionInput{
		RequestID:   actionRequestID("expansion-ok", a.ID, batchID, now),
		AssistantID: a.ID, Trigger: ExpansionPlanEmpty, Now: now,
	})
	return DecisionRouteResult{Outcome: RouteApplied, BatchID: batchID}
}

// actionRequestID derives the stable request ID of one supervisor acting-phase
// action. A non-empty batchID (a durable event batch) makes the ID fully
// deterministic from assistantID + batchID: a crash between the action and the
// batch receipt replays with the same ID, and the store's receipt dedup returns
// the already-recorded result instead of re-executing the action (ideation or
// expansion record). Empty-batch manual/test calls keep per-call uniqueness.
func actionRequestID(prefix, assistantID, batchID string, now time.Time) string {
	if strings.TrimSpace(batchID) != "" {
		return StableID("request", prefix+"/"+assistantID+"/"+batchID)
	}
	return StableID("request", prefix+"/"+assistantID+"/"+fmt.Sprint(now.UnixNano()))
}

// adoptOpportunity executes the Adopt -> Execute stages for one evidence-backed
// opportunity: it re-checks the Rank gate (continuous mode + policy + evidence
// + dedup + concurrency), promotes the opportunity to a planned responsibility,
// and starts its managed Session through the normal advance path. A policy
// refusal or a fruitless candidate is an explicit typed result so the trigger
// events stay observable and the opportunity stays in the pool.
func (e *SupervisorExecutor) adoptOpportunity(a Assistant, opportunityID, batchID string, now time.Time) DecisionRouteResult {
	control := e.sessionControl()
	if control == nil {
		res := DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID,
			Err: errors.New("assistant: supervisor adopt capability is unavailable")}
		e.recordDiagnostic("supervisor_adopt", res.Err)
		return res
	}
	snapshot, err := e.store.Get(a.ID)
	if err != nil {
		e.recordDiagnostic("supervisor_adopt", err)
		return DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID, Err: err}
	}
	var opp *Opportunity
	for i := range snapshot.Opportunities {
		if snapshot.Opportunities[i].ID == opportunityID {
			opp = &snapshot.Opportunities[i]
			break
		}
	}
	if opp == nil {
		return DecisionRouteResult{Outcome: RouteNoOp, BatchID: batchID}
	}
	runningSessions, _ := e.supervisorSessions(a.ID)
	out, reason := EvaluateOpportunityAdoption(a, snapshot, *opp, len(runningSessions))
	if out != AdoptProceed {
		// Policy/evidence/duplicate/capacity refusals keep the opportunity in
		// the pool; the decision is recorded and the trigger stays observable.
		e.recordDiagnostic("supervisor_adopt", fmt.Errorf("%s: %s", out, reason))
		_, _ = e.store.RecordExpansion(RecordExpansionInput{
			RequestID:   StableID("request", "expansion-adopt/"+a.ID+"/"+opportunityID),
			AssistantID: a.ID, Trigger: ExpansionPlanEmpty, Err: reason, Now: now,
		})
		return DecisionRouteResult{Outcome: RouteNoOp, BatchID: batchID}
	}
	requestID := StableID("request", "adopt/"+a.ID+"/"+opportunityID)
	resp, err := e.store.AdoptOpportunity(AdoptOpportunityInput{
		RequestID: requestID, AssistantID: a.ID, OpportunityID: opportunityID, Now: now,
	})
	if err != nil {
		e.recordDiagnostic("supervisor_adopt", err)
		return DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID, Err: err}
	}
	// Execute: the adopted responsibility runs through the same managed-Session
	// path as advance (idempotent by responsibility).
	return e.advanceResponsibility(a, resp.Alias)
}

// advanceResponsibility turns the supervisor's advance decision into a managed
// Session that actually executes the targeted responsibility. The Session goes
// through the shared Session subsystem (SessionControl.Create) with a stable
// request ID derived from the responsibility, so a duplicated or replayed
// advance resolves to the same Session instead of creating a second one. The
// typed result lets the caller keep the trigger events pending on failure.
func (e *SupervisorExecutor) advanceResponsibility(a Assistant, alias string) DecisionRouteResult {
	control := e.sessionControl()
	if control == nil {
		res := DecisionRouteResult{Outcome: RouteFailed, Err: errors.New("assistant: supervisor advance capability is unavailable")}
		e.recordDiagnostic("supervisor_advance", res.Err)
		return res
	}
	snapshot, err := e.store.Get(a.ID)
	if err != nil {
		e.recordDiagnostic("supervisor_advance", err)
		return DecisionRouteResult{Outcome: RouteFailed, Err: err}
	}
	resp := findResponsibilityByAlias(snapshot.Plan, alias)
	if resp == nil {
		resp = selectReadyResponsibility(snapshot.Plan)
	}
	if resp == nil {
		// No ready responsibility: the advance decision is stale — a safe no-op.
		return DecisionRouteResult{Outcome: RouteNoOp}
	}
	prompt := strings.TrimSpace(resp.NextAction)
	if prompt == "" {
		prompt = strings.TrimSpace(resp.Objective)
	}
	if prompt == "" {
		return DecisionRouteResult{Outcome: RouteNoOp}
	}
	if _, err := control.Create(SessionCreateRequest{
		Title:   resp.Objective,
		Prompt:  prompt,
		OwnerID: a.ID,
		// Purpose stays an untyped constant so the assistant package never
		// imports agent (agent imports assistant). The Session subsystem reads
		// it back as PurposeManaged.
		Purpose: "managed",
		// The session executes exactly this plan item: the binding is persisted
		// in the Session meta so active/failed/completed derive from the Session
		// and are never written back to the plan.
		ResponsibilityID: resp.ID,
		RequestID:        StableID("request", "advance/"+a.ID+"/"+resp.ID),
	}); err != nil {
		e.recordDiagnostic("supervisor_advance", err)
		return DecisionRouteResult{Outcome: RouteFailed, Err: err}
	}
	return DecisionRouteResult{Outcome: RouteApplied}
}

func findResponsibilityByAlias(plan Plan, alias string) *Responsibility {
	alias = strings.TrimSpace(alias)
	if alias == "" {
		return nil
	}
	for i := range plan.Responsibilities {
		r := &plan.Responsibilities[i]
		if r.Alias == alias || r.ID == alias {
			return r
		}
	}
	return nil
}

// selectReadyResponsibility deterministically picks the one responsibility a
// session works on: an already-active one wins, otherwise the first ready one
// in creation order.
func selectReadyResponsibility(plan Plan) *Responsibility {
	for i := range plan.Responsibilities {
		if plan.Responsibilities[i].Status == RespActive {
			r := plan.Responsibilities[i]
			return &r
		}
	}
	for i := range plan.Responsibilities {
		if plan.Responsibilities[i].Status == RespReady {
			r := plan.Responsibilities[i]
			return &r
		}
	}
	return nil
}

func truncateString(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}
