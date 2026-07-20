package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"workground2/internal/nilutil"
)

var errActionTransitionLost = errors.New("work: action transition lost to another executor")

type actionFlight struct{ done chan struct{} }

// SetActionRegistry replaces the trusted action definitions. It rejects a
// different registry that reuses an existing action's handler ID and version.
// It is safe to call while unrelated actions execute; each resolution observes
// one registry.
func (s *Service) SetActionRegistry(reg *ActionRegistry) error {
	s.actionCfgMu.Lock()
	defer s.actionCfgMu.Unlock()
	if s.actions != nil && reg != nil && s.actions != reg {
		previous := s.actions.snapshot()
		for key, next := range reg.snapshot() {
			if current, found := previous[key]; found && current.HandlerID == next.HandlerID && current.HandlerVersion == next.HandlerVersion {
				return &ErrActionHandlerRegistrationConflict{
					BlockKind: key.blockKind, ActionID: key.actionID,
					HandlerID: next.HandlerID, HandlerVersion: next.HandlerVersion,
				}
			}
		}
	}
	s.actions = reg
	return nil
}

// SetPermissionChecker replaces the action permission adapter. Nil and typed-nil
// values disable gated execution and are handled fail-closed.
func (s *Service) SetPermissionChecker(checker PermissionChecker) {
	if nilutil.IsNil(checker) {
		checker = nil
	}
	s.actionCfgMu.Lock()
	s.permissions = checker
	s.actionCfgMu.Unlock()
}

// ExecuteBlockAction resolves the current block's action ID through the trusted
// registry, persists a reservation before any approval/side effect, and returns
// the event-derived receipt for every idempotent replay.
func (s *Service) ExecuteBlockAction(ctx context.Context, input BlockActionRequest) (*ActionReceipt, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("ExecuteBlockAction", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID, blockID, actionID := strings.TrimSpace(input.WorkID), strings.TrimSpace(input.BlockID), strings.TrimSpace(input.ActionID)
	if workID == "" || blockID == "" || actionID == "" {
		return nil, errors.New("work: ExecuteBlockAction: workID, blockID and actionID are required")
	}
	inputCopy, err := cloneJSONMap(input.Input)
	if err != nil {
		return nil, fmt.Errorf("work: ExecuteBlockAction: invalid input: %w", err)
	}
	flightKey := workID + "\x00" + requestID

	for {
		current, state, loadErr := s.store.LoadState(workID, "")
		if loadErr != nil {
			return nil, fmt.Errorf("work: ExecuteBlockAction: load state: %w", loadErr)
		}
		if existing, _, found := findActionReceipt(current.ActionReceipts, requestID); found {
			inputDigest, digestErr := actionInputDigest(workID, blockID, actionID, existing.HandlerID, existing.HandlerVersion, inputCopy)
			if digestErr != nil {
				return nil, fmt.Errorf("work: ExecuteBlockAction: fingerprint input: %w", digestErr)
			}
			if existing.InputDigest != inputDigest || existing.WorkID != workID || existing.BlockID != blockID || existing.ActionID != actionID {
				return nil, &ErrActionFingerprintConflict{RequestID: requestID, ExistingFP: existing.InputDigest, IncomingFP: inputDigest}
			}
			if terminalReceipt(existing.Status) {
				return receiptResult(existing, state.Revision)
			}
			if flight, owner := s.beginActionFlight(flightKey); !owner {
				if waitErr := waitActionFlight(ctx, flight); waitErr != nil {
					return existing.toPublicReceipt(state.Revision), waitErr
				}
				continue
			} else {
				defer s.finishActionFlight(flightKey, flight)
			}

			if existing.Status == ActionRunning {
				legacyHandler := existing.HandlerIdentityVersion == 0 || existing.HandlerID == "" || existing.HandlerVersion == ""
				message := "previous executor stopped before recording the outcome; verify external state before retrying"
				if legacyHandler {
					message = "legacy running receipt has no trusted handler identity; verify external state before retrying"
				}
				unknown, rev, transitionErr := s.transitionAction(ctx, existing, ActionRunning, ActionUnknown,
					message, false, false, nil)
				if transitionErr != nil {
					if errors.Is(transitionErr, errActionTransitionLost) && terminalReceipt(unknown.Status) {
						return receiptResult(unknown, rev)
					}
					return existing.toPublicReceipt(state.Revision), transitionErr
				}
				if emitErr := s.emitActionSnapshot(workID, requestID); emitErr != nil {
					return unknown.toPublicReceipt(rev), emitErr
				}
				unknownErr := &ErrActionOutcomeUnknown{RequestID: requestID, Message: unknown.Message}
				if legacyHandler {
					versionErr := actionHandlerVersionError(existing, "", "", unknown.toPublicReceipt(rev), "legacy running receipt requires manual verification and upgrade")
					return unknown.toPublicReceipt(rev), errors.Join(versionErr, unknownErr)
				}
				return unknown.toPublicReceipt(rev), unknownErr
			}
			if existing.HandlerIdentityVersion == 0 || existing.HandlerID == "" || existing.HandlerVersion == "" {
				return s.failPendingHandlerVersion(existing, "", "", "legacy pending receipt has no trusted handler identity; upgrade required")
			}

			resolved, resolveErr := s.resolveAction(current, blockID, actionID, inputCopy)
			if resolveErr != nil {
				var unknownIntent *ErrActionUnknownIntent
				if errors.As(resolveErr, &unknownIntent) {
					return s.failPendingHandlerVersion(existing, "", "", "registered handler is unavailable; upgrade required")
				}
				failed, persistErr := s.finishBeforeRun(context.Background(), existing, ActionFailed, resolveErr.Error(), false)
				return failed, errors.Join(resolveErr, persistErr)
			}
			if existing.HandlerID != resolved.registration.HandlerID || existing.HandlerVersion != resolved.registration.HandlerVersion {
				reason := fmt.Sprintf("reserved handler %s@%s does not match registered handler %s@%s; upgrade required",
					existing.HandlerID, existing.HandlerVersion, resolved.registration.HandlerID, resolved.registration.HandlerVersion)
				return s.failPendingHandlerVersion(existing, resolved.registration.HandlerID, resolved.registration.HandlerVersion, reason)
			}
			if existing.Fingerprint != resolved.fingerprint {
				return nil, &ErrActionFingerprintConflict{RequestID: requestID, ExistingFP: existing.Fingerprint, IncomingFP: resolved.fingerprint}
			}
			return s.runReservedAction(ctx, existing, resolved, inputCopy)
		}

		if current.ArchiveState != ArchiveActive {
			return nil, fmt.Errorf("work: ExecuteBlockAction: Work %s is %s", workID, current.ArchiveState)
		}
		if input.ExpectedRevision != state.Revision {
			return nil, &ErrActionRevisionConflict{
				WorkID: workID, Expected: input.ExpectedRevision, Current: state.Revision,
				Latest: &WorkView{SchemaVersion: WorkViewSchemaVersion, Work: current, Revision: state.Revision},
			}
		}
		resolved, resolveErr := s.resolveAction(current, blockID, actionID, inputCopy)
		if resolveErr != nil {
			return nil, resolveErr
		}
		inputDigest, digestErr := actionInputDigest(workID, blockID, actionID, resolved.registration.HandlerID, resolved.registration.HandlerVersion, inputCopy)
		if digestErr != nil {
			return nil, fmt.Errorf("work: ExecuteBlockAction: fingerprint input: %w", digestErr)
		}
		now := time.Now().UTC()
		record := ActionReceiptRecord{
			WorkID: workID, BlockID: blockID, BlockKind: resolved.block.Kind, ActionID: actionID,
			HandlerIdentityVersion: HandlerIdentityVersion,
			HandlerID:              resolved.registration.HandlerID, HandlerVersion: resolved.registration.HandlerVersion,
			Status: ActionPending, RequestID: requestID, InputDigest: inputDigest,
			Fingerprint: resolved.fingerprint, Intent: resolved.registration.Intent,
			Summary: resolved.summary, Risk: resolved.risk, ConfirmRequired: resolved.confirm,
			Retryable: true, OutcomeKnown: true, CreatedAt: now, UpdatedAt: now,
		}
		if _, reserveErr := s.commitActionRecord(workID, requestID+"/action/reserved", EventBlockActionReserved, record, state.Revision); reserveErr != nil {
			// A duplicate or unrelated writer may have won. Reload the durable
			// projection and decide from it; never invoke on an uncertain reserve.
			var conflict *ErrWorkEventConflict
			if errors.As(reserveErr, &conflict) {
				continue
			}
			return nil, fmt.Errorf("work: ExecuteBlockAction: persist reservation: %w", reserveErr)
		}
		if emitErr := s.emitActionSnapshot(workID, requestID); emitErr != nil {
			return record.toPublicReceipt(state.Revision + 1), emitErr
		}
		flight, owner := s.beginActionFlight(flightKey)
		if !owner {
			if waitErr := waitActionFlight(ctx, flight); waitErr != nil {
				return record.toPublicReceipt(state.Revision + 1), waitErr
			}
			continue
		}
		defer s.finishActionFlight(flightKey, flight)
		return s.runReservedAction(ctx, record, resolved, inputCopy)
	}
}

type resolvedAction struct {
	block        *BlockInstance
	spec         *BlockActionSpec
	registration ActionRegistration
	risk         ActionRisk
	confirm      bool
	summary      string
	fingerprint  string
}

func (s *Service) resolveAction(value *Work, blockID, actionID string, input map[string]any) (*resolvedAction, error) {
	block := findBlock(value, blockID)
	if block == nil {
		return nil, fmt.Errorf("work: ExecuteBlockAction: block %s not found in Work %s", blockID, value.ID)
	}
	if block.Tombstone {
		return nil, fmt.Errorf("work: ExecuteBlockAction: block %s is tombstoned", blockID)
	}
	spec := findActionSpec(block, actionID)
	if spec == nil {
		return nil, &ErrActionUnknownAction{BlockID: blockID, ActionID: actionID}
	}
	reg, ok := s.lookupAction(block.Kind, actionID)
	if !ok {
		return nil, &ErrActionUnknownIntent{BlockKind: block.Kind, ActionID: actionID}
	}
	if declared := strings.TrimSpace(spec.Intent); declared != "" && declared != reg.Intent {
		return nil, &ErrActionDefinitionMismatch{BlockKind: block.Kind, ActionID: actionID, Declared: declared, Canonical: reg.Intent}
	}
	risk, err := effectiveRisk(reg.Risk, spec.Risk)
	if err != nil {
		return nil, err
	}
	confirm := reg.ConfirmRequired || spec.ConfirmRequired
	summary := reg.Summary
	if summary == "" {
		summary = reg.Intent
	}
	fingerprint, err := actionFingerprint(value.ID, blockID, actionID, block.Kind, reg.HandlerID, reg.HandlerVersion, reg.Intent, risk, confirm, reg.Payload, input)
	if err != nil {
		return nil, fmt.Errorf("work: ExecuteBlockAction: canonical fingerprint: %w", err)
	}
	return &resolvedAction{block: block, spec: spec, registration: reg, risk: risk, confirm: confirm, summary: summary, fingerprint: fingerprint}, nil
}

func (s *Service) runReservedAction(ctx context.Context, record ActionReceiptRecord, resolved *resolvedAction, input map[string]any) (*ActionReceipt, error) {
	if riskRequiresApproval(resolved.risk) || resolved.confirm {
		checker := s.permissionChecker()
		if nilutil.IsNil(checker) {
			message := ErrActionPermissionUnavailable.Error()
			receipt, err := s.finishBeforeRun(context.Background(), record, ActionFailed, message, true)
			if err != nil {
				return receipt, err
			}
			return receipt, ErrActionPermissionUnavailable
		}
		decision, err := checker.CheckPermission(ctx, PermissionRequest{
			WorkID: record.WorkID, BlockID: record.BlockID, ActionID: record.ActionID, RequestID: record.RequestID,
			HandlerID: record.HandlerID, HandlerVersion: record.HandlerVersion,
			Object:   ObjectContext{Kind: ObjectBlock, ID: record.BlockID, ParentID: record.WorkID},
			ToolName: resolved.registration.Intent, Risk: string(resolved.risk), Summary: resolved.summary,
			ConfirmRequired: resolved.confirm, Input: input,
		})
		if err != nil {
			message := "permission/approval failed: " + err.Error()
			receipt, persistErr := s.finishBeforeRun(context.Background(), record, ActionFailed, message, true)
			if persistErr != nil {
				return receipt, errors.Join(err, persistErr)
			}
			return receipt, err
		}
		if decision.ApprovalRequired {
			message := "permission checker returned unresolved approval"
			receipt, err := s.finishBeforeRun(ctx, record, ActionFailed, message, true)
			if err != nil {
				return receipt, err
			}
			return receipt, errors.New(message)
		}
		if !decision.Allowed {
			message := strings.TrimSpace(decision.Reason)
			if message == "" {
				message = "blocked by permission policy or rejected by user"
			}
			receipt, err := s.finishBeforeRun(ctx, record, ActionRejected, message, true)
			if err != nil {
				return receipt, err
			}
			return receipt, &ErrActionRejected{RequestID: record.RequestID, Message: message}
		}
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		message := "action cancelled before handler start: " + cancelErr.Error()
		receipt, persistErr := s.finishBeforeRun(context.Background(), record, ActionFailed, message, true)
		if persistErr != nil {
			return receipt, errors.Join(cancelErr, persistErr)
		}
		return receipt, cancelErr
	}

	running, revision, err := s.transitionAction(ctx, record, ActionPending, ActionRunning, "", false, false, nil)
	if err != nil {
		if errors.Is(err, errActionTransitionLost) && terminalReceipt(running.Status) {
			return receiptResult(running, revision)
		}
		return record.toPublicReceipt(revision), fmt.Errorf("work: ExecuteBlockAction: persist running: %w", err)
	}
	if emitErr := s.emitActionSnapshot(record.WorkID, record.RequestID); emitErr != nil {
		return running.toPublicReceipt(revision), emitErr
	}
	if cancelErr := ctx.Err(); cancelErr != nil {
		message := "action cancelled before handler start: " + cancelErr.Error()
		final, finalRev, persistErr := s.transitionAction(context.Background(), running, ActionRunning, ActionFailed, message, true, true, nil)
		if persistErr != nil {
			return running.toPublicReceipt(revision), errors.Join(cancelErr, persistErr)
		}
		if emitErr := s.emitActionSnapshot(record.WorkID, record.RequestID); emitErr != nil {
			return final.toPublicReceipt(finalRev), errors.Join(cancelErr, emitErr)
		}
		return final.toPublicReceipt(finalRev), cancelErr
	}

	handlerInput, err := cloneJSONMap(input)
	if err != nil {
		return running.toPublicReceipt(revision), err
	}
	handlerCtx := ActionHandlerContext{
		WorkID: record.WorkID, BlockID: record.BlockID, ActionID: record.ActionID, RequestID: record.RequestID,
		HandlerID: record.HandlerID, HandlerVersion: record.HandlerVersion,
		Input: handlerInput, Payload: append(json.RawMessage(nil), resolved.registration.Payload...),
		Fingerprint: record.Fingerprint, Risk: resolved.risk,
	}
	type handlerOutcome struct {
		result *ActionResult
		err    error
	}
	handlerDone := make(chan handlerOutcome, 1)
	go func() {
		result, handlerErr := resolved.registration.Handler(ctx, handlerCtx)
		handlerDone <- handlerOutcome{result: result, err: handlerErr}
	}()
	var result *ActionResult
	var handlerErr error
	select {
	case outcome := <-handlerDone:
		result, handlerErr = outcome.result, outcome.err
		if cancelErr := ctx.Err(); cancelErr != nil {
			handlerErr = errors.Join(handlerErr, cancelErr)
		}
	case <-ctx.Done():
		handlerErr = ctx.Err()
	}

	status, message, retryable, known := ActionSucceeded, "", false, true
	var resultData json.RawMessage
	if result != nil {
		message, retryable = result.Message, result.Retryable
		if len(result.Data) > 0 {
			canonical, resultErr := canonicalJSON(result.Data)
			if resultErr != nil {
				handlerErr = errors.Join(handlerErr, fmt.Errorf("handler returned invalid result JSON: %w", resultErr))
				if resolved.risk != RiskRead {
					result.UnknownOutcome = true
				}
			} else {
				resultData = canonical
			}
		}
	}
	if handlerErr != nil {
		status = ActionFailed
		if message == "" {
			message = handlerErr.Error()
		} else {
			message = handlerErr.Error() + ": " + message
		}
		if (result != nil && result.UnknownOutcome) || (resolved.risk != RiskRead && (errors.Is(handlerErr, context.Canceled) || errors.Is(handlerErr, context.DeadlineExceeded))) {
			status, retryable, known = ActionUnknown, false, false
		}
	}
	final, finalRev, persistErr := s.transitionAction(context.Background(), running, ActionRunning, status, message, retryable, known, resultData)
	if persistErr != nil {
		if errors.Is(persistErr, errActionTransitionLost) && terminalReceipt(final.Status) {
			return receiptResult(final, finalRev)
		}
		return running.toPublicReceipt(revision), fmt.Errorf("work: ExecuteBlockAction: handler finished but receipt persistence failed: %w", persistErr)
	}
	if emitErr := s.emitActionSnapshot(record.WorkID, record.RequestID); emitErr != nil {
		return final.toPublicReceipt(finalRev), emitErr
	}
	if handlerErr != nil {
		if status == ActionUnknown {
			return final.toPublicReceipt(finalRev), &ErrActionOutcomeUnknown{RequestID: record.RequestID, Message: message}
		}
		return final.toPublicReceipt(finalRev), fmt.Errorf("work: ExecuteBlockAction: handler: %w", handlerErr)
	}
	return final.toPublicReceipt(finalRev), nil
}

func (s *Service) failPendingHandlerVersion(record ActionReceiptRecord, currentID, currentVersion, reason string) (*ActionReceipt, error) {
	message := "handler version conflict: " + reason
	receipt, persistErr := s.finishBeforeRun(context.Background(), record, ActionFailed, message, true)
	versionErr := actionHandlerVersionError(record, currentID, currentVersion, receipt, reason)
	if persistErr != nil {
		return receipt, errors.Join(versionErr, persistErr)
	}
	return receipt, versionErr
}

func actionHandlerVersionError(record ActionReceiptRecord, currentID, currentVersion string, latest *ActionReceipt, reason string) *ErrActionHandlerVersionConflict {
	retryable := latest != nil && latest.Retryable
	return &ErrActionHandlerVersionConflict{
		RequestID: record.RequestID, HandlerID: record.HandlerID, HandlerVersion: record.HandlerVersion,
		CurrentID: currentID, CurrentVersion: currentVersion, Retryable: retryable, Latest: latest, Reason: reason,
	}
}

func (s *Service) finishBeforeRun(ctx context.Context, record ActionReceiptRecord, status ActionReceiptStatus, message string, retryable bool) (*ActionReceipt, error) {
	final, revision, err := s.transitionAction(ctx, record, ActionPending, status, message, retryable, true, nil)
	if err != nil {
		if errors.Is(err, errActionTransitionLost) && terminalReceipt(final.Status) {
			return receiptResult(final, revision)
		}
		return record.toPublicReceipt(revision), err
	}
	if emitErr := s.emitActionSnapshot(record.WorkID, record.RequestID); emitErr != nil {
		return final.toPublicReceipt(revision), emitErr
	}
	return final.toPublicReceipt(revision), nil
}

func (s *Service) transitionAction(ctx context.Context, previous ActionReceiptRecord, expected, status ActionReceiptStatus, message string, retryable, outcomeKnown bool, result json.RawMessage) (ActionReceiptRecord, int64, error) {
	for attempt := 0; attempt < 8; attempt++ {
		if err := ctx.Err(); err != nil {
			return previous, 0, err
		}
		value, state, err := s.store.LoadState(previous.WorkID, "")
		if err != nil {
			return previous, 0, err
		}
		current, _, found := findActionReceipt(value.ActionReceipts, previous.RequestID)
		if !found {
			return previous, state.Revision, fmt.Errorf("work: action receipt %q disappeared", previous.RequestID)
		}
		if current.Fingerprint != previous.Fingerprint {
			return current, state.Revision, &ErrActionFingerprintConflict{RequestID: previous.RequestID, ExistingFP: current.Fingerprint, IncomingFP: previous.Fingerprint}
		}
		if current.Status != expected {
			if current.Status == status {
				return current, state.Revision, nil
			}
			return current, state.Revision, fmt.Errorf("%w: request %s wanted %s -> %s, current status %s", errActionTransitionLost, previous.RequestID, expected, status, current.Status)
		}
		current.Status, current.Message, current.Retryable, current.OutcomeKnown = status, message, retryable, outcomeKnown
		current.Result = append(json.RawMessage(nil), result...)
		current.UpdatedAt = time.Now().UTC()
		revision, commitErr := s.commitActionRecord(previous.WorkID, previous.RequestID+"/action/"+string(status), EventBlockActionChanged, current, state.Revision)
		if commitErr == nil {
			return current, revision, nil
		}
		var conflict *ErrWorkEventConflict
		if !errors.As(commitErr, &conflict) || conflict.Kind == WorkEventRequestConflict {
			return current, state.Revision, commitErr
		}
	}
	return previous, 0, fmt.Errorf("work: action transition contention exceeded retry limit")
}

func (s *Service) commitActionRecord(workID, eventRequestID string, eventType WorkEventType, record ActionReceiptRecord, baseRevision int64) (int64, error) {
	payload, err := json.Marshal(record)
	if err != nil {
		return 0, err
	}
	event := newServiceEvent(workID, eventRequestID, eventType, payload, record.UpdatedAt)
	event.BaseRevision, event.Revision = baseRevision, baseRevision+1
	return s.store.CommitEvent(workID, event)
}

func (s *Service) emitActionSnapshot(workID, requestID string) error {
	view, err := s.loadView(workID)
	if err != nil {
		return fmt.Errorf("work: action committed but reload failed: %w", err)
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return committedRecovery("action-view", workID, requestID, view.Revision, err)
	}
	return nil
}

func receiptResult(record ActionReceiptRecord, revision int64) (*ActionReceipt, error) {
	receipt := record.toPublicReceipt(revision)
	if record.Status == ActionUnknown {
		return receipt, &ErrActionOutcomeUnknown{RequestID: record.RequestID, Message: record.Message}
	}
	return receipt, nil
}

func (s *Service) beginActionFlight(key string) (*actionFlight, bool) {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	if flight := s.actionRuns[key]; flight != nil {
		return flight, false
	}
	flight := &actionFlight{done: make(chan struct{})}
	if s.actionRuns == nil {
		s.actionRuns = make(map[string]*actionFlight)
	}
	s.actionRuns[key] = flight
	return flight, true
}

func (s *Service) finishActionFlight(key string, flight *actionFlight) {
	s.actionMu.Lock()
	if s.actionRuns[key] == flight {
		delete(s.actionRuns, key)
		close(flight.done)
	}
	s.actionMu.Unlock()
}

func waitActionFlight(ctx context.Context, flight *actionFlight) error {
	select {
	case <-flight.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Service) lookupAction(blockKind, actionID string) (ActionRegistration, bool) {
	s.actionCfgMu.RLock()
	registry := s.actions
	s.actionCfgMu.RUnlock()
	if registry == nil {
		return ActionRegistration{}, false
	}
	return registry.Lookup(blockKind, actionID)
}

func (s *Service) permissionChecker() PermissionChecker {
	s.actionCfgMu.RLock()
	checker := s.permissions
	s.actionCfgMu.RUnlock()
	if nilutil.IsNil(checker) {
		return nil
	}
	return checker
}

func findBlock(value *Work, blockID string) *BlockInstance {
	for index := range value.Blocks {
		if value.Blocks[index].ID == blockID {
			return &value.Blocks[index]
		}
	}
	return nil
}

func findActionSpec(block *BlockInstance, actionID string) *BlockActionSpec {
	for index := range block.Actions {
		if block.Actions[index].ID == actionID {
			return &block.Actions[index]
		}
	}
	return nil
}
