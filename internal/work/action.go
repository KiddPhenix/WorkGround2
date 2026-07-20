package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// ActionRisk is the canonical side-effect class of an action handler.
type ActionRisk string

const (
	RiskRead        ActionRisk = "read"
	RiskWrite       ActionRisk = "write"
	RiskDestructive ActionRisk = "destructive"
	RiskExternal    ActionRisk = "external"
)

func validActionRisk(r ActionRisk) bool {
	switch r {
	case RiskRead, RiskWrite, RiskDestructive, RiskExternal:
		return true
	default:
		return false
	}
}

// ActionReceiptStatus is the persisted execution state.
type ActionReceiptStatus string

const (
	ActionPending   ActionReceiptStatus = "pending"
	ActionRunning   ActionReceiptStatus = "running"
	ActionSucceeded ActionReceiptStatus = "succeeded"
	ActionFailed    ActionReceiptStatus = "failed"
	ActionRejected  ActionReceiptStatus = "rejected"
	ActionUnknown   ActionReceiptStatus = "unknown"
)

func terminalReceipt(s ActionReceiptStatus) bool {
	switch s {
	case ActionSucceeded, ActionFailed, ActionRejected, ActionUnknown:
		return true
	default:
		return false
	}
}

func validActionTransition(from, to ActionReceiptStatus) bool {
	switch from {
	case ActionPending:
		return to == ActionRunning || to == ActionFailed || to == ActionRejected
	case ActionRunning:
		return to == ActionSucceeded || to == ActionFailed || to == ActionUnknown
	default:
		return false
	}
}

// ActionHandlerContext contains only canonical data resolved by Service.
type ActionHandlerContext struct {
	WorkID      string
	BlockID     string
	ActionID    string
	RequestID   string
	Input       map[string]any
	Payload     json.RawMessage
	Fingerprint string
	Risk        ActionRisk
}

// ActionResult is a typed handler result. UnknownOutcome is meaningful when
// the handler also returns an error: it prevents unsafe replay after an
// external timeout whose remote outcome cannot be proved.
type ActionResult struct {
	Data           json.RawMessage `json:"data,omitempty"`
	Message        string          `json:"message,omitempty"`
	Retryable      bool            `json:"retryable,omitempty"`
	UnknownOutcome bool            `json:"unknownOutcome,omitempty"`
}

// ActionHandler executes one trusted action definition.
type ActionHandler func(context.Context, ActionHandlerContext) (*ActionResult, error)

// ActionRegistration is the trusted definition for one block-kind/action-ID
// pair. Intent, risk, payload and handler never come from the frontend or the
// mutable BlockInstance.
type ActionRegistration struct {
	BlockKind       string
	ActionID        string
	Intent          string
	Summary         string
	Risk            ActionRisk
	ConfirmRequired bool
	Payload         json.RawMessage
	Handler         ActionHandler
}

type actionKey struct {
	blockKind string
	actionID  string
}

// ActionRegistry is a concurrency-safe registry of trusted action definitions.
type ActionRegistry struct {
	mu       sync.RWMutex
	handlers map[actionKey]ActionRegistration
}

// NewActionRegistry creates an empty trusted action registry.
func NewActionRegistry() *ActionRegistry {
	return &ActionRegistry{handlers: make(map[actionKey]ActionRegistration)}
}

// Register adds one immutable action definition.
func (r *ActionRegistry) Register(reg ActionRegistration) error {
	reg.BlockKind = strings.TrimSpace(reg.BlockKind)
	reg.ActionID = strings.TrimSpace(reg.ActionID)
	reg.Intent = strings.TrimSpace(reg.Intent)
	reg.Summary = strings.TrimSpace(reg.Summary)
	if reg.BlockKind == "" || reg.ActionID == "" || reg.Intent == "" {
		return fmt.Errorf("work: action register: blockKind, actionID and intent are required")
	}
	if reg.Handler == nil {
		return fmt.Errorf("work: action register: handler is nil for %s/%s", reg.BlockKind, reg.ActionID)
	}
	if !validActionRisk(reg.Risk) {
		return fmt.Errorf("work: action register: unknown risk %q for %s/%s", reg.Risk, reg.BlockKind, reg.ActionID)
	}
	if len(reg.Payload) > 0 {
		payload, err := canonicalJSON(reg.Payload)
		if err != nil {
			return fmt.Errorf("work: action register: invalid payload for %s/%s: %w", reg.BlockKind, reg.ActionID, err)
		}
		reg.Payload = payload
	}
	key := actionKey{blockKind: reg.BlockKind, actionID: reg.ActionID}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.handlers == nil {
		r.handlers = make(map[actionKey]ActionRegistration)
	}
	if _, exists := r.handlers[key]; exists {
		return fmt.Errorf("work: action register: %s/%s is already registered", reg.BlockKind, reg.ActionID)
	}
	r.handlers[key] = reg
	return nil
}

// Lookup returns a copy of the registered action definition.
func (r *ActionRegistry) Lookup(blockKind, actionID string) (ActionRegistration, bool) {
	if r == nil {
		return ActionRegistration{}, false
	}
	r.mu.RLock()
	reg, ok := r.handlers[actionKey{blockKind: blockKind, actionID: actionID}]
	r.mu.RUnlock()
	reg.Payload = append(json.RawMessage(nil), reg.Payload...)
	return reg, ok
}

func effectiveRisk(registered ActionRisk, declared string) (ActionRisk, error) {
	if !validActionRisk(registered) {
		return "", fmt.Errorf("work: action: handler has unknown registered risk %q", registered)
	}
	declared = strings.TrimSpace(declared)
	if declared == "" {
		return registered, nil
	}
	declaredRisk := ActionRisk(declared)
	if !validActionRisk(declaredRisk) {
		return "", fmt.Errorf("work: action: block declares unknown risk %q", declared)
	}
	return maxRisk(registered, declaredRisk), nil
}

var riskOrder = map[ActionRisk]int{
	RiskRead: 0, RiskWrite: 1, RiskExternal: 2, RiskDestructive: 3,
}

func maxRisk(a, b ActionRisk) ActionRisk {
	if riskOrder[a] >= riskOrder[b] {
		return a
	}
	return b
}

func riskRequiresApproval(r ActionRisk) bool { return r != RiskRead }

func actionInputDigest(workID, blockID, actionID string, input map[string]any) (string, error) {
	return digestCanonical(struct {
		WorkID   string         `json:"workId"`
		BlockID  string         `json:"blockId"`
		ActionID string         `json:"actionId"`
		Input    map[string]any `json:"input,omitempty"`
	}{workID, blockID, actionID, input})
}

func actionFingerprint(workID, blockID, actionID, blockKind, intent string, risk ActionRisk, confirm bool, payload json.RawMessage, input map[string]any) (string, error) {
	return digestCanonical(struct {
		WorkID    string          `json:"workId"`
		BlockID   string          `json:"blockId"`
		ActionID  string          `json:"actionId"`
		BlockKind string          `json:"blockKind"`
		Intent    string          `json:"intent"`
		Risk      ActionRisk      `json:"risk"`
		Confirm   bool            `json:"confirmRequired"`
		Payload   json.RawMessage `json:"payload,omitempty"`
		Input     map[string]any  `json:"input,omitempty"`
	}{workID, blockID, actionID, blockKind, intent, risk, confirm, payload, input})
}

func digestCanonical(value any) (string, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", digest[:]), nil
}

// ActionReceiptRecord is the event-derived, persisted idempotency record.
type ActionReceiptRecord struct {
	WorkID          string              `json:"workId"`
	BlockID         string              `json:"blockId"`
	BlockKind       string              `json:"blockKind"`
	ActionID        string              `json:"actionId"`
	Status          ActionReceiptStatus `json:"status"`
	Message         string              `json:"message,omitempty"`
	RequestID       string              `json:"requestId"`
	InputDigest     string              `json:"inputDigest"`
	Fingerprint     string              `json:"fingerprint"`
	Intent          string              `json:"intent"`
	Summary         string              `json:"summary,omitempty"`
	Risk            ActionRisk          `json:"risk"`
	ConfirmRequired bool                `json:"confirmRequired,omitempty"`
	Result          json.RawMessage     `json:"result,omitempty"`
	Retryable       bool                `json:"retryable"`
	OutcomeKnown    bool                `json:"outcomeKnown"`
	CreatedAt       time.Time           `json:"createdAt"`
	UpdatedAt       time.Time           `json:"updatedAt"`
}

func (r ActionReceiptRecord) toPublicReceipt(revision int64) *ActionReceipt {
	return &ActionReceipt{
		WorkID: r.WorkID, BlockID: r.BlockID, ActionID: r.ActionID,
		Status: string(r.Status), Message: r.Message, RequestID: r.RequestID,
		Fingerprint: r.Fingerprint, Result: append(json.RawMessage(nil), r.Result...),
		Retryable: r.Retryable, OutcomeKnown: r.OutcomeKnown, Revision: revision,
	}
}

func findActionReceipt(receipts []ActionReceiptRecord, requestID string) (ActionReceiptRecord, int, bool) {
	for i, receipt := range receipts {
		if receipt.RequestID == requestID {
			return receipt, i, true
		}
	}
	return ActionReceiptRecord{}, -1, false
}

func upsertActionReceipt(receipts []ActionReceiptRecord, receipt ActionReceiptRecord) []ActionReceiptRecord {
	_, index, found := findActionReceipt(receipts, receipt.RequestID)
	if found {
		receipts[index] = receipt
		return receipts
	}
	return append(receipts, receipt)
}
