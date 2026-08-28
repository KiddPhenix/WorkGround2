package assistant

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SupervisorAction is the bounded next action the supervisor decided to take.
type SupervisorAction string

const (
	// ActionAdvance selects one executable responsibility to work on.
	ActionAdvance SupervisorAction = "advance"
	// ActionAnswer answers a pending interaction on a managed session.
	ActionAnswer SupervisorAction = "answer"
	// ActionExpand enters the expansion loop (no executable work left).
	ActionExpand SupervisorAction = "expand"
	// ActionAdopt auto-adopts one evidence-backed high-value opportunity into a
	// planned responsibility and starts its managed Session (continuous mode,
	// policy-gated). The target is the opportunity ID.
	ActionAdopt SupervisorAction = "adopt"
	// ActionSteer inserts guidance into a managed session.
	ActionSteer SupervisorAction = "steer"
	// ActionWait means nothing is actionable right now.
	ActionWait SupervisorAction = "wait"
)

func validSupervisorAction(a SupervisorAction) bool {
	switch a {
	case ActionAdvance, ActionAnswer, ActionExpand, ActionAdopt, ActionSteer, ActionWait:
		return true
	}
	return false
}

// SupervisorDecision is the bounded single-turn decision of the supervisor: one
// action, an optional target (responsibility alias or session ID), and a short
// rationale. The decision is auditable, never a hidden imperative.
type SupervisorDecision struct {
	Action    SupervisorAction `json:"action"`
	Target    string           `json:"target,omitempty"`
	Rationale string           `json:"rationale,omitempty"`
}

// Supervisor runs one bounded model turn that decides the next action from the
// implicit context. It is the supervisor reasoning step of the loop: it never
// executes the action itself, so the loop can record, fence, and route the
// decision before any side effect.
//
// NOTE: the production supervisor loop no longer reasons out-of-session
// (Supervisor.Reason was removed): the supervisor turn now runs through the
// assistant's durable Purpose=supervisor Session Controller
// (SupervisorExecutor), and only the bounded decision vocabulary below is
// shared between the Session turn and the acting phase.

const supervisorDecisionInstruction = `

请基于以上状态决定下一步，并只输出一个 JSON 对象（不要额外文字）：
{"action":"advance|answer|expand|adopt|steer|wait","target":"责任别名或 Session ID 或机会 ID","rationale":"一句话理由"}
action 含义：advance=推进一项可执行责任；answer=代答受管 Session 的待回答问题；expand=无剩余工作，进入扩展；adopt=自动采纳一个有证据的高价值机会（continuous 模式，target 为机会 ID）；steer=指导受管 Session；wait=当前无可执行工作。`

// ParseSupervisorDecision extracts and validates one bounded decision from model
// output. A malformed or over-bounded output yields an explicit error so a bad
// decision is never silently dropped.
func ParseSupervisorDecision(text string) (SupervisorDecision, error) {
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start < 0 || end <= start {
		return SupervisorDecision{}, errors.New("assistant: supervisor output has no JSON object")
	}
	raw := text[start : end+1]
	if len(raw) > 4096 {
		return SupervisorDecision{}, errors.New("assistant: supervisor output exceeds size limit")
	}
	var decision SupervisorDecision
	if err := json.Unmarshal([]byte(raw), &decision); err != nil {
		return SupervisorDecision{}, fmt.Errorf("assistant: malformed supervisor decision: %w", err)
	}
	if !validSupervisorAction(decision.Action) {
		return SupervisorDecision{}, fmt.Errorf("assistant: invalid supervisor action %q", decision.Action)
	}
	decision.Target = strings.TrimSpace(decision.Target)
	decision.Rationale = strings.TrimSpace(decision.Rationale)
	if decision.Action == ActionAdvance || decision.Action == ActionAnswer || decision.Action == ActionSteer || decision.Action == ActionAdopt {
		if decision.Target == "" {
			return SupervisorDecision{}, fmt.Errorf("assistant: supervisor action %s requires a target", decision.Action)
		}
	}
	return decision, nil
}
