package assistant

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"workground2/internal/event"
)

// Trial status vocabulary persisted on an isolated experiment trial. A fork is
// running (still racing), done (completed and, once the race settles, the
// winner) or failed (the fork session failed/cancelled, or the trial timed
// out). A failed trial can never win; when every trial is terminal and none
// completed, the sweep falls back to the most rollback-safe candidate so the
// original session is never left pending forever.
const (
	TrialStatusRunning = "running"
	TrialStatusDone    = "done"
	TrialStatusFailed  = "failed"
)

// TrialState is the durable status of one isolated experiment candidate fork.
// Answer carries the serialized []event.AskAnswer batch submitted to that fork,
// so the winning answer can be replayed onto the original session after a
// restart without re-running the model. Worktree records the isolation
// location the host used for this candidate ("session:<id>" for a fork
// Session, or a real worktree/sandbox path when the host provides one) — every
// candidate must be isolated on its own, never sharing another candidate's
// workspace.
type TrialState struct {
	SessionID string `json:"session_id"`
	Worktree  string `json:"worktree,omitempty"`
	Answer    string `json:"answer"`
	Status    string `json:"status"`
}

// TrialStatusResolver resolves one trial fork session's derived status, or
// ok=false when the session cannot be located. The desktop host backs this with
// agent.ListSessions + DeriveSessionStatus; tests inject a fake.
type TrialStatusResolver func(sessionID string) (status string, ok bool)

// EncodeTrialAnswer serializes a batch answer set for durable TrialState
// storage. It is the inverse of DecodeTrialAnswer.
func EncodeTrialAnswer(answers []event.AskAnswer) string {
	b, err := json.Marshal(answers)
	if err != nil {
		return ""
	}
	return string(b)
}

// DecodeTrialAnswer restores a batch answer set persisted by EncodeTrialAnswer.
func DecodeTrialAnswer(raw string) ([]event.AskAnswer, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("assistant: trial answer is empty")
	}
	var answers []event.AskAnswer
	if err := json.Unmarshal([]byte(raw), &answers); err != nil {
		return nil, err
	}
	return answers, nil
}

// BuildExperimentCandidates derives the isolated-trial candidate answer sets for
// a pending ask batch. It starts from the model's inferred answers (already
// validated option labels), then adds one variant per alternative option label,
// so a multi-option question races each plausible label. When the question set
// offers fewer than two distinct options it falls back to a "user-verbatim"
// candidate carrying the question prompt as a free-typed answer, so an
// experiment always has at least two candidates to race.
func BuildExperimentCandidates(questions []event.AskQuestion, inferred []event.AskAnswer) [][]event.AskAnswer {
	if len(questions) == 0 {
		return nil
	}

	base := make([]event.AskAnswer, len(questions))
	for i, q := range questions {
		if i < len(inferred) {
			base[i] = inferred[i]
		}
		if base[i].QuestionID == "" {
			base[i].QuestionID = q.ID
		}
		if len(base[i].Selected) == 0 && len(q.Options) > 0 {
			base[i].Selected = []string{strings.TrimSpace(q.Options[0].Label)}
		}
	}

	out := [][]event.AskAnswer{cloneTrialAnswers(base)}
	seen := map[string]bool{EncodeTrialAnswer(base): true}

	for i, q := range questions {
		for _, opt := range q.Options {
			label := strings.TrimSpace(opt.Label)
			if label == "" {
				continue
			}
			if len(base[i].Selected) == 1 && base[i].Selected[0] == label {
				continue
			}
			variant := cloneTrialAnswers(base)
			variant[i] = event.AskAnswer{QuestionID: q.ID, Selected: []string{label}}
			key := EncodeTrialAnswer(variant)
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, variant)
		}
	}

	if len(out) < 2 {
		verbatim := cloneTrialAnswers(base)
		for i, q := range questions {
			selected := strings.TrimSpace(q.Prompt)
			if selected == "" && len(q.Options) > 0 {
				selected = strings.TrimSpace(q.Options[0].Label)
			}
			verbatim[i] = event.AskAnswer{QuestionID: q.ID, Selected: []string{selected}}
		}
		if key := EncodeTrialAnswer(verbatim); !seen[key] {
			out = append(out, verbatim)
		}
	}
	return out
}

func cloneTrialAnswers(answers []event.AskAnswer) []event.AskAnswer {
	out := make([]event.AskAnswer, len(answers))
	for i, a := range answers {
		out[i] = a
		out[i].Selected = append([]string(nil), a.Selected...)
	}
	return out
}

// ExperimentResolution is the bounded outcome of one experiment race sweep.
// The sweep only settles when every trial is terminal (done, failed, or timed
// out); a still-running trial keeps the race open so a preferred candidate is
// never cancelled while it is still racing.
type ExperimentResolution struct {
	// Winner is the winning trial (Status == done) picked by comparing the
	// completed candidates in preference order; HasWinner is false when no
	// candidate completed.
	Winner    TrialState
	HasWinner bool
	// Losers are the trial session IDs that must be cancelled / rolled back.
	Losers []string
	// Fallback is true when every trial is terminal but none completed: the
	// caller must answer the original interaction with the first (inferred,
	// most rollback-safe) candidate so the original session never stays
	// pending.
	Fallback bool
	// Pending lists the still-running trial session IDs; TimedOut lists the
	// trials that exceeded the experiment max age (treated as terminal).
	Pending  []string
	TimedOut []string
}

// ResolveExperiment classifies every trial of a race and reports whether the
// race settled. The trial status resolver reports the REAL fork session state
// (done / failed / cancelled / running), never a fake result; a session the
// host can no longer locate counts as failed. A still-running trial past the
// experiment max age is timed out (terminal). Only when no trial remains
// pending does the caller compare and act, so a duplicated or out-of-order
// completion observation can never regress an already-settled race (the
// caller skips settled decisions by their stable result).
func ResolveExperiment(trials []TrialState, status TrialStatusResolver, now, startedAt time.Time, maxAge time.Duration) ExperimentResolution {
	var res ExperimentResolution
	var completed []TrialState
	for _, t := range trials {
		st, found := status(t.SessionID)
		done, failed, timedOut := classifyTrialStatus(t.SessionID, st, found, now, startedAt, maxAge)
		switch {
		case done:
			completed = append(completed, t)
		case failed, timedOut:
			res.TimedOut = append(res.TimedOut, t.SessionID)
		default:
			res.Pending = append(res.Pending, t.SessionID)
		}
	}
	if len(res.Pending) > 0 {
		return res // the race is still open: never settle while a candidate races
	}
	if len(completed) > 0 {
		res.Winner = completed[0] // preference order: the first completed candidate wins
		res.HasWinner = true
	}
	for _, t := range trials {
		if res.HasWinner && t.SessionID == res.Winner.SessionID {
			continue
		}
		res.Losers = append(res.Losers, t.SessionID)
	}
	res.Fallback = !res.HasWinner
	return res
}

// classifyTrialStatus maps one trial's real session status plus its age to the
// race vocabulary. "completed"/done is a winner candidate; failed/cancelled,
// a vanished session, and an over-age running trial are all terminal; anything
// else is still racing.
func classifyTrialStatus(sessionID, st string, found bool, now, startedAt time.Time, maxAge time.Duration) (done, failed, timedOut bool) {
	if !found {
		return false, true, false // the host can no longer locate the fork: terminal
	}
	switch st {
	case TrialStatusDone, "completed":
		return true, false, false
	case TrialStatusFailed, "cancelled":
		return false, true, false
	}
	if maxAge > 0 && !startedAt.IsZero() && now.Sub(startedAt) > maxAge {
		return false, false, true
	}
	return false, false, false
}
