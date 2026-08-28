package assistanttool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"workground2/internal/assistant"
)

type policyResult struct {
	Status   string            `json:"status"`
	Policy   *assistant.Policy `json:"policy,omitempty"`
	Revision int64             `json:"revision,omitempty"`
	Message  string            `json:"message,omitempty"`
}

func (r policyResult) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return fmt.Sprintf(`{"status":"retryable_error","message":%q}`, err.Error())
	}
	return string(b)
}

func policyError(status string, err error) string {
	return policyResult{Status: status, Message: err.Error()}.String()
}

type policyTool struct {
	store       *assistant.Store
	assistantID string
}

func newPolicyTool(store *assistant.Store, assistantID string) *policyTool {
	return &policyTool{store: store, assistantID: assistantID}
}

// policyArgs is the wire form of the assistant-editable policy. Each access
// field is deny, allow, or approve; omitted fields keep their current value.
// external_voice_enabled is deliberately NOT here: it is the user's direct
// switch and is never exposed to the Assistant itself.
type policyArgs struct {
	LocalWrite *string `json:"local_write"`
	Network    *string `json:"network"`
	Publish    *string `json:"publish"`
	Delete     *string `json:"delete"`
	Payment    *string `json:"payment"`
	Secrets    *string `json:"secrets"`
	Private    *string `json:"private_data"`
	// Isolation gates reversible multi-candidate trials (deny/allow/approve).
	Isolation *string `json:"isolation"`
	// MaxConcurrentSessions caps parallel managed Sessions; lowering is a
	// tightening, raising is a widening and is refused.
	MaxConcurrentSessions *int `json:"max_concurrent_sessions"`
	// AutoAnswer is the ordinary-question strategy: "auto" or "ask". Moving to
	// ask (wait for the user) is a tightening; back to auto is a widening and
	// is refused.
	AutoAnswer *string `json:"auto_answer"`
}

func accessValue(v *string, current assistant.Access) assistant.Access {
	if v == nil {
		return current
	}
	return assistant.Access(strings.TrimSpace(*v))
}

// accessRank orders the permission dimensions so a widening is a pure
// comparison: deny < allow < approve. An empty value (legacy) counts as the
// policy default via normalize before comparison.
func accessRank(a assistant.Access) int {
	switch assistant.Access(strings.TrimSpace(string(a))) {
	case assistant.AccessDeny:
		return 0
	case assistant.AccessAllow:
		return 1
	case assistant.AccessApprove:
		return 2
	}
	return 1 // unknown/empty treated as allow for ordering safety
}

// policyWidening reports whether next grants anything beyond current. The
// Assistant may only tighten its own policy; any widening is refused
// (blocked_by_policy) before the CAS so a natural-language ask can never
// escalate permissions.
func policyWidening(current, next assistant.Policy) bool {
	for _, dim := range []struct {
		cur, nxt assistant.Access
	}{
		{current.LocalWrite, next.LocalWrite}, {current.Network, next.Network},
		{current.Publish, next.Publish}, {current.Delete, next.Delete},
		{current.Payment, next.Payment}, {current.Secrets, next.Secrets},
		{current.Private, next.Private}, {current.ConstraintEdit, next.ConstraintEdit},
		{current.Isolation, next.Isolation},
	} {
		if accessRank(dim.nxt) > accessRank(dim.cur) {
			return true
		}
	}
	if next.MaxConcurrentSessions > current.MaxConcurrentSessions {
		return true
	}
	if next.AutoAnswer == assistant.AutoAnswerAuto && current.AutoAnswer == assistant.AutoAnswerAsk {
		return true
	}
	return false
}

// ---- assistant_policy_get ---------------------------------------------------

type policyGetTool struct{ *policyTool }

// NewPolicyGetTool reads the Assistant's current policy.
func NewPolicyGetTool(store *assistant.Store, assistantID string) *policyGetTool {
	return &policyGetTool{policyTool: newPolicyTool(store, assistantID)}
}

func (t *policyGetTool) Name() string   { return "assistant_policy_get" }
func (t *policyGetTool) ReadOnly() bool { return true }

func (t *policyGetTool) Description() string {
	return "Read the Assistant's permission policy: local write, network, publish, delete, payment, secrets, and private-data access (each deny/allow/approve). Use assistant_policy_update to change it; the Assistant can never widen its own policy."
}

func (t *policyGetTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"}},"required":[]}`)
}

func (t *policyGetTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	id := t.assistantID
	var in struct {
		AssistantID string `json:"assistant_id"`
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &in)
	}
	if strings.TrimSpace(in.AssistantID) != "" {
		id = strings.TrimSpace(in.AssistantID)
	}
	snapshot, err := t.store.Get(id)
	if err != nil {
		return policyError("retryable_error", err), nil
	}
	policy := snapshot.Assistant.Policy
	return policyResult{Status: "accepted", Policy: &policy, Revision: snapshot.Assistant.Revision}.String(), nil
}

// ---- assistant_policy_update ------------------------------------------------

type policyUpdateTool struct{ *policyTool }

// NewPolicyUpdateTool updates the Assistant's policy under revision CAS.
func NewPolicyUpdateTool(store *assistant.Store, assistantID string) *policyUpdateTool {
	return &policyUpdateTool{policyTool: newPolicyTool(store, assistantID)}
}

func (t *policyUpdateTool) Name() string   { return "assistant_policy_update" }
func (t *policyUpdateTool) ReadOnly() bool { return false }

func (t *policyUpdateTool) Description() string {
	return "Update the Assistant's permission policy. Only the fields you pass change; others keep their current value. Pass expected_revision to reject a stale edit, and a stable request_id for replay safety. An Assistant can only tighten, never widen, its own policy beyond the user's current grant: raising an access level, raising max_concurrent_sessions, or switching auto_answer back to auto are all refused. external_voice_enabled is user-only and not exposed here."
}

func (t *policyUpdateTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{"assistant_id":{"type":"string"},"local_write":{"type":"string"},"network":{"type":"string"},"publish":{"type":"string"},"delete":{"type":"string"},"payment":{"type":"string"},"secrets":{"type":"string"},"private_data":{"type":"string"},"isolation":{"type":"string"},"max_concurrent_sessions":{"type":"integer"},"auto_answer":{"type":"string"},"expected_revision":{"type":"integer"},"request_id":{"type":"string"}},"required":["request_id"]}`)
}

func (t *policyUpdateTool) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var in struct {
		AssistantID           string  `json:"assistant_id"`
		LocalWrite            *string `json:"local_write"`
		Network               *string `json:"network"`
		Publish               *string `json:"publish"`
		Delete                *string `json:"delete"`
		Payment               *string `json:"payment"`
		Secrets               *string `json:"secrets"`
		Private               *string `json:"private_data"`
		Isolation             *string `json:"isolation"`
		MaxConcurrentSessions *int    `json:"max_concurrent_sessions"`
		AutoAnswer            *string `json:"auto_answer"`
		ExpectedRevision      int64   `json:"expected_revision"`
		RequestID             string  `json:"request_id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return policyError("invalid", err), nil
	}
	id := t.assistantID
	if strings.TrimSpace(in.AssistantID) != "" {
		id = strings.TrimSpace(in.AssistantID)
	}
	if in.RequestID == "" {
		return policyError("invalid", fmt.Errorf("request_id is required")), nil
	}
	snapshot, err := t.store.Get(id)
	if err != nil {
		return policyError("retryable_error", err), nil
	}
	current := snapshot.Assistant
	next := current.Policy
	next.LocalWrite = accessValue(in.LocalWrite, next.LocalWrite)
	next.Network = accessValue(in.Network, next.Network)
	next.Publish = accessValue(in.Publish, next.Publish)
	next.Delete = accessValue(in.Delete, next.Delete)
	next.Payment = accessValue(in.Payment, next.Payment)
	next.Secrets = accessValue(in.Secrets, next.Secrets)
	next.Private = accessValue(in.Private, next.Private)
	next.Isolation = accessValue(in.Isolation, next.Isolation)
	if in.MaxConcurrentSessions != nil {
		next.MaxConcurrentSessions = *in.MaxConcurrentSessions
	}
	if in.AutoAnswer != nil {
		next.AutoAnswer = assistant.AutoAnswerPolicy(strings.TrimSpace(*in.AutoAnswer))
	}
	// The Assistant can never widen its own policy (design 13.3 / gap F). The
	// check runs before the CAS so a stale or malicious edit cannot escalate.
	if policyWidening(current.Policy, next) {
		return policyError("blocked_by_policy", fmt.Errorf("the Assistant cannot widen its own policy; only tightening is allowed")), nil
	}
	current.Policy = next
	updated, err := t.store.UpdateAssistant(in.RequestID, current, in.ExpectedRevision, time.Now().UTC())
	if err != nil {
		return policyError(mapStoreError(err), err), nil
	}
	policy := updated.Policy
	return policyResult{Status: "accepted", Policy: &policy, Revision: updated.Revision}.String(), nil
}
