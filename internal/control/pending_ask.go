package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"workground2/internal/event"
	"workground2/internal/fileutil"
	"workground2/internal/store"
)

// persistedAsk is the durable form of the one structured ask currently blocking
// a turn on a user decision. It lives beside the session transcript so a restart
// can re-project the exact question and, once answered, resume the same session
// instead of silently dropping the prompt.
type persistedAsk struct {
	ID        string              `json:"id"`
	Questions []event.AskQuestion `json:"questions"`
}

// persistPendingAsk atomically writes the pending ask sidecar before the
// AskRequest is emitted. A failure is returned to the caller so a prompt that
// could not be recovered is never shown.
func (c *Controller) persistPendingAsk(id string, questions []event.AskQuestion) error {
	path := store.SessionPendingAsk(c.SessionPath())
	if path == "" {
		return nil // persistence disabled: the in-memory prompt still works
	}
	data, err := json.MarshalIndent(persistedAsk{ID: id, Questions: questions}, "", "  ")
	if err != nil {
		return err
	}
	return fileutil.AtomicWriteFile(path, data, 0o600)
}

// clearPendingAskSidecar removes the durable pending-ask record. It is
// idempotent and safe to call from the answer and explicit-cancel converge
// paths. Context timeout/shutdown deliberately keeps the record recoverable.
// The id is logged for correlation but the sidecar is per-session:
// promptMu guarantees at most one ask is ever pending.
func (c *Controller) clearPendingAskSidecar(id string) {
	path := store.SessionPendingAsk(c.SessionPath())
	if path == "" {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		slog.Warn("controller: clear pending ask", "path", path, "id", id, "err", err)
	}
}

// loadPendingAsk re-hydrates a pending ask that survived a restart. It runs
// during Resume, before any turn can be submitted, so the restored ask is the
// single source of truth every frontend projection (TabMeta.PendingPrompt,
// widget PendingInteraction, ReplayPendingPrompts) reads from.
func (c *Controller) loadPendingAsk(sessionPath string) {
	path := store.SessionPendingAsk(sessionPath)
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("controller: load pending ask", "path", path, "err", err)
		}
		return
	}
	var p persistedAsk
	if err := json.Unmarshal(data, &p); err != nil || p.ID == "" || len(p.Questions) == 0 {
		// Corrupt or empty: nothing recoverable, so drop it and let the next ask
		// start clean instead of resurrecting a broken prompt forever.
		slog.Warn("controller: discard corrupt pending ask", "path", path, "err", err)
		c.notice("a pending question could not be restored because its saved state was damaged")
		_ = os.Remove(path)
		return
	}
	c.approval.restoreRecoveredAsk(p.ID, p.Questions)
	c.notice("restored a question that was waiting for your answer before the restart")
}

// transplantPendingAskSidecar follows a running turn when snapshot-conflict
// recovery moves the Controller onto a recovery branch. Leaving the record on
// the old branch would make the question disappear from the live Controller and
// later reappear as a ghost when the abandoned branch is opened again.
func (c *Controller) transplantPendingAskSidecar(fromPath, toPath string) {
	from := store.SessionPendingAsk(fromPath)
	to := store.SessionPendingAsk(toPath)
	if from == "" || to == "" || from == to {
		return
	}
	data, err := os.ReadFile(from)
	if err != nil {
		if !os.IsNotExist(err) {
			slog.Warn("controller: read pending ask for transplant", "path", from, "err", err)
			c.notice("the pending question could not follow the recovered session; it remains on the original session")
		}
		return
	}
	if existing, readErr := os.ReadFile(to); readErr == nil {
		if string(existing) == string(data) {
			_ = os.Remove(from)
			return
		}
		slog.Warn("controller: pending ask transplant target already exists", "from", from, "to", to)
		c.notice("the recovered session already has a different pending question; the original question was kept for recovery")
		return
	} else if !os.IsNotExist(readErr) {
		slog.Warn("controller: inspect pending ask transplant target", "path", to, "err", readErr)
		c.notice("the pending question could not follow the recovered session; it remains on the original session")
		return
	}
	if err := fileutil.AtomicWriteFile(to, data, 0o600); err != nil {
		slog.Warn("controller: transplant pending ask", "from", from, "to", to, "err", err)
		c.notice("the pending question could not follow the recovered session; it remains on the original session")
		return
	}
	if err := os.Remove(from); err != nil && !os.IsNotExist(err) {
		slog.Warn("controller: remove transplanted pending ask source", "path", from, "err", err)
	}
}

// startRecoveredAskTurn continues a recovered ask as a fresh synthetic turn once
// the user answers. It returns false when the turn could not be accepted so the
// caller can keep the question answerable instead of losing the answer.
func (c *Controller) startRecoveredAskTurn(id string, p pendingAsk, answers []event.AskAnswer) bool {
	input := askRecoveryPrompt(p.questions, answers)
	display := askRecoveryDisplay(p.questions, answers)
	return c.tryRunGuarded(func(ctx context.Context) error {
		err := newTurnOrchestrator(c).runSyntheticTurnWithRawDisplay(ctx, input, input, display)
		if err == nil {
			// Keep the sidecar until the recovery turn really completes. If the
			// process exits after the click but before completion, the question is
			// shown again instead of silently losing the user's decision.
			c.clearPendingAskSidecar(id)
			return nil
		}
		if !(errors.Is(err, context.Canceled) && c.CancelRequested()) {
			c.approval.restoreRecoveredAsk(id, p.questions)
			c.sink.Emit(event.Event{Kind: event.AskRequest, Ask: event.Ask{ID: id, Questions: p.questions}})
			c.notice("the recovered answer could not continue the task; the question is ready to retry")
		}
		return err
	})
}

func askRecoveryDisplay(questions []event.AskQuestion, answers []event.AskAnswer) string {
	pick := make(map[string][]string, len(answers))
	for _, answer := range answers {
		pick[answer.QuestionID] = answer.Selected
	}
	parts := make([]string, 0, len(questions))
	for _, question := range questions {
		label := strings.TrimSpace(question.Header)
		if label == "" {
			label = strings.TrimSpace(question.Prompt)
		}
		selected := pick[question.ID]
		value := "暂不选择"
		if len(selected) > 0 {
			value = strings.Join(selected, "、")
		}
		parts = append(parts, label+"："+value)
	}
	return "已回答重启前的问题：" + strings.Join(parts, "；")
}

// askRecoveryPrompt restates the recovered questions and the user's selections
// as a new turn. The interrupted turn that produced the original tool call was
// stripped, so the model is told to re-check the current workspace before acting
// on answers that may now be stale.
func askRecoveryPrompt(questions []event.AskQuestion, answers []event.AskAnswer) string {
	pick := make(map[string][]string, len(answers))
	for _, a := range answers {
		pick[a.QuestionID] = a.Selected
	}
	var b strings.Builder
	b.WriteString("The session was interrupted while the agent was waiting for your answer to a structured question, and the unfinished turn was removed. Here is the recovered question and your answer, so the work can continue in a fresh turn.\n\n")
	b.WriteString("Question asked:\n")
	for _, q := range questions {
		label := q.Header
		if label == "" {
			label = q.Prompt
		}
		fmt.Fprintf(&b, "- %s: %s\n", label, q.Prompt)
		for _, o := range q.Options {
			if o.Description != "" {
				fmt.Fprintf(&b, "    - %s (%s)\n", o.Label, o.Description)
			} else {
				fmt.Fprintf(&b, "    - %s\n", o.Label)
			}
		}
	}
	b.WriteString("\nYour answer:\n")
	for _, q := range questions {
		label := q.Header
		if label == "" {
			label = q.Prompt
		}
		sel := pick[q.ID]
		if len(sel) == 0 {
			fmt.Fprintf(&b, "- %s: (dismissed — do not decide for me; stop and wait for my next message)\n", label)
			continue
		}
		fmt.Fprintf(&b, "- %s: %s\n", label, strings.Join(sel, ", "))
	}
	b.WriteString("\nRe-examine the current workspace state before continuing. Treat the answers above as the user's intent and do not blindly apply them to stale files, results, or assumptions. If the current state makes the choice unsafe or no longer applicable, explain what changed and ask a new targeted question; otherwise continue without repeating the same question.")
	return b.String()
}
