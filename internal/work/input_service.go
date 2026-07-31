package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// ── InputService ────────────────────────────────────────────────────────────

// InputService owns typed WorkInput lifecycle operations: request, save draft,
// submit, reject, pin and unpin. It reads/writes authoritative V2 events
// through the WorkStore. It delegates Cornerstone pin/unpin to CornerstoneManager
// as an independent transaction.
type InputService struct {
	store        WorkStore
	cornerstones *CornerstoneManager
	defStore     DefinitionRevisionStore
	clock        Clock
}

const (
	customWorkInfoTaskID  = "work-information"
	customWorkInfoBlockID = "work-information"
)

// NewInputService creates an InputService backed by the given store and
// optional CornerstoneManager. Pass nil for cornerstones to disable pin/unpin.
func NewInputService(store WorkStore, cornerstones *CornerstoneManager) *InputService {
	s := &InputService{
		store:        store,
		cornerstones: cornerstones,
		clock:        RealClock{},
	}
	if defStore, ok := store.(DefinitionRevisionStore); ok {
		s.defStore = defStore
	}
	return s
}

// SetDefinitionStore configures the V2 definition revision store used for
// InputSpec lookup during submit validation.
func (s *InputService) SetDefinitionStore(defStore DefinitionRevisionStore) {
	s.defStore = defStore
}

// ── RequestInput ────────────────────────────────────────────────────────────

type RequestInputRequest struct {
	WorkID           string `json:"workId"`
	RunID            string `json:"runId"`
	TaskID           string `json:"taskId"`
	BlockID          string `json:"blockId"`
	InputID          string `json:"inputId"`
	SpecID           string `json:"specId"`
	DefinitionRev    int64  `json:"definitionRev"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// RequestInput declares a new input gate for a task.
func (s *InputService) RequestInput(ctx context.Context, req RequestInputRequest) (*WorkInput, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	if req.WorkID == "" {
		return nil, errors.New("work: RequestInput: workID is required")
	}
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.RequestID == "" {
		return nil, errors.New("work: RequestInput: requestID is required")
	}
	req.InputID = strings.TrimSpace(req.InputID)
	if req.InputID == "" {
		return nil, errors.New("work: RequestInput: inputID is required")
	}
	req.RunID = strings.TrimSpace(req.RunID)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.BlockID = strings.TrimSpace(req.BlockID)
	req.SpecID = strings.TrimSpace(req.SpecID)
	if req.RunID == "" || req.TaskID == "" || req.BlockID == "" || req.SpecID == "" {
		return nil, errors.New("work: RequestInput: runID/taskID/blockID/specID are required")
	}

	eventRequestID := req.RequestID + "/req"
	current, state, err := s.store.LoadState(req.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}

	intentDigest := hashInputOperation("RequestInput", req)
	if state.RequestFound {
		if state.RequestType != EventInputRequested {
			return nil, fmt.Errorf("%w: RequestInput requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, state.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(current, req.RequestID, "RequestInput", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput != nil {
			cp := *receipt.ResultInput
			return &cp, nil
		}
		return nil, fmt.Errorf("work: RequestInput: input %q not found in projection for replayed request %q", req.InputID, req.RequestID)
	}
	if err := validateInputWrite(current, state, req.ExpectedRevision, req.DefinitionRev); err != nil {
		return nil, err
	}
	if _, err := s.resolveInputSpecAt(req.WorkID, req.DefinitionRev, req.SpecID); err != nil {
		return nil, fmt.Errorf("work: RequestInput: %w", err)
	}

	if idx := findInputIndex(current, req.InputID); idx >= 0 {
		return nil, fmt.Errorf("%w: inputID %q already exists", ErrWorkRequestIDConflict, req.InputID)
	}

	now := s.clock.Now().UTC()
	resultInput := &WorkInput{ID: req.InputID, WorkID: req.WorkID, RunID: req.RunID, TaskID: req.TaskID,
		BlockID: req.BlockID, SpecID: req.SpecID, State: InputRequested, UpdatedAt: now}
	receipt := &InputIntentReceipt{RequestID: req.RequestID, Operation: "RequestInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: state.Revision + 1, ResultDigest: hashInputOperation("WorkInput", resultInput),
		ResultInput: resultInput, CreatedAt: now}
	payload, err := json.Marshal(InputRequestedPayload{
		InputID: req.InputID,
		WorkID:  req.WorkID,
		RunID:   req.RunID,
		TaskID:  req.TaskID,
		BlockID: req.BlockID,
		SpecID:  req.SpecID,
		Receipt: receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: RequestInput: encode event: %w", err)
	}
	event := newServiceEventV2(req.WorkID, eventRequestID, EventInputRequested, payload, now)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: req.RunID, TaskID: req.TaskID, BlockID: req.BlockID, InputID: req.InputID, SpecID: req.SpecID,
		ExpectedRevision: int64Ptr(req.ExpectedRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}

	if _, err := s.store.CommitEvent(req.WorkID, event); err != nil {
		return nil, fmt.Errorf("work: RequestInput: commit: %w", err)
	}

	// Reload to get the projected input.
	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return nil, committedRecovery("request-input-reload", req.WorkID, req.RequestID, event.Revision, reloadErr)
	}
	if idx := findInputIndex(reloaded, req.InputID); idx >= 0 {
		cp := reloaded.V2Inputs[idx]
		return &cp, nil
	}
	return nil, fmt.Errorf("work: RequestInput: input %q disappeared from projection after commit", req.InputID)
}

// ── SaveDraft ──────────────────────────────────────────────────────────────

// SaveDraftRequest carries parameters for saving an input draft.
type SaveDraftRequest struct {
	WorkID           string          `json:"workId"`
	InputID          string          `json:"inputId"`
	Value            json.RawMessage `json:"value"`
	Extra            string          `json:"extra,omitempty"`
	Source           string          `json:"source,omitempty"`
	UpdatedBy        string          `json:"updatedBy,omitempty"`
	DefinitionRev    int64           `json:"definitionRev"`
	InputRevision    int64           `json:"inputRevision"`
	ExpectedRevision int64           `json:"expectedRevision"`
	RequestID        string          `json:"requestId"`
}

// SaveDraftResult reports the outcome of a SaveDraft call.
type SaveDraftResult struct {
	Input    *WorkInput `json:"input"`
	Revision int64      `json:"revision"`
	Error    string     `json:"error,omitempty"`
}

// SaveDraft saves a draft value for a requested input. Validation failure
// preserves the draft and populates Result.Error.
// Repeated calls with the same requestID and intent are idempotent.
func (s *InputService) SaveDraft(ctx context.Context, req SaveDraftRequest) (*SaveDraftResult, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.InputID = strings.TrimSpace(req.InputID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.WorkID == "" || req.InputID == "" || req.RequestID == "" {
		return nil, errors.New("work: SaveDraft: workID/inputID/requestID are required")
	}

	eventRequestID := req.RequestID + "/draft"
	current, state, err := s.store.LoadState(req.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}

	intentDigest := hashInputOperation("SaveDraft", req)
	if state.RequestFound {
		if state.RequestType != EventInputDraftSaved {
			return nil, fmt.Errorf("%w: SaveDraft requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, state.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(current, req.RequestID, "SaveDraft", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput != nil {
			cp := *receipt.ResultInput
			return &SaveDraftResult{Input: &cp, Revision: receipt.ResultRevision}, nil
		}
		return nil, fmt.Errorf("%w: SaveDraft: input %q missing after replayed commit", ErrWorkNeedsRepair, req.InputID)
	}

	idx := findInputIndex(current, req.InputID)
	if idx < 0 {
		return nil, fmt.Errorf("work: SaveDraft: input %q not found in work %q", req.InputID, req.WorkID)
	}
	existing := current.V2Inputs[idx]
	if !json.Valid(req.Value) {
		return &SaveDraftResult{Input: &existing, Revision: state.Revision, Error: "value is not valid JSON"}, nil
	}
	if err := validateInputWrite(current, state, req.ExpectedRevision, req.DefinitionRev); err != nil {
		return &SaveDraftResult{Input: &existing, Revision: state.Revision, Error: err.Error()}, nil
	}

	newRevision := existing.Revision + 1
	if req.InputRevision != existing.Revision {
		return &SaveDraftResult{
			Input: &existing, Revision: state.Revision,
			Error: fmt.Sprintf("input revision conflict: expected %d, current %d", req.InputRevision, existing.Revision),
		}, nil
	}

	now := s.clock.Now().UTC()
	resultInput := existing
	resultInput.Value = append(json.RawMessage(nil), req.Value...)
	resultInput.Extra = strings.TrimSpace(req.Extra)
	resultInput.State, resultInput.Source, resultInput.UpdatedBy = InputDraft, req.Source, req.UpdatedBy
	resultInput.Revision, resultInput.Error, resultInput.UpdatedAt = newRevision, "", now
	receipt := &InputIntentReceipt{RequestID: req.RequestID, Operation: "SaveDraft", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: state.Revision + 1, ResultDigest: hashInputOperation("WorkInput", &resultInput),
		ResultInput: &resultInput, CreatedAt: now}
	payload, err := json.Marshal(InputDraftSavedPayload{
		InputID:          req.InputID,
		WorkID:           req.WorkID,
		RunID:            existing.RunID,
		TaskID:           existing.TaskID,
		BlockID:          existing.BlockID,
		SpecID:           existing.SpecID,
		Value:            req.Value,
		Extra:            strings.TrimSpace(req.Extra),
		Source:           req.Source,
		UpdatedBy:        req.UpdatedBy,
		Revision:         newRevision,
		ExpectedRevision: req.InputRevision,
		Receipt:          receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: SaveDraft: encode event: %w", err)
	}
	event := newServiceEventV2(req.WorkID, eventRequestID, EventInputDraftSaved, payload, now)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID, InputID: req.InputID, SpecID: existing.SpecID,
		ExpectedRevision: int64Ptr(req.InputRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}

	rev, err := s.store.CommitEvent(req.WorkID, event)
	if err != nil {
		return nil, fmt.Errorf("work: SaveDraft: commit: %w", err)
	}

	// Reload projection.
	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return &SaveDraftResult{Revision: rev}, committedRecovery("save-draft-reload", req.WorkID, req.RequestID, rev, reloadErr)
	}
	if ri := findInputIndex(reloaded, req.InputID); ri >= 0 {
		cp := reloaded.V2Inputs[ri]
		return &SaveDraftResult{Input: &cp, Revision: rev}, nil
	}
	return &SaveDraftResult{Revision: rev}, fmt.Errorf("work: SaveDraft: input %q missing from projection after commit", req.InputID)
}

// ── SubmitInput ─────────────────────────────────────────────────────────────

// SubmitInputRequest carries parameters for submitting an input value.
type SubmitInputRequest struct {
	WorkID           string          `json:"workId"`
	InputID          string          `json:"inputId"`
	Value            json.RawMessage `json:"value"`
	Extra            string          `json:"extra,omitempty"`
	Source           string          `json:"source,omitempty"`
	UpdatedBy        string          `json:"updatedBy,omitempty"`
	DefinitionRev    int64           `json:"definitionRev"`
	InputRevision    int64           `json:"inputRevision"`
	ExpectedRevision int64           `json:"expectedRevision"`
	RequestID        string          `json:"requestId"`
}

// SubmitInputResult reports the outcome of a SubmitInput call.
type SubmitInputResult struct {
	Input           *WorkInput          `json:"input"`
	Receipt         *InputIntentReceipt `json:"receipt,omitempty"`
	Revision        int64               `json:"revision"`
	Duplicate       bool                `json:"duplicate"`
	AffectedTaskIDs []string            `json:"affectedTaskIds,omitempty"`
	Error           string              `json:"error,omitempty"`
	Committed       bool                `json:"committed"`
	Recoverable     bool                `json:"recoverable"`
	TransportError  *WorkTransportError `json:"transportError,omitempty"`
}

// SubmitInput validates and submits an input value. On success it marks
// the owning task(s) as affected so a scheduler can re-evaluate readiness.
//
// Idempotency: same requestID + same intent digest returns the same result
// without incrementing revision or re-marking tasks.
// Conflict: same requestID + different intent returns ErrWorkRequestIDConflict.
func (s *InputService) SubmitInput(ctx context.Context, req SubmitInputRequest) (*SubmitInputResult, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.InputID = strings.TrimSpace(req.InputID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.WorkID == "" || req.InputID == "" || req.RequestID == "" {
		return nil, errors.New("work: SubmitInput: workID/inputID/requestID are required")
	}

	eventRequestID := req.RequestID + "/submit"
	current, state, err := s.store.LoadState(req.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}

	intentDigest := hashInputOperation("SubmitInput", req)

	// Idempotency: replay from the event log / receipt.
	if state.RequestFound {
		if state.RequestType != EventInputSubmitted && state.RequestType != EventInputRejected {
			return nil, fmt.Errorf("%w: SubmitInput requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, state.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(current, req.RequestID, "SubmitInput", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput != nil {
			cp := *receipt.ResultInput
			return &SubmitInputResult{Input: &cp, Receipt: cloneInputIntentReceipt(receipt), Revision: receipt.ResultRevision, Duplicate: true,
				AffectedTaskIDs: append([]string(nil), receipt.AffectedTaskIDs...), Error: receipt.Error}, nil
		}
		// Input found in event log but not yet in projection — fall through to retry.
	}

	idx := findInputIndex(current, req.InputID)
	if idx < 0 {
		return nil, fmt.Errorf("work: SubmitInput: input %q not found in work %q", req.InputID, req.WorkID)
	}
	existing := current.V2Inputs[idx]
	if err := validateInputWrite(current, state, req.ExpectedRevision, req.DefinitionRev); err != nil {
		return &SubmitInputResult{Input: &existing, Revision: state.Revision, Error: err.Error()}, nil
	}
	if req.InputRevision != existing.Revision {
		return &SubmitInputResult{
			Input:    &existing,
			Revision: state.Revision,
			Error:    fmt.Sprintf("input revision conflict: expected %d, current %d", req.InputRevision, existing.Revision),
		}, nil
	}

	// Validate value against InputSpec.
	spec, resolveErr := s.resolveInputSpecForInput(req.WorkID, req.DefinitionRev, existing)
	if resolveErr != nil {
		return &SubmitInputResult{Input: &existing, Revision: state.Revision, Error: resolveErr.Error()}, nil
	}
	if valErr := ValidateInputValue(*spec, req.Value); valErr != nil {
		return s.commitSubmitRejection(req, existing, state, intentDigest, valErr)
	}

	newRevision := existing.Revision + 1

	affectedTaskIDs := []string{existing.TaskID}
	if existing.CustomSpec != nil {
		// Custom Work information is optional context shared with future tasks.
		// Saving it must not invalidate or restart an in-flight task.
		affectedTaskIDs = nil
	} else if (existing.State == InputSubmitted || existing.State == InputAccepted) &&
		jsonValuesEqual(existing.Value, req.Value) &&
		strings.TrimSpace(existing.Extra) == strings.TrimSpace(req.Extra) {
		// A new request ID with unchanged content is still recorded as a
		// successful save, but it must not restart completed work.
		affectedTaskIDs = nil
	} else if existing.State == InputSubmitted || existing.State == InputAccepted {
		affectedTaskIDs, err = s.affectedInputTaskIDs(current, existing, req.DefinitionRev)
		if err != nil {
			return nil, fmt.Errorf("work: SubmitInput: resolve affected tasks: %w", err)
		}
	}

	now := s.clock.Now().UTC()
	resultInput := existing
	resultInput.Value = append(json.RawMessage(nil), req.Value...)
	resultInput.Extra = strings.TrimSpace(req.Extra)
	resultInput.State, resultInput.Source, resultInput.UpdatedBy = InputSubmitted, req.Source, req.UpdatedBy
	resultInput.Revision, resultInput.Error, resultInput.UpdatedAt = newRevision, "", now
	receipt := &InputIntentReceipt{
		RequestID: req.RequestID, Operation: "SubmitInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: state.Revision + 1, ResultDigest: hashInputOperation("WorkInput", &resultInput),
		ResultInput:     &resultInput,
		AffectedTaskIDs: append([]string(nil), affectedTaskIDs...), CreatedAt: now,
	}
	payload, err := json.Marshal(InputSubmittedPayload{
		InputID:          req.InputID,
		WorkID:           req.WorkID,
		RunID:            existing.RunID,
		TaskID:           existing.TaskID,
		BlockID:          existing.BlockID,
		SpecID:           existing.SpecID,
		Value:            req.Value,
		Extra:            strings.TrimSpace(req.Extra),
		Source:           req.Source,
		UpdatedBy:        req.UpdatedBy,
		Revision:         newRevision,
		ExpectedRevision: req.InputRevision,
		AffectedTaskIDs:  affectedTaskIDs,
		Receipt:          receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: SubmitInput: encode event: %w", err)
	}
	event := newServiceEventV2(req.WorkID, eventRequestID, EventInputSubmitted, payload, now)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID, InputID: req.InputID, SpecID: existing.SpecID,
		ExpectedRevision: int64Ptr(req.InputRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}

	rev, err := s.store.CommitEvent(req.WorkID, event)
	if err != nil {
		return nil, fmt.Errorf("work: SubmitInput: commit: %w", err)
	}

	// A sidecar is only an optional cache; the receipt above is authoritative
	// because it is committed in the event payload and reducer projection.
	if receiptStore, ok := s.store.(InputReceiptStore); ok {
		if receiptErr := receiptStore.StoreInputReceipt(req.WorkID, receipt); receiptErr != nil {
			return &SubmitInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: rev, AffectedTaskIDs: affectedTaskIDs},
				committedRecovery("submit-input-receipt", req.WorkID, req.RequestID, rev, receiptErr)
		}
	}

	// Reload projection.
	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return &SubmitInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: rev, AffectedTaskIDs: affectedTaskIDs},
			committedRecovery("submit-input-reload", req.WorkID, req.RequestID, rev, reloadErr)
	}
	if ri := findInputIndex(reloaded, req.InputID); ri >= 0 {
		cp := reloaded.V2Inputs[ri]
		return &SubmitInputResult{Input: &cp, Receipt: cloneInputIntentReceipt(receipt), Revision: rev, AffectedTaskIDs: affectedTaskIDs}, nil
	}
	return &SubmitInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: rev, AffectedTaskIDs: affectedTaskIDs},
		fmt.Errorf("work: SubmitInput: input %q missing from projection after commit", req.InputID)
}

// affectedInputTaskIDs expands a changed, previously-submitted input through
// the active run's existing downstream runtimes. The scheduler still derives
// the DAG subgraph from the owning task; including materialized descendants
// here lets the same committed input event invalidate their completed states.
func (s *InputService) affectedInputTaskIDs(
	current *Work,
	input WorkInput,
	definitionRev int64,
) ([]string, error) {
	ids := map[string]bool{input.TaskID: true}
	if current == nil || input.RunID == "" {
		return []string{input.TaskID}, nil
	}
	runtime := current.V2TaskRuntimes[input.TaskID]
	if runtime == nil || runtime.NodeID == "" {
		return []string{input.TaskID}, nil
	}
	if s == nil || s.defStore == nil {
		return nil, errors.New("definition store is not configured")
	}
	if current.V2CurrentRevision != definitionRev {
		return nil, fmt.Errorf(
			"definition revision conflict: expected %d, current %d",
			definitionRev,
			current.V2CurrentRevision,
		)
	}
	definition, err := s.defStore.LoadRevision(input.WorkID, definitionRev)
	if err != nil {
		return nil, err
	}
	for _, nodeID := range AffectedNodes(definition.Nodes, []string{runtime.NodeID}) {
		taskID, deriveErr := DeriveTaskID(input.RunID, nodeID)
		if deriveErr != nil {
			return nil, deriveErr
		}
		if current.V2TaskRuntimes[taskID] != nil {
			ids[taskID] = true
		}
	}
	result := make([]string, 0, len(ids))
	for taskID := range ids {
		result = append(result, taskID)
	}
	sort.Strings(result)
	return result, nil
}

// AddCustomInput atomically creates and submits one user-owned Work
// information item. The inline InputSpec keeps the active definition immutable
// while allowing the normal input editor and validation path to be reused.
func (s *InputService) AddCustomInput(ctx context.Context, req AddCustomWorkInputRequest) (*SubmitInputResult, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.RunID = strings.TrimSpace(req.RunID)
	req.InputID = strings.TrimSpace(req.InputID)
	req.Name = strings.TrimSpace(req.Name)
	req.Description = strings.TrimSpace(req.Description)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.WorkID == "" || req.RunID == "" || req.InputID == "" || req.Name == "" || req.RequestID == "" {
		return nil, errors.New("work: AddCustomInput: workID/runID/inputID/name/requestID are required")
	}
	if req.Kind != InputText && req.Kind != InputFile {
		return nil, fmt.Errorf("work: AddCustomInput: kind %q must be text or file", req.Kind)
	}
	spec := InputSpec{
		ID:          "custom:" + req.InputID,
		Label:       req.Name,
		Description: req.Description,
		Kind:        req.Kind,
		Required:    true,
	}
	if valErr := ValidateInputValue(spec, req.Value); valErr != nil {
		return &SubmitInputResult{Revision: req.ExpectedRevision, Error: valErr.Error()}, nil
	}

	eventRequestID := req.RequestID + "/submit"
	current, state, err := s.store.LoadState(req.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}
	intentDigest := hashInputOperation("AddCustomWorkInput", req)
	if state.RequestFound {
		if state.RequestType != EventInputSubmitted {
			return nil, fmt.Errorf("%w: AddCustomInput requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, state.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(current, req.RequestID, "AddCustomWorkInput", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput == nil {
			return nil, fmt.Errorf("%w: AddCustomInput result is unavailable", ErrWorkNeedsRepair)
		}
		cp := cloneWorkInput(*receipt.ResultInput)
		return &SubmitInputResult{
			Input: &cp, Receipt: cloneInputIntentReceipt(receipt),
			Revision: receipt.ResultRevision, Duplicate: true,
		}, nil
	}
	if err := validateInputWrite(current, state, req.ExpectedRevision, req.DefinitionRevision); err != nil {
		return &SubmitInputResult{Revision: state.Revision, Error: err.Error()}, nil
	}
	if findInputIndex(current, req.InputID) >= 0 {
		return nil, fmt.Errorf("%w: inputID %q already exists", ErrWorkRequestIDConflict, req.InputID)
	}

	now := s.clock.Now().UTC()
	resultInput := WorkInput{
		ID: req.InputID, WorkID: req.WorkID, RunID: req.RunID,
		TaskID: customWorkInfoTaskID, BlockID: customWorkInfoBlockID,
		SpecID: spec.ID, CustomSpec: cloneInputSpec(&spec),
		Value: append(json.RawMessage(nil), req.Value...), State: InputSubmitted,
		Source: "user", UpdatedBy: "user", Revision: 1, UpdatedAt: now,
	}
	receipt := &InputIntentReceipt{
		RequestID: req.RequestID, Operation: "AddCustomWorkInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: state.Revision + 2,
		ResultDigest: hashInputOperation("WorkInput", &resultInput),
		ResultInput:  &resultInput, CreatedAt: now,
	}
	requestPayload, err := json.Marshal(InputRequestedPayload{
		InputID: req.InputID, WorkID: req.WorkID, RunID: req.RunID,
		TaskID: customWorkInfoTaskID, BlockID: customWorkInfoBlockID,
		SpecID: spec.ID, CustomSpec: &spec,
	})
	if err != nil {
		return nil, fmt.Errorf("work: AddCustomInput: encode request event: %w", err)
	}
	submitPayload, err := json.Marshal(InputSubmittedPayload{
		InputID: req.InputID, WorkID: req.WorkID, RunID: req.RunID,
		TaskID: customWorkInfoTaskID, BlockID: customWorkInfoBlockID,
		SpecID: spec.ID, Value: req.Value, Source: "user", UpdatedBy: "user",
		Revision: 1, ExpectedRevision: 0, Receipt: receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: AddCustomInput: encode submit event: %w", err)
	}
	requestEvent := newServiceEventV2(req.WorkID, req.RequestID+"/request", EventInputRequested, requestPayload, now)
	requestEvent.BaseRevision, requestEvent.Revision = state.Revision, state.Revision+1
	requestEvent.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: req.RunID, TaskID: customWorkInfoTaskID, BlockID: customWorkInfoBlockID,
		InputID: req.InputID, SpecID: spec.ID, DefinitionRevision: int64Ptr(req.DefinitionRevision),
	}
	submitEvent := newServiceEventV2(req.WorkID, eventRequestID, EventInputSubmitted, submitPayload, now)
	submitEvent.BaseRevision, submitEvent.Revision = state.Revision+1, state.Revision+2
	submitEvent.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: req.RunID, TaskID: customWorkInfoTaskID, BlockID: customWorkInfoBlockID,
		InputID: req.InputID, SpecID: spec.ID, ExpectedRevision: int64Ptr(0),
		DefinitionRevision: int64Ptr(req.DefinitionRevision),
	}
	revisions, err := s.store.CommitEvents(req.WorkID, []WorkEvent{requestEvent, submitEvent})
	if err != nil {
		return nil, fmt.Errorf("work: AddCustomInput: commit: %w", err)
	}
	revision := revisions[len(revisions)-1]
	if receiptStore, ok := s.store.(InputReceiptStore); ok {
		if receiptErr := receiptStore.StoreInputReceipt(req.WorkID, receipt); receiptErr != nil {
			return &SubmitInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: revision},
				committedRecovery("add-custom-input-receipt", req.WorkID, req.RequestID, revision, receiptErr)
		}
	}
	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return &SubmitInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: revision},
			committedRecovery("add-custom-input-reload", req.WorkID, req.RequestID, revision, reloadErr)
	}
	if idx := findInputIndex(reloaded, req.InputID); idx >= 0 {
		cp := cloneWorkInput(reloaded.V2Inputs[idx])
		return &SubmitInputResult{Input: &cp, Receipt: cloneInputIntentReceipt(receipt), Revision: revision}, nil
	}
	return &SubmitInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: revision},
		fmt.Errorf("work: AddCustomInput: input %q missing after commit", req.InputID)
}

func (s *InputService) commitSubmitRejection(req SubmitInputRequest, existing WorkInput, state WorkEventState, intentDigest string, validationErr error) (*SubmitInputResult, error) {
	if !json.Valid(req.Value) {
		return &SubmitInputResult{Input: &existing, Revision: state.Revision, Error: validationErr.Error()}, nil
	}
	now := s.clock.Now().UTC()
	resultInput := existing
	resultInput.Value = append(json.RawMessage(nil), req.Value...)
	resultInput.Extra = strings.TrimSpace(req.Extra)
	resultInput.State, resultInput.Error = InputRejected, validationErr.Error()
	resultInput.Source, resultInput.UpdatedBy = req.Source, req.UpdatedBy
	resultInput.Revision, resultInput.UpdatedAt = existing.Revision+1, now
	receipt := &InputIntentReceipt{
		RequestID: req.RequestID, Operation: "SubmitInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: state.Revision + 1,
		ResultDigest: hashInputOperation("WorkInput", &resultInput), ResultInput: &resultInput,
		Error: validationErr.Error(), CreatedAt: now,
	}
	payload, err := json.Marshal(InputRejectedPayload{
		InputID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID, SpecID: existing.SpecID,
		Value: req.Value, Extra: strings.TrimSpace(req.Extra),
		Reason: validationErr.Error(), Source: req.Source, UpdatedBy: req.UpdatedBy,
		Revision: resultInput.Revision, ExpectedRevision: req.InputRevision, Receipt: receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: SubmitInput: encode validation rejection: %w", err)
	}
	event := newServiceEventV2(req.WorkID, req.RequestID+"/submit", EventInputRejected, payload, now)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID, InputID: req.InputID, SpecID: existing.SpecID,
		ExpectedRevision: int64Ptr(req.InputRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}
	rev, err := s.store.CommitEvent(req.WorkID, event)
	if err != nil {
		return nil, fmt.Errorf("work: SubmitInput: commit validation rejection: %w", err)
	}
	if receiptStore, ok := s.store.(InputReceiptStore); ok {
		if receiptErr := receiptStore.StoreInputReceipt(req.WorkID, receipt); receiptErr != nil {
			return &SubmitInputResult{Input: &resultInput, Receipt: cloneInputIntentReceipt(receipt), Revision: rev, Error: validationErr.Error()},
				committedRecovery("submit-input-rejection-receipt", req.WorkID, req.RequestID, rev, receiptErr)
		}
	}
	return &SubmitInputResult{Input: &resultInput, Receipt: cloneInputIntentReceipt(receipt), Revision: rev, Error: validationErr.Error()}, nil
}

// ── RejectInput ─────────────────────────────────────────────────────────────

type RejectInputRequest struct {
	WorkID           string `json:"workId"`
	InputID          string `json:"inputId"`
	Reason           string `json:"reason,omitempty"`
	Source           string `json:"source,omitempty"`
	DefinitionRev    int64  `json:"definitionRev"`
	InputRevision    int64  `json:"inputRevision"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// RejectInput marks an input as rejected with an optional reason.
func (s *InputService) RejectInput(ctx context.Context, req RejectInputRequest) (*WorkInput, error) {
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.InputID = strings.TrimSpace(req.InputID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.WorkID == "" || req.InputID == "" || req.RequestID == "" {
		return nil, errors.New("work: RejectInput: workID/inputID/requestID are required")
	}

	eventRequestID := req.RequestID + "/reject"
	current, state, err := s.store.LoadState(req.WorkID, eventRequestID)
	if err != nil {
		return nil, err
	}
	intentDigest := hashInputOperation("RejectInput", req)
	if state.RequestFound {
		if state.RequestType != EventInputRejected {
			return nil, fmt.Errorf("%w: RejectInput requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, state.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(current, req.RequestID, "RejectInput", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput != nil {
			cp := *receipt.ResultInput
			return &cp, nil
		}
		return nil, fmt.Errorf("%w: RejectInput: input %q missing after replayed commit", ErrWorkNeedsRepair, req.InputID)
	}

	idx := findInputIndex(current, req.InputID)
	if idx < 0 {
		return nil, fmt.Errorf("work: RejectInput: input %q not found in work %q", req.InputID, req.WorkID)
	}
	existing := current.V2Inputs[idx]
	if err := validateInputWrite(current, state, req.ExpectedRevision, req.DefinitionRev); err != nil {
		return nil, err
	}
	if req.InputRevision != existing.Revision {
		return nil, fmt.Errorf("work: RejectInput: input revision conflict: expected %d, current %d", req.InputRevision, existing.Revision)
	}

	now := s.clock.Now().UTC()
	resultInput := existing
	resultInput.State, resultInput.Error, resultInput.Source = InputRejected, req.Reason, req.Source
	resultInput.Revision, resultInput.UpdatedAt = existing.Revision+1, now
	receipt := &InputIntentReceipt{RequestID: req.RequestID, Operation: "RejectInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: state.Revision + 1, ResultDigest: hashInputOperation("WorkInput", &resultInput),
		ResultInput: &resultInput, Error: req.Reason, CreatedAt: now}
	payload, err := json.Marshal(InputRejectedPayload{
		InputID:          req.InputID,
		WorkID:           req.WorkID,
		RunID:            existing.RunID,
		TaskID:           existing.TaskID,
		BlockID:          existing.BlockID,
		SpecID:           existing.SpecID,
		Value:            existing.Value,
		Extra:            existing.Extra,
		Reason:           req.Reason,
		Source:           req.Source,
		UpdatedBy:        req.Source,
		Revision:         existing.Revision + 1,
		ExpectedRevision: req.InputRevision,
		Receipt:          receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: RejectInput: encode event: %w", err)
	}
	event := newServiceEventV2(req.WorkID, eventRequestID, EventInputRejected, payload, now)
	event.BaseRevision, event.Revision = state.Revision, state.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID, InputID: req.InputID, SpecID: existing.SpecID,
		ExpectedRevision: int64Ptr(req.InputRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}

	if _, err := s.store.CommitEvent(req.WorkID, event); err != nil {
		return nil, fmt.Errorf("work: RejectInput: commit: %w", err)
	}

	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return nil, committedRecovery("reject-input-reload", req.WorkID, req.RequestID, event.Revision, reloadErr)
	}
	if idx := findInputIndex(reloaded, req.InputID); idx >= 0 {
		cp := reloaded.V2Inputs[idx]
		return &cp, nil
	}
	return nil, fmt.Errorf("work: RejectInput: input %q disappeared from projection after commit", req.InputID)
}

// ── Pin / Unpin ─────────────────────────────────────────────────────────────

// PinInputRequest is a typed request to pin an input as a Cornerstone.
type PinInputRequest struct {
	WorkID           string `json:"workId"`
	InputID          string `json:"inputId"`
	DefinitionRev    int64  `json:"definitionRev"`
	InputRevision    int64  `json:"inputRevision"`
	ExpectedRevision int64  `json:"expectedRevision"`
	RequestID        string `json:"requestId"`
}

// PinInputResult reports the outcome of a PinInput call.
type PinInputResult struct {
	Input         *WorkInput          `json:"input"`
	Receipt       *InputIntentReceipt `json:"receipt,omitempty"`
	CornerstoneID string              `json:"cornerstoneId,omitempty"`
	Pinned        bool                `json:"pinned"`
	Revision      int64               `json:"revision"`
	Duplicate     bool                `json:"duplicate"`
	Error         string              `json:"error,omitempty"`
}

// PinInput pins a submitted input as a Cornerstone. It delegates to
// CornerstoneManager for the actual pin operation, then writes an
// input.cornerstone_changed event. Pin failure does NOT rollback a
// previously submitted input — the two are independent transactions.
func (s *InputService) PinInput(ctx context.Context, req PinInputRequest) (*PinInputResult, error) {
	if s.cornerstones == nil {
		return nil, errors.New("work: PinInput: no CornerstoneManager configured")
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.InputID = strings.TrimSpace(req.InputID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.WorkID == "" || req.InputID == "" || req.RequestID == "" {
		return nil, errors.New("work: PinInput: workID/inputID/requestID are required")
	}
	intentDigest := hashInputOperation("PinInput", req)
	eventRequestID := req.RequestID + "/pin-cs"
	if completed, completedState, loadErr := s.store.LoadState(req.WorkID, eventRequestID); loadErr == nil && completedState.RequestFound {
		if completedState.RequestType != EventInputCornerstoneChanged {
			return nil, fmt.Errorf("%w: PinInput requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, completedState.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(completed, req.RequestID, "PinInput", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput != nil {
			cp := *receipt.ResultInput
			return &PinInputResult{Input: &cp, Receipt: cloneInputIntentReceipt(receipt), CornerstoneID: receipt.CornerstoneID, Pinned: true,
				Revision: receipt.ResultRevision, Duplicate: true}, nil
		}
		return nil, fmt.Errorf("%w: PinInput requestID %q does not match input %q", ErrWorkRequestIDConflict, req.RequestID, req.InputID)
	}

	// Load current state to read the input value.
	current, state, err := s.store.LoadState(req.WorkID, "")
	if err != nil {
		return nil, err
	}
	idx := findInputIndex(current, req.InputID)
	if idx < 0 {
		return nil, fmt.Errorf("work: PinInput: input %q not found in work %q", req.InputID, req.WorkID)
	}
	existing := current.V2Inputs[idx]
	if err := validateInputDefinition(current, req.DefinitionRev); err != nil {
		return &PinInputResult{Input: &existing, Revision: state.Revision, Error: err.Error()}, nil
	}
	if req.InputRevision != existing.Revision {
		return &PinInputResult{Input: &existing, Revision: state.Revision,
			Error: fmt.Sprintf("input revision conflict: expected %d, current %d", req.InputRevision, existing.Revision)}, nil
	}

	if existing.State != InputSubmitted && existing.State != InputAccepted {
		return &PinInputResult{
			Input: &existing, Revision: state.Revision,
			Error: fmt.Sprintf("input must be submitted before pinning, current state: %s", existing.State),
		}, nil
	}
	spec, specErr := s.resolveInputSpecAt(req.WorkID, req.DefinitionRev, existing.SpecID)
	if specErr != nil {
		return &PinInputResult{Input: &existing, Revision: state.Revision, Error: specErr.Error()}, nil
	}
	if !spec.PinEligible {
		return &PinInputResult{Input: &existing, Revision: state.Revision, Error: "input is not eligible for Cornerstone pinning"}, nil
	}

	// Pin via CornerstoneManager as independent transaction.
	csInput := PinCornerstoneInput{
		Type:             CornerstoneParameter,
		Title:            fmt.Sprintf("Input: %s", existing.SpecID),
		Content:          string(existing.Value),
		Ref:              CornerstoneRef{Kind: "inline"},
		Mode:             CornerstoneSnapshot,
		Required:         false,
		ExpectedRevision: req.ExpectedRevision,
		RequestID:        req.RequestID + "/cs",
	}
	csResult, csErr := s.cornerstones.Pin(req.WorkID, csInput)
	if csErr != nil {
		return &PinInputResult{
			Input: &existing, Revision: state.Revision,
			Error: fmt.Sprintf("Cornerstone pin failed: %v", csErr),
		}, nil
	}

	cornerstoneID := csResult.Cornerstone.ID

	// Write input.cornerstone_changed event.
	now := s.clock.Now().UTC()
	resultInput := existing
	resultInput.CornerstoneID, resultInput.Revision, resultInput.UpdatedAt = cornerstoneID, existing.Revision+1, now
	receipt := &InputIntentReceipt{RequestID: req.RequestID, Operation: "PinInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: csResult.Revision + 1, ResultDigest: hashInputOperation("WorkInput", &resultInput),
		ResultInput:   &resultInput,
		CornerstoneID: cornerstoneID, Pinned: true, CreatedAt: now}
	payload, err := json.Marshal(InputCornerstoneChangedPayload{
		InputID:          req.InputID,
		WorkID:           req.WorkID,
		RunID:            existing.RunID,
		TaskID:           existing.TaskID,
		BlockID:          existing.BlockID,
		SpecID:           existing.SpecID,
		CornerstoneID:    cornerstoneID,
		Pinned:           true,
		Revision:         existing.Revision + 1,
		ExpectedRevision: req.InputRevision,
		Receipt:          receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: PinInput: encode event: %w", err)
	}
	event := newServiceEventV2(req.WorkID, eventRequestID, EventInputCornerstoneChanged, payload, now)
	event.BaseRevision, event.Revision = csResult.Revision, csResult.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID,
		InputID: req.InputID, SpecID: existing.SpecID,
		ExpectedRevision: int64Ptr(req.InputRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}

	rev, commitErr := s.store.CommitEvent(req.WorkID, event)
	if commitErr != nil {
		return &PinInputResult{
			Input: &existing, Pinned: true, CornerstoneID: cornerstoneID, Revision: state.Revision,
			Error: fmt.Sprintf("input.cornerstone_changed event commit failed: %v", commitErr),
		}, nil
	}

	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return &PinInputResult{
				Receipt: cloneInputIntentReceipt(receipt), CornerstoneID: cornerstoneID,
				Pinned: true, Revision: rev,
			},
			committedRecovery("pin-input-reload", req.WorkID, req.RequestID, rev, reloadErr)
	}
	if ri := findInputIndex(reloaded, req.InputID); ri >= 0 {
		cp := reloaded.V2Inputs[ri]
		return &PinInputResult{
			Input: &cp, Receipt: cloneInputIntentReceipt(receipt), CornerstoneID: cornerstoneID, Pinned: true, Revision: rev,
		}, nil
	}
	return &PinInputResult{Receipt: cloneInputIntentReceipt(receipt), Pinned: true, CornerstoneID: cornerstoneID, Revision: rev},
		fmt.Errorf("work: PinInput: input %q missing from projection after commit", req.InputID)
}

// UnpinInput removes the Cornerstone linkage from an input.
func (s *InputService) UnpinInput(ctx context.Context, req PinInputRequest) (*PinInputResult, error) {
	if s.cornerstones == nil {
		return nil, errors.New("work: UnpinInput: no CornerstoneManager configured")
	}
	if err := checkContext(ctx); err != nil {
		return nil, err
	}
	req.WorkID = strings.TrimSpace(req.WorkID)
	req.InputID = strings.TrimSpace(req.InputID)
	req.RequestID = strings.TrimSpace(req.RequestID)
	if req.WorkID == "" || req.InputID == "" || req.RequestID == "" {
		return nil, errors.New("work: UnpinInput: workID/inputID/requestID are required")
	}
	intentDigest := hashInputOperation("UnpinInput", req)
	eventRequestID := req.RequestID + "/unpin-cs"
	if completed, completedState, loadErr := s.store.LoadState(req.WorkID, eventRequestID); loadErr == nil && completedState.RequestFound {
		if completedState.RequestType != EventInputCornerstoneChanged {
			return nil, fmt.Errorf("%w: UnpinInput requestID %q already used for %q", ErrWorkRequestIDConflict, req.RequestID, completedState.RequestType)
		}
		receipt, receiptErr := inputReceiptReplay(completed, req.RequestID, "UnpinInput", intentDigest)
		if receiptErr != nil {
			return nil, receiptErr
		}
		if receipt.ResultInput != nil {
			cp := *receipt.ResultInput
			return &PinInputResult{Input: &cp, Receipt: cloneInputIntentReceipt(receipt), Revision: receipt.ResultRevision, Duplicate: true}, nil
		}
		return nil, fmt.Errorf("%w: UnpinInput requestID %q does not match input %q", ErrWorkRequestIDConflict, req.RequestID, req.InputID)
	}

	current, state, err := s.store.LoadState(req.WorkID, "")
	if err != nil {
		return nil, err
	}
	idx := findInputIndex(current, req.InputID)
	if idx < 0 {
		return nil, fmt.Errorf("work: UnpinInput: input %q not found in work %q", req.InputID, req.WorkID)
	}
	existing := current.V2Inputs[idx]
	if err := validateInputDefinition(current, req.DefinitionRev); err != nil {
		return &PinInputResult{Input: &existing, Revision: state.Revision, Error: err.Error()}, nil
	}
	if req.InputRevision != existing.Revision {
		return &PinInputResult{Input: &existing, Revision: state.Revision,
			Error: fmt.Sprintf("input revision conflict: expected %d, current %d", req.InputRevision, existing.Revision)}, nil
	}

	if existing.CornerstoneID == "" {
		return &PinInputResult{Input: &existing, Revision: state.Revision, Duplicate: true}, nil
	}

	removeResult, removeErr := s.cornerstones.Remove(req.WorkID, RemoveCornerstoneInput{
		CornerstoneID:    existing.CornerstoneID,
		ExpectedRevision: req.ExpectedRevision,
		RequestID:        req.RequestID + "/cs",
	})
	if removeErr != nil {
		return &PinInputResult{Input: &existing, Pinned: true, CornerstoneID: existing.CornerstoneID,
			Revision: state.Revision, Error: fmt.Sprintf("Cornerstone unpin failed: %v", removeErr)}, nil
	}

	now := s.clock.Now().UTC()
	resultInput := existing
	resultInput.CornerstoneID, resultInput.Revision, resultInput.UpdatedAt = "", existing.Revision+1, now
	receipt := &InputIntentReceipt{RequestID: req.RequestID, Operation: "UnpinInput", IntentDigest: intentDigest,
		InputID: req.InputID, ResultRevision: removeResult.Revision + 1, ResultDigest: hashInputOperation("WorkInput", &resultInput),
		ResultInput: &resultInput, CreatedAt: now}
	payload, err := json.Marshal(InputCornerstoneChangedPayload{
		InputID:          req.InputID,
		WorkID:           req.WorkID,
		RunID:            existing.RunID,
		TaskID:           existing.TaskID,
		BlockID:          existing.BlockID,
		SpecID:           existing.SpecID,
		CornerstoneID:    "",
		Pinned:           false,
		Revision:         existing.Revision + 1,
		ExpectedRevision: req.InputRevision,
		Receipt:          receipt,
	})
	if err != nil {
		return nil, fmt.Errorf("work: UnpinInput: encode event: %w", err)
	}
	event := newServiceEventV2(req.WorkID, eventRequestID, EventInputCornerstoneChanged, payload, now)
	event.BaseRevision, event.Revision = removeResult.Revision, removeResult.Revision+1
	event.Object = ObjectContext{
		Kind: ObjectInput, ID: req.InputID, WorkID: req.WorkID,
		RunID: existing.RunID, TaskID: existing.TaskID, BlockID: existing.BlockID,
		InputID: req.InputID, SpecID: existing.SpecID,
		ExpectedRevision: int64Ptr(req.InputRevision), DefinitionRevision: int64Ptr(req.DefinitionRev),
	}

	rev, commitErr := s.store.CommitEvent(req.WorkID, event)
	if commitErr != nil {
		return nil, fmt.Errorf("work: UnpinInput: commit: %w", commitErr)
	}

	reloaded, _, reloadErr := s.store.LoadState(req.WorkID, "")
	if reloadErr != nil {
		return &PinInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: rev},
			committedRecovery("unpin-input-reload", req.WorkID, req.RequestID, rev, reloadErr)
	}
	if ri := findInputIndex(reloaded, req.InputID); ri >= 0 {
		cp := reloaded.V2Inputs[ri]
		return &PinInputResult{Input: &cp, Receipt: cloneInputIntentReceipt(receipt), Revision: rev}, nil
	}
	return &PinInputResult{Receipt: cloneInputIntentReceipt(receipt), Revision: rev},
		fmt.Errorf("work: UnpinInput: input %q missing from projection after commit", req.InputID)
}

// ── Helpers ─────────────────────────────────────────────────────────────────

func (s *InputService) resolveInputSpecAt(workID string, revision int64, specID string) (*InputSpec, error) {
	if s.defStore == nil {
		return nil, errors.New("no definition store configured")
	}
	if revision <= 0 {
		return nil, errors.New("definitionRev must be positive")
	}
	def, err := s.defStore.LoadRevision(workID, revision)
	if err != nil {
		return nil, fmt.Errorf("load definition revision %d: %w", revision, err)
	}
	for i := range def.InputSpecs {
		if def.InputSpecs[i].ID == specID {
			return &def.InputSpecs[i], nil
		}
	}
	return nil, fmt.Errorf("InputSpec %q not found in definition revision %d", specID, revision)
}

func (s *InputService) resolveInputSpecForInput(workID string, revision int64, input WorkInput) (*InputSpec, error) {
	if input.CustomSpec != nil {
		if input.CustomSpec.ID != input.SpecID {
			return nil, fmt.Errorf("custom InputSpec %q does not match input specId %q", input.CustomSpec.ID, input.SpecID)
		}
		return cloneInputSpec(input.CustomSpec), nil
	}
	return s.resolveInputSpecAt(workID, revision, input.SpecID)
}

func cloneWorkInput(input WorkInput) WorkInput {
	input.Value = append(json.RawMessage(nil), input.Value...)
	input.CustomSpec = cloneInputSpec(input.CustomSpec)
	return input
}

func validateInputDefinition(current *Work, definitionRev int64) error {
	if current == nil {
		return ErrWorkNotFound
	}
	if current.SchemaVersion < SchemaVersionV2 {
		return fmt.Errorf("work: input mutation requires schema V2")
	}
	if current.ArchiveState != ArchiveActive {
		return fmt.Errorf("work: input mutation requires active Work, got %s", current.ArchiveState)
	}
	if definitionRev <= 0 || current.V2CurrentRevision != definitionRev {
		return fmt.Errorf("work: definition revision conflict: expected %d, current %d", definitionRev, current.V2CurrentRevision)
	}
	return nil
}

func validateInputWrite(current *Work, state WorkEventState, expectedWorkRev, definitionRev int64) error {
	if err := validateInputDefinition(current, definitionRev); err != nil {
		return err
	}
	if expectedWorkRev != state.Revision {
		return revisionConflict(current.ID, expectedWorkRev, state.Revision)
	}
	return nil
}

func inputReceiptReplay(current *Work, requestID, operation, intentDigest string) (*InputIntentReceipt, error) {
	if current == nil || current.V2InputReceipts == nil {
		return nil, fmt.Errorf("%w: input receipt %q is missing", ErrWorkNeedsRepair, requestID)
	}
	receipt, ok := current.V2InputReceipts[requestID]
	if !ok {
		return nil, fmt.Errorf("%w: input receipt %q is missing", ErrWorkNeedsRepair, requestID)
	}
	if receipt.Operation != operation || receipt.IntentDigest != intentDigest {
		return nil, fmt.Errorf("%w: requestID %q already used by a different input intent", ErrWorkRequestIDConflict, requestID)
	}
	return &receipt, nil
}

func checkContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("work: context is nil")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
		return nil
	}
}
