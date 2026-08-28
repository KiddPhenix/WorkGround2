package assistant

import (
	"strings"
	"time"
)

// DecisionSource records how an interaction got answered (or will be answered).
// It is the auditable origin of a choice, not a hidden inference.
type DecisionSource string

const (
	// DecisionInfer auto-answers from evidence: the choice is reversible and
	// confident enough that waiting for the user would block progress.
	DecisionInfer DecisionSource = "infer"
	// DecisionExperiment runs several reversible candidates in isolated
	// sessions/worktrees and answers with the best-evidenced one.
	DecisionExperiment DecisionSource = "experiment"
	// DecisionUser is a hard gate: the interaction must wait for the user.
	DecisionUser DecisionSource = "user"
	// DecisionDeferred marks an interaction whose decision deadline has not
	// arrived yet: the runner parked it so the user (or a later tick) can
	// answer before the Assistant decides on its own.
	DecisionDeferred DecisionSource = "deferred"
)

// HardGateReason is why an interaction is a hard gate. Every reason is a
// concrete, closed enum value so the UI and diagnostics never parse prose.
type HardGateReason string

const (
	HardGateCredentials         HardGateReason = "credentials"
	HardGateIrreversible        HardGateReason = "irreversible_destructive"
	HardGateFundsLegalIdentity  HardGateReason = "funds_legal_identity"
	HardGatePolicyRequiresUser  HardGateReason = "policy_requires_user"
	HardGateUserRequiresConfirm HardGateReason = "user_requires_confirmation"
)

// defaultAutoAnswerDeadline is how long an ordinary interaction may wait before
// the Assistant must decide on its own instead of blocking indefinitely.
const defaultAutoAnswerDeadline = 15 * time.Minute

// defaultExperimentMaxAge bounds one isolated-trial race. A fork still running
// past this age is timed out (terminal), so the winner comparison or the
// rollback-safe fallback always settles and the original session is never left
// pending forever.
const defaultExperimentMaxAge = 15 * time.Minute

// inferConfidenceThreshold is the confidence above which a reversible choice is
// inferred directly. Below it a reversible choice is isolated-tried instead, so
// a low-confidence guess never mutates shared state.
const inferConfidenceThreshold = 0.8

// InteractionDecision is the bounded auto-response decision for one pending
// interaction. HardGate is set only when Source == DecisionUser; Confidence and
// Candidates carry the model's inference for infer/experiment decisions.
type InteractionDecision struct {
	Source     DecisionSource `json:"source"`
	HardGate   HardGateReason `json:"hard_gate,omitempty"`
	Confidence float64        `json:"confidence,omitempty"`
	Candidates []string       `json:"candidates,omitempty"`
	Rationale  string         `json:"rationale,omitempty"`
	DueAt      time.Time      `json:"due_at" ts_type:"string"`
}

// hardGateKeyword is one conservative signal that an interaction is a hard
// gate. The list is intentionally narrow and fail-closed: it only ever promotes
// a question to a hard gate, never auto-answers one. The model's inference is
// the authoritative choice-maker; these keywords are the safety boundary that
// keeps the model from answering funds/legal/identity/credentials/destructive
// questions without the user.
var hardGateKeyword = []struct {
	reason HardGateReason
	terms  []string
}{
	{HardGateFundsLegalIdentity, []string{"付款", "支付", "付款", "扣款", "订阅", "买", "价格", "金额", "法律", "诉讼", "合同", "身份", "实名", "身份证", "护照", "payment", "purchase", "subscribe", "legal", "contract", "identity", "passport"}},
	{HardGateCredentials, []string{"密码", "密钥", "令牌", "凭据", "apikey", "api key", "secret", "token", "password", "credential"}},
	{HardGateIrreversible, []string{"永久删除", "清空", "删除所有", "覆盖所有", "格式化", "不可恢复", "drop database", "rm -rf", "delete all", "format", "overwrite all", "permanently delete"}},
	{HardGateUserRequiresConfirm, []string{"必须确认", "总是问我", "永远确认", "需要我确认", "always ask", "always confirm", "require confirmation"}},
}

// ClassifyHardGate reports whether a pending interaction is a hard gate that
// must wait for the user, and why. The policy access mode (approve) is checked
// first, then the conservative keyword table. A non-match is not a hard gate:
// the model may infer or experiment, and only the closed hard-gate list ever
// waits for the user.
func ClassifyHardGate(action, prompt string, policy Policy) (HardGateReason, bool) {
	text := strings.ToLower(strings.TrimSpace(action + " " + prompt))

	if policy.Payment == AccessApprove && matchesAny(text, hardGateKeyword[0].terms) {
		return HardGateFundsLegalIdentity, true
	}
	if policy.Delete == AccessApprove && matchesAny(text, hardGateKeyword[2].terms) {
		return HardGateIrreversible, true
	}
	if policy.Secrets == AccessApprove || policy.Private == AccessApprove {
		if matchesAny(text, hardGateKeyword[1].terms) {
			return HardGateCredentials, true
		}
	}
	for _, gate := range hardGateKeyword {
		if matchesAny(text, gate.terms) {
			return gate.reason, true
		}
	}
	return "", false
}

func matchesAny(text string, terms []string) bool {
	for _, term := range terms {
		if strings.Contains(text, term) {
			return true
		}
	}
	return false
}

// AutoAnswerDueAt returns the decision deadline for an ordinary interaction.
// After this time the Assistant must decide (infer or experiment) instead of
// blocking indefinitely; hard gates are never auto-answered regardless of the
// deadline.
func AutoAnswerDueAt(now time.Time) time.Time {
	return utcNow(now).Add(defaultAutoAnswerDeadline)
}

// RouteInteractionInput is the bounded input to the auto-response router. The
// model's inferred candidates and confidence are supplied by the supervisor
// loop; the router only applies the policy-driven decision order from design
// section 9 — it never fabricates a choice on its own.
type RouteInteractionInput struct {
	Action     string
	Prompt     string
	Policy     Policy
	Confidence float64  // model-inferred confidence in [0,1]
	Candidates []string // model-inferred options
	Reversible bool     // whether the top choice can be rolled back
	CanIsolate bool     // whether an isolated trial (fork/worktree/sandbox) is possible
	Now        time.Time
}

// RouteInteraction decides how a pending interaction should be handled:
//
//  1. hard gate (funds/legal/identity/credentials/destructive/policy/user) —
//     wait for the user;
//  2. confident reversible choice — infer and answer;
//  3. low-confidence reversible choice with isolation — isolated trial;
//  4. low-confidence reversible choice without isolation — infer the most
//     reversible option;
//  5. low-confidence irreversible choice — wait for the user (fail closed).
func RouteInteraction(in RouteInteractionInput) InteractionDecision {
	now := utcNow(in.Now)
	if reason, gate := ClassifyHardGate(in.Action, in.Prompt, in.Policy); gate {
		return InteractionDecision{Source: DecisionUser, HardGate: reason}
	}
	if in.Confidence >= inferConfidenceThreshold {
		return InteractionDecision{
			Source: DecisionInfer, Confidence: in.Confidence,
			Candidates: in.Candidates, DueAt: AutoAnswerDueAt(now),
		}
	}
	if in.Reversible && in.CanIsolate {
		return InteractionDecision{
			Source: DecisionExperiment, Confidence: in.Confidence,
			Candidates: in.Candidates, DueAt: AutoAnswerDueAt(now),
		}
	}
	if in.Reversible {
		return InteractionDecision{
			Source: DecisionInfer, Confidence: in.Confidence,
			Candidates: in.Candidates, DueAt: AutoAnswerDueAt(now),
			Rationale: "low-confidence but reversible; choosing the most reversible option",
		}
	}
	return InteractionDecision{Source: DecisionUser, HardGate: HardGateIrreversible}
}
