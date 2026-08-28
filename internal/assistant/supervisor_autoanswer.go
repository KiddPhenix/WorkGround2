package assistant

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"workground2/internal/event"
)

// autoAnswerOutcome is the bounded acting-phase result of one answer action.
// A hard gate leaves the whole batch for the user (the trigger events are
// consumed and the user's answer wakes the next turn); an applied batch
// answered or deferred at least one interaction; a failure keeps the events
// pending for the next tick.
type autoAnswerOutcome struct {
	hardGate bool
	applied  bool
	failed   error
}

func (o autoAnswerOutcome) merge(x autoAnswerOutcome) autoAnswerOutcome {
	if x.failed != nil && o.failed == nil {
		o.failed = x.failed
	}
	o.hardGate = o.hardGate || x.hardGate
	o.applied = o.applied || x.applied
	return o
}

func (o autoAnswerOutcome) toRouteResult(batchID string) DecisionRouteResult {
	switch {
	case o.failed != nil:
		return DecisionRouteResult{Outcome: RouteFailed, BatchID: batchID, Err: o.failed}
	case o.hardGate:
		return DecisionRouteResult{Outcome: RouteHardGatePending, BatchID: batchID}
	case o.applied:
		return DecisionRouteResult{Outcome: RouteApplied, BatchID: batchID}
	default:
		return DecisionRouteResult{Outcome: RouteNoOp, BatchID: batchID}
	}
}

// autoAnswerPending answers one session's pending ask questions with the model,
// unless any question is a hard gate (then the whole batch is left for the
// user). It is the 17.5 auto-answer loop wiring: RouteInteraction decides the
// source (infer / experiment / user), AutoAnswer picks the option, and the
// SessionControl adapter submits the answer to the live controller or to an
// isolated fork. Every decision is recorded through Store.RecordInteractionDecision
// so the source/confidence/candidates/rationale/result/rollback point are
// auditable. This runs inside the shared supervisor executor so desktop and
// daemon behave identically.
func (e *SupervisorExecutor) autoAnswerPending(a Assistant, sessionID string) autoAnswerOutcome {
	control := e.sessionControl()
	autoAnswer := e.resolveAutoAnswer()
	if control == nil || autoAnswer == nil {
		res := autoAnswerOutcome{failed: errors.New("assistant: supervisor auto-answer capability is unavailable")}
		e.recordDiagnostic("autoanswer_pending", res.failed)
		return res
	}
	items, err := control.PendingInteractions(sessionID)
	if err != nil {
		e.recordDiagnostic("autoanswer_pending", err)
		return autoAnswerOutcome{failed: err}
	}
	now := time.Now()
	var out autoAnswerOutcome
	for _, item := range items {
		if item.Kind != "ask" || len(item.Questions) == 0 {
			continue
		}
		out = out.merge(e.autoAnswerInteraction(a, sessionID, item, now))
	}
	if out.failed == nil && !out.hardGate && !out.applied {
		// No pending ask batch (or every batch was already terminal): a safe
		// no-op — the decision was stale.
		return autoAnswerOutcome{}
	}
	return out
}

// autoAnswerInteraction routes and answers one pending ask batch (an interaction
// may carry several questions submitted together).
func (e *SupervisorExecutor) autoAnswerInteraction(a Assistant, sessionID string, item SessionInteraction, now time.Time) autoAnswerOutcome {
	adapter := e.sessionControl()
	autoAnswer := e.resolveAutoAnswer()
	if adapter == nil || autoAnswer == nil {
		res := autoAnswerOutcome{failed: errors.New("assistant: supervisor auto-answer capability is unavailable")}
		e.recordDiagnostic("autoanswer_infer", res.failed)
		return res
	}
	interactionID := strings.TrimSpace(item.ID)
	if interactionID == "" && len(item.Questions) > 0 {
		interactionID = strings.TrimSpace(item.Questions[0].ID)
	}
	if interactionID == "" {
		res := autoAnswerOutcome{failed: errors.New("assistant: auto-answer interaction has no id")}
		e.recordDiagnostic("autoanswer_infer", res.failed)
		return res
	}

	// Resume any prior decision for this interaction. A terminal decision
	// (answered, forked, or left for the user) means there is nothing more to
	// do; a deferred decision carries the persisted deadline.
	var prior InteractionDecisionRecord
	hasPrior := false
	if e.store != nil {
		var err error
		prior, hasPrior, err = e.store.LatestDecision(a.ID, sessionID, interactionID)
		if err != nil {
			e.recordDiagnostic("autoanswer_decision_lookup", err)
			return autoAnswerOutcome{failed: err}
		}
	}
	if hasPrior && prior.Source != DecisionDeferred {
		return autoAnswerOutcome{} // already terminal: safe no-op
	}

	// Decision deadline: the interaction's own DueAt wins, then a previously
	// recorded deferral, then the default deadline from first observation.
	dueAt := item.DueAt
	if dueAt.IsZero() {
		if hasPrior && !prior.DueAt.IsZero() {
			dueAt = prior.DueAt
		} else {
			dueAt = AutoAnswerDueAt(now)
		}
	}
	if now.Before(dueAt) {
		// Not yet due: park the deadline (idempotently) and leave the
		// interaction for the user or a later tick.
		e.recordDecision(InteractionDecisionRecord{
			ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/deferred", a.ID, sessionID, interactionID)),
			AssistantID:   a.ID,
			SessionID:     sessionID,
			InteractionID: interactionID,
			Source:        DecisionDeferred,
			Rationale:     "interaction not yet due; deferring to the user or a later tick",
			Result:        "deferred",
			DueAt:         dueAt,
		})
		return autoAnswerOutcome{applied: true}
	}

	prompt := interactionPrompt(item.Questions)

	// Hard gate short-circuit: never spend a model turn (and never infer
	// options) on a funds/legal/identity/credentials/destructive/policy
	// question. The whole batch is left for the user.
	if reason, gate := ClassifyHardGate("answer_required", prompt, a.Policy); gate {
		e.recordDecision(InteractionDecisionRecord{
			ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/user", a.ID, sessionID, interactionID)),
			AssistantID:   a.ID,
			SessionID:     sessionID,
			InteractionID: interactionID,
			Source:        DecisionUser,
			HardGate:      reason,
			Rationale:     "hard gate: " + string(reason),
			Result:        "wait_for_user",
		})
		return autoAnswerOutcome{hardGate: true}
	}

	// Infer the answer(s) plus confidence before routing, so RouteInteraction
	// decides from evidence instead of a hidden guess.
	var answers []event.AskAnswer
	candidates := make([]string, 0, len(item.Questions))
	rationales := make([]string, 0, len(item.Questions))
	confidence := 1.0
	for _, question := range item.Questions {
		inference, err := autoAnswer.InferDecision(context.Background(), a.Mission, question)
		if err != nil {
			e.recordDiagnostic("autoanswer_infer", err)
			return autoAnswerOutcome{failed: err}
		}
		answers = append(answers, inference.Answer)
		for _, label := range inference.Answer.Selected {
			if !containsString(candidates, label) {
				candidates = append(candidates, label)
			}
		}
		if inference.Confidence < confidence {
			confidence = inference.Confidence
		}
		if inference.Rationale != "" {
			rationales = append(rationales, inference.Rationale)
		}
	}
	rationale := strings.Join(rationales, "；")

	decision := RouteInteraction(RouteInteractionInput{
		Action:     "answer_required",
		Prompt:     prompt,
		Policy:     a.Policy,
		Confidence: confidence,
		Candidates: candidates,
		Reversible: true,
		CanIsolate: true,
		Now:        now,
	})
	if rationale == "" {
		rationale = decision.Rationale
	}

	// submitInfer answers the original session directly and records an infer
	// decision. It is the shared fallback for both the confident path and an
	// experiment that could not isolate enough candidates.
	submitInfer := func(rationaleOverride string) autoAnswerOutcome {
		requestID := StableID("request", "autoanswer/"+sessionID+"/"+interactionID+"/answer")
		if err := adapter.AnswerQuestion(sessionID, item.ID, answers, requestID); err != nil {
			e.recordDiagnostic("autoanswer_submit", err)
			return autoAnswerOutcome{failed: err}
		}
		e.recordDecision(InteractionDecisionRecord{
			ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/infer", a.ID, sessionID, interactionID)),
			AssistantID:   a.ID,
			SessionID:     sessionID,
			InteractionID: interactionID,
			Source:        DecisionInfer,
			Confidence:    confidence,
			Candidates:    candidates,
			Rationale:     rationaleOverride,
			Result:        "answered",
			DueAt:         dueAt,
		})
		return autoAnswerOutcome{applied: true}
	}

	switch decision.Source {
	case DecisionInfer:
		return submitInfer(rationale)
	case DecisionExperiment:
		// Low-confidence but reversible: race several candidates in isolated
		// forks so a bad guess never mutates the shared session. Each fork gets
		// one candidate answer; the winner sweep (resolveExperimentTrials)
		// later picks the first completed trial and cancels the rest.
		candidateAnswers := BuildExperimentCandidates(item.Questions, answers)
		if len(candidateAnswers) < 2 {
			return submitInfer("experiment isolation produced fewer than two candidates; " + rationale)
		}
		trials := make([]TrialState, 0, len(candidateAnswers))
		for idx, cand := range candidateAnswers {
			forkRequestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/fork/%d", sessionID, interactionID, idx))
			forkID, err := adapter.Fork(sessionID, forkRequestID)
			if err != nil {
				e.recordDiagnostic("autoanswer_fork", err)
				continue
			}
			answerRequestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/answer/%d", forkID, interactionID, idx))
			if err := adapter.AnswerQuestion(forkID, item.ID, cand, answerRequestID); err != nil {
				e.recordDiagnostic("autoanswer_submit_fork", err)
				continue
			}
			trials = append(trials, TrialState{
				SessionID: forkID,
				// The fork Session is this candidate's isolation; a host that
				// supports real worktree/sandbox isolation records that
				// location here instead. Every candidate is isolated on its
				// own fork — candidates never share a workspace.
				Worktree: "session:" + forkID,
				Answer:   EncodeTrialAnswer(cand),
				Status:   TrialStatusRunning,
			})
		}
		if len(trials) < 2 {
			// Not enough forks survived: unwind the partial trials and answer
			// directly instead of leaving a dangling experiment.
			for _, t := range trials {
				cancelRequestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/cancel/%s", sessionID, interactionID, t.SessionID))
				if err := adapter.Cancel(t.SessionID, cancelRequestID); err != nil {
					e.recordDiagnostic("autoanswer_cancel", err)
				}
			}
			return submitInfer("experiment isolation produced fewer than two trials; " + rationale)
		}
		rollbackIDs := make([]string, 0, len(trials))
		for _, t := range trials {
			rollbackIDs = append(rollbackIDs, t.SessionID)
		}
		e.recordDecision(InteractionDecisionRecord{
			ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/experiment", a.ID, sessionID, interactionID)),
			AssistantID:   a.ID,
			SessionID:     sessionID,
			InteractionID: interactionID,
			Source:        DecisionExperiment,
			Confidence:    confidence,
			Candidates:    candidates,
			Rationale:     rationale,
			Result:        "running",
			Rollback:      strings.Join(rollbackIDs, ","),
			Trials:        trials,
			DueAt:         dueAt,
		})
		// The experiment's candidate labels are every distinct option raced
		// across the trials (not just the model's inferred selection), so the
		// durable record reflects the full candidate set.
		expCandidates := make([]string, 0, len(trials))
		for _, t := range trials {
			answers, err := DecodeTrialAnswer(t.Answer)
			if err != nil {
				continue
			}
			for _, ans := range answers {
				for _, label := range ans.Selected {
					if !containsString(expCandidates, label) {
						expCandidates = append(expCandidates, label)
					}
				}
			}
		}
		// Durable experiment log: the full candidate/session/worktree/status
		// set with a revision fence, so the winner comparison and rollback
		// survive ticks and restarts even before the sweep settles the race.
		e.recordExperiment(Experiment{
			ID:          StableID("experiment", fmt.Sprintf("%s/%s/%s", a.ID, sessionID, interactionID)),
			AssistantID: a.ID,
			Hypothesis:  prompt,
			Isolation:   "session",
			Metric:      "fork session completed / preference order",
			Candidates:  expCandidates,
			Trials:      trials,
			Result:      "running",
			Confidence:  confidence,
			Rollback:    strings.Join(rollbackIDs, ","),
			Status:      ExperimentRunning,
		}, "start")
		return autoAnswerOutcome{applied: true}
	default:
		// DecisionUser (irreversible fail-closed): leave the whole batch for the user.
		e.recordDecision(InteractionDecisionRecord{
			ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/user", a.ID, sessionID, interactionID)),
			AssistantID:   a.ID,
			SessionID:     sessionID,
			InteractionID: interactionID,
			Source:        DecisionUser,
			HardGate:      decision.HardGate,
			Confidence:    confidence,
			Candidates:    candidates,
			Rationale:     decision.Rationale,
			Result:        "wait_for_user",
			DueAt:         dueAt,
		})
		return autoAnswerOutcome{hardGate: true}
	}
}

// resolveExperimentTrials sweeps every active Assistant's decisions for running
// experiments (Source==experiment with recorded Trials and no settled outcome)
// and advances them: only when EVERY trial is terminal does it compare — the
// first completed candidate in preference order answers the original session
// and every other trial is cancelled, or, when no candidate completed (all
// failed or timed out), the most rollback-safe inferred answer continues so
// the original session is never permanently pending. It is idempotent — a
// decision whose Result already records a settled outcome is skipped, and the
// winner/fallback decision is persisted with a stable ID so replays and
// duplicate completion observations cannot double-answer or regress.
func (e *SupervisorExecutor) resolveExperimentTrials() {
	if e == nil || e.store == nil {
		return
	}
	adapter := e.sessionControl()
	if adapter == nil {
		return
	}
	assistants, err := e.store.List()
	if err != nil {
		e.recordDiagnostic("experiment_list", err)
		return
	}
	for _, a := range assistants {
		if a.Lifecycle != LifecycleActive {
			continue
		}
		snapshot, err := e.store.Get(a.ID)
		if err != nil {
			continue
		}
		// The latest experiment decision per interaction wins. Decisions are
		// appended in recording order, so the last one for a key is the newest.
		latest := make(map[string]InteractionDecisionRecord)
		for _, rec := range snapshot.Decisions {
			if rec.Source != DecisionExperiment {
				continue
			}
			key := rec.SessionID + "\x00" + rec.InteractionID
			latest[key] = rec
		}
		for _, rec := range latest {
			if isSettledExperimentResult(rec.Result) {
				continue
			}
			if len(rec.Trials) == 0 {
				continue
			}
			e.resolveExperimentWinner(a, rec, adapter)
		}
	}
}

// resolveExperimentWinner advances one running experiment decision: it asks the
// trial status resolver which forks have settled, and only when EVERY trial is
// terminal does it compare and act — the first completed candidate in
// preference order answers the original session and every other fork is
// cancelled, or, when no candidate completed (all failed or timed out), the
// most rollback-safe inferred candidate answers the original session directly
// so it is never left permanently pending. The outcome is persisted with
// stable decision and experiment IDs, so duplicated or out-of-order completion
// observations cannot double-answer or regress a settled race.
func (e *SupervisorExecutor) resolveExperimentWinner(a Assistant, rec InteractionDecisionRecord, adapter SessionControl) {
	resolver := e.trialStatusResolver()
	if resolver == nil {
		return // no status source: keep the experiment running
	}
	now := time.Now()
	res := ResolveExperiment(rec.Trials, resolver, now, rec.CreatedAt, e.experimentMaxAge)
	if len(res.Pending) > 0 {
		// A candidate is still racing: keep the experiment running. The
		// deadline bound (experimentMaxAge) guarantees this cannot last
		// forever — an over-age fork is timed out on a later sweep.
		return
	}
	trials := markTrialStatuses(rec.Trials, resolver, now, rec.CreatedAt, e.experimentMaxAge)
	cost := experimentCostSummary(trials, res)
	sideEffects := "cancelled=" + strings.Join(res.Losers, ",")

	if res.HasWinner {
		winnerAnswers, err := DecodeTrialAnswer(res.Winner.Answer)
		if err != nil {
			e.recordDiagnostic("experiment_winner_answer", err)
			return
		}
		requestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/winner/%s", rec.SessionID, rec.InteractionID, res.Winner.SessionID))
		if err := adapter.AnswerQuestion(rec.SessionID, rec.InteractionID, winnerAnswers, requestID); err != nil {
			e.recordDiagnostic("experiment_winner_submit", err)
			return
		}
		for _, loser := range res.Losers {
			cancelRequestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/cancel/%s", rec.SessionID, rec.InteractionID, loser))
			if err := adapter.Cancel(loser, cancelRequestID); err != nil {
				e.recordDiagnostic("experiment_cancel", err)
			}
		}
		evidence := fmt.Sprintf("all candidates terminal; completed compared in preference order; winner=%s", res.Winner.SessionID)
		e.recordDecision(InteractionDecisionRecord{
			ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/experiment/winner", a.ID, rec.SessionID, rec.InteractionID)),
			AssistantID:   a.ID,
			SessionID:     rec.SessionID,
			InteractionID: rec.InteractionID,
			Source:        DecisionExperiment,
			Confidence:    rec.Confidence,
			Candidates:    rec.Candidates,
			Rationale:     rec.Rationale,
			Result:        "answered:" + res.Winner.SessionID,
			Rollback:      strings.Join(res.Losers, ","),
			Trials:        trials,
			Winner:        res.Winner.SessionID,
			Evidence:      evidence,
			Cost:          cost,
			SideEffects:   sideEffects,
			DueAt:         rec.DueAt,
			CreatedAt:     rec.CreatedAt.Add(time.Nanosecond),
		})
		e.recordExperiment(Experiment{
			ID:          StableID("experiment", fmt.Sprintf("%s/%s/%s", a.ID, rec.SessionID, rec.InteractionID)),
			AssistantID: a.ID,
			Hypothesis:  rec.Rationale,
			Isolation:   "session",
			Metric:      "fork session completed / preference order",
			Conclusion:  "winner=" + res.Winner.SessionID,
			Candidates:  rec.Candidates,
			Trials:      trials,
			Result:      "answered:" + res.Winner.SessionID,
			Winner:      res.Winner.SessionID,
			Cost:        cost,
			SideEffects: sideEffects,
			Evidence:    evidence,
			Confidence:  rec.Confidence,
			Rollback:    strings.Join(res.Losers, ","),
			Status:      ExperimentConcluded,
		}, "winner")
		return
	}

	// Fallback: every candidate failed or timed out. Answer the original
	// interaction with the most rollback-safe candidate (the first = inferred
	// answer) and cancel every fork, so the original session is never left
	// permanently pending.
	fallbackAnswers, err := DecodeTrialAnswer(rec.Trials[0].Answer)
	if err != nil {
		e.recordDiagnostic("experiment_fallback_answer", err)
		return
	}
	fallbackRequestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/fallback", rec.SessionID, rec.InteractionID))
	if err := adapter.AnswerQuestion(rec.SessionID, rec.InteractionID, fallbackAnswers, fallbackRequestID); err != nil {
		e.recordDiagnostic("experiment_fallback_submit", err)
		return
	}
	for _, loser := range res.Losers {
		cancelRequestID := StableID("request", fmt.Sprintf("autoanswer/%s/%s/cancel/%s", rec.SessionID, rec.InteractionID, loser))
		if err := adapter.Cancel(loser, cancelRequestID); err != nil {
			e.recordDiagnostic("experiment_cancel", err)
		}
	}
	evidence := "no candidate completed (all failed or timed out); using the most rollback-safe inferred answer"
	e.recordDecision(InteractionDecisionRecord{
		ID:            StableID("decision", fmt.Sprintf("%s/%s/%s/experiment/fallback", a.ID, rec.SessionID, rec.InteractionID)),
		AssistantID:   a.ID,
		SessionID:     rec.SessionID,
		InteractionID: rec.InteractionID,
		Source:        DecisionExperiment,
		Confidence:    rec.Confidence,
		Candidates:    rec.Candidates,
		Rationale:     rec.Rationale + "; " + evidence,
		Result:        "answered-fallback",
		Rollback:      strings.Join(res.Losers, ","),
		Trials:        trials,
		Evidence:      evidence,
		Cost:          cost,
		SideEffects:   sideEffects,
		DueAt:         rec.DueAt,
		CreatedAt:     rec.CreatedAt.Add(time.Nanosecond),
	})
	e.recordExperiment(Experiment{
		ID:          StableID("experiment", fmt.Sprintf("%s/%s/%s", a.ID, rec.SessionID, rec.InteractionID)),
		AssistantID: a.ID,
		Hypothesis:  rec.Rationale,
		Isolation:   "session",
		Metric:      "fork session completed / preference order",
		Conclusion:  evidence,
		Candidates:  rec.Candidates,
		Trials:      trials,
		Result:      "answered-fallback",
		Cost:        cost,
		SideEffects: sideEffects,
		Evidence:    evidence,
		Confidence:  rec.Confidence,
		Rollback:    strings.Join(res.Losers, ","),
		Status:      ExperimentConcluded,
	}, "fallback")
}

// markTrialStatuses rewrites each trial's persisted status to its real
// terminal classification (done for completed forks, failed for failed/cancelled
// or timed-out forks). A completed fork that lost the preference race keeps
// Status done — the Winner field records which one won.
func markTrialStatuses(trials []TrialState, resolver TrialStatusResolver, now, startedAt time.Time, maxAge time.Duration) []TrialState {
	out := append([]TrialState(nil), trials...)
	for i := range out {
		st, found := resolver(out[i].SessionID)
		done, failed, timedOut := classifyTrialStatus(out[i].SessionID, st, found, now, startedAt, maxAge)
		switch {
		case done:
			out[i].Status = TrialStatusDone
		case failed, timedOut:
			out[i].Status = TrialStatusFailed
		}
	}
	return out
}

// experimentCostSummary is the bounded, observed cost summary of one settled
// race: fork count and how many forks completed, failed or timed out.
func experimentCostSummary(trials []TrialState, res ExperimentResolution) string {
	done, failed := 0, 0
	for _, t := range trials {
		switch t.Status {
		case TrialStatusDone:
			done++
		case TrialStatusFailed:
			failed++
		}
	}
	return fmt.Sprintf("forks=%d done=%d failed=%d timed_out=%d", len(trials), done, failed, len(res.TimedOut))
}

// recordExperiment persists one auto-answer experiment record idempotently with
// a revision fence: the start write creates it (expected revision 0) and each
// conclusion reads the current revision and updates under CAS, so a stale or
// replayed conclusion can never overwrite a newer edit. The stable request ID
// makes the write replay-safe; a record missing after a crash (decision
// written, experiment not) is created with the conclusion directly. The
// conclusion keeps the start's hypothesis/isolation/metric (a truthful audit
// trail), only overwriting the settled state fields.
func (e *SupervisorExecutor) recordExperiment(exp Experiment, phase string) {
	if e.store == nil {
		return
	}
	requestID := StableID("request", fmt.Sprintf("experiment/%s/%s", exp.ID, phase))
	expected := int64(0)
	if snap, err := e.store.Get(exp.AssistantID); err == nil {
		for _, ex := range snap.Experiments {
			if ex.ID == exp.ID {
				expected = ex.Revision
				if strings.TrimSpace(exp.Hypothesis) == "" {
					exp.Hypothesis = ex.Hypothesis
				}
				if strings.TrimSpace(exp.Isolation) == "" {
					exp.Isolation = ex.Isolation
				}
				if strings.TrimSpace(exp.Metric) == "" {
					exp.Metric = ex.Metric
				}
				break
			}
		}
	}
	if strings.TrimSpace(exp.Hypothesis) == "" {
		exp.Hypothesis = "auto-answer isolated trial " + exp.ID
	}
	if _, err := e.store.RecordExperiment(RecordExperimentInput{
		RequestID: requestID, Experiment: exp, ExpectedRevision: expected, Now: time.Now(),
	}); err != nil {
		e.recordDiagnostic("experiment_record", err)
	}
}

// isSettledExperimentResult reports whether an experiment decision already
// answered (or fallback-answered) its interaction, so the winner sweep skips it
// instead of re-answering on duplicate or out-of-order completion observations.
func isSettledExperimentResult(result string) bool {
	return strings.HasPrefix(result, "answered:") || result == "answered-fallback"
}

// recordDecision persists a decision through the store (idempotent receipt).
func (e *SupervisorExecutor) recordDecision(rec InteractionDecisionRecord) {
	if e.store == nil {
		return
	}
	if _, err := e.store.RecordInteractionDecision(rec); err != nil {
		e.recordDiagnostic("autoanswer_decision", err)
	}
}

// interactionPrompt joins a batch's question prompts into one bounded string
// for hard-gate classification and routing.
func interactionPrompt(questions []event.AskQuestion) string {
	prompts := make([]string, 0, len(questions))
	for _, q := range questions {
		if p := strings.TrimSpace(q.Prompt); p != "" {
			prompts = append(prompts, p)
		}
	}
	return strings.Join(prompts, " ")
}

func containsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
