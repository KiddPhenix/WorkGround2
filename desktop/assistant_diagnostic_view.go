package main

import (
	"sort"
	"time"

	"workground2/internal/assistant"
)

// ── 隐式上下文 / 自主决策诊断 ─────────────────────────────────────────────
//
// AssistantSupervisorDiagnostic is the read-only view of what the supervisor
// loop actually observed and decided: the supervisor Session identity, the
// cycle observation revisions, pending/merged events, recent decision/action
// receipts, the next-step hint, and failures/retries. Every field is derived
// from backend authoritative state (Store, event queue, supervisor executor) —
// the UI renders it, it never feeds UI-private state back.

type AssistantSupervisorDiagnostic struct {
	AssistantID     string                  `json:"assistant_id"`
	Supervisor      *AssistantSupervisorRef `json:"supervisor,omitempty"`
	Cycle           *AssistantCycleView     `json:"cycle,omitempty"`
	PendingEvents   []AssistantEventView    `json:"pending_events,omitempty"`
	RecentDecisions []AssistantDecisionView `json:"recent_decisions,omitempty"`
	RecentReceipts  []AssistantReceiptView  `json:"recent_receipts,omitempty"`
	NextStep        string                  `json:"next_step,omitempty"`
	RunningSessions []AssistantSessionView  `json:"running_sessions,omitempty"`
	FailedSessions  []AssistantSessionView  `json:"failed_sessions,omitempty"`
	RetryDue        int                     `json:"retry_due"`
	Diagnostics     []AssistantDiagnostic   `json:"diagnostics,omitempty"`
	At              time.Time               `json:"at" ts_type:"string"`
}

type AssistantSupervisorRef struct {
	ID   string `json:"id"`
	Path string `json:"path"`
}

type AssistantCycleView struct {
	ID        string                    `json:"id"`
	Fence     int64                     `json:"fence"`
	State     string                    `json:"state"`
	Observed  AssistantCycleObservation `json:"observed"`
	NextStep  string                    `json:"next_step,omitempty"`
	Revision  int64                     `json:"revision"`
	UpdatedAt time.Time                 `json:"updated_at" ts_type:"string"`
}

type AssistantCycleObservation struct {
	PlanRevision      int64 `json:"plan_revision"`
	AssistantRevision int64 `json:"assistant_revision"`
	MemoryRevision    int64 `json:"memory_revision"`
	WorkEpoch         int64 `json:"work_epoch"`
}

type AssistantEventView struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	SessionID string    `json:"session_id,omitempty"`
	Revision  int64     `json:"revision,omitempty"`
	RequestID string    `json:"request_id,omitempty"`
	Payload   string    `json:"payload,omitempty"`
	At        time.Time `json:"at" ts_type:"string"`
}

type AssistantDecisionView struct {
	ID            string    `json:"id"`
	SessionID     string    `json:"session_id"`
	InteractionID string    `json:"interaction_id"`
	Source        string    `json:"source,omitempty"`
	Confidence    float64   `json:"confidence,omitempty"`
	Result        string    `json:"result,omitempty"`
	Winner        string    `json:"winner,omitempty"`
	Rollback      string    `json:"rollback,omitempty"`
	DueAt         time.Time `json:"due_at,omitempty" ts_type:"string"`
	CreatedAt     time.Time `json:"created_at" ts_type:"string"`
}

type AssistantReceiptView struct {
	RequestID string    `json:"request_id"`
	Operation string    `json:"operation"`
	CreatedAt time.Time `json:"created_at" ts_type:"string"`
}

type AssistantSessionView struct {
	ID      string `json:"id"`
	Title   string `json:"title"`
	Status  string `json:"status"`
	Purpose string `json:"purpose,omitempty"`
}

// AssistantSupervisorDiagnostic returns the bounded supervisor diagnostic for
// one assistant. Failures are explicit: an unavailable runtime or store is an
// error, never a silently empty diagnostic.
func (a *App) AssistantSupervisorDiagnostic(assistantID string) (AssistantSupervisorDiagnostic, error) {
	service, err := a.assistantRuntime()
	if err != nil {
		return AssistantSupervisorDiagnostic{}, err
	}
	out := AssistantSupervisorDiagnostic{
		AssistantID: assistantID,
		At:          time.Now(),
		Diagnostics: service.Diagnostics(),
	}
	if service.executor != nil {
		if ref, ok := service.executor.Host().FindSupervisorSession(assistantID); ok {
			out.Supervisor = &AssistantSupervisorRef{ID: ref.ID, Path: ref.Path}
		}
		running, failed := service.executor.SessionSummaries(assistantID)
		out.RunningSessions = sessionViews(running)
		out.FailedSessions = sessionViews(failed)
		if events, err := service.executor.Events().Pending(assistantID); err == nil {
			for _, ev := range events {
				out.PendingEvents = append(out.PendingEvents, AssistantEventView{
					ID: ev.ID, Kind: string(ev.Kind), SessionID: ev.SessionID,
					Revision: ev.Revision, RequestID: ev.RequestID, Payload: ev.Payload, At: ev.At,
				})
			}
		}
	}
	if service.store != nil {
		if cycle, ok := service.store.LatestCycle(assistantID); ok {
			out.Cycle = &AssistantCycleView{
				ID: cycle.ID, Fence: cycle.Fence, State: string(cycle.State),
				Observed: AssistantCycleObservation{
					PlanRevision:      cycle.Observed.PlanRevision,
					AssistantRevision: cycle.Observed.AssistantRevision,
					MemoryRevision:    cycle.Observed.MemoryRevision,
					WorkEpoch:         cycle.Observed.WorkEpoch,
				},
				NextStep:  cycle.NextStep,
				Revision:  cycle.Revision,
				UpdatedAt: cycle.UpdatedAt,
			}
			if out.NextStep == "" {
				out.NextStep = cycle.NextStep
			}
		}
		snapshot, err := service.store.Get(assistantID)
		if err == nil {
			out.RecentDecisions = decisionViews(snapshot.Decisions)
			out.RecentReceipts = receiptViews(snapshot.Receipts)
			if out.NextStep == "" {
				out.NextStep = service.supervisorNextStep(snapshot, out.At)
			}
		}
		if due, err := service.store.RetryDue(out.At); err == nil {
			out.RetryDue = len(due)
		}
	}
	return out, nil
}

func sessionViews(items []assistant.SupervisorSessionSummary) []AssistantSessionView {
	out := make([]AssistantSessionView, 0, len(items))
	for _, s := range items {
		out = append(out, AssistantSessionView{ID: s.ID, Title: s.Title, Status: s.Status, Purpose: s.Purpose})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func decisionViews(items []assistant.InteractionDecisionRecord) []AssistantDecisionView {
	out := make([]AssistantDecisionView, 0, len(items))
	for _, d := range items {
		out = append(out, AssistantDecisionView{
			ID: d.ID, SessionID: d.SessionID, InteractionID: d.InteractionID,
			Source: string(d.Source), Confidence: d.Confidence, Result: d.Result,
			Winner: d.Winner, Rollback: d.Rollback, DueAt: d.DueAt, CreatedAt: d.CreatedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}

func receiptViews(items []assistant.RequestReceipt) []AssistantReceiptView {
	out := make([]AssistantReceiptView, 0, len(items))
	for _, r := range items {
		out = append(out, AssistantReceiptView{RequestID: r.RequestID, Operation: r.Operation, CreatedAt: r.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	if len(out) > 10 {
		out = out[:10]
	}
	return out
}
