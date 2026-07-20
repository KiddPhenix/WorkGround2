package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var errActionTransitionLost = errors.New("work: action transition lost to another executor")

type actionFlight struct{ done chan struct{} }

func (s *Service) SetActionRegistry(reg *ActionRegistry) { s.actions = reg }

func (s *Service) SetPermissionChecker(checker PermissionChecker) { s.permissions = checker }

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
	inputDigest, err := actionInputDigest(workID, blockID, actionID, inputCopy)
	if err != nil {
		return nil, fmt.Errorf("work: ExecuteBlockAction: fingerprint input: %w", err)
	}
	flightKey := workID + "\x00" + requestID

	for {
		current, state, loadErr := s.store.LoadState(workID, "")
		if loadErr != nil {
			return nil, fmt.Errorf("work: ExecuteBlockAction: load state: %w", loadErr)
		}
		if existing, _, found := findActionReceipt(current.ActionReceipts, requestID); found {
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
				unknown, rev, transitionErr := s.transitionAction(ctx, existing, ActionRunning, ActionUnknown,
					"previous executor stopped before recording the outcome; verify external state before retrying", false, false, nil)
				if transitionErr != nil {
					if errors.Is(transitionErr, errActionTransitionLost) && terminalReceipt(unknown.Status) {
						return receiptResult(unknown, rev)
					}
					return existing.toPublicReceipt(state.Revision), transitionErr
				}
				if emitErr := s.emitActionSnapshot(workID, requestID); emitErr != nil {
					return unknown.toPublicReceipt(rev), emitErr
				}
				return unknown.toPublicReceipt(rev), &ErrActionOutcomeUnknown{RequestID: requestID, Message: unknown.Message}
			}

			resolved, resolveErr := s.resolveAction(current, blockID, actionID, inputCopy)
			if resolveErr != nil {
				failed, persistErr := s.finishBeforeRun(context.Background(), existing, ActionFailed, resolveErr.Error(), false)
				return failed, errors.Join(resolveErr, persistErr)
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
		now := time.Now().UTC()
		record := ActionReceiptRecord{
			WorkID: workID, BlockID: blockID, BlockKind: resolved.block.Kind, ActionID: actionID,
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
	fingerprint, err := actionFingerprint(value.ID, blockID, actionID, block.Kind, reg.Intent, risk, confirm, reg.Payload, input)
	if err != nil {
		return nil, fmt.Errorf("work: ExecuteBlockAction: canonical fingerprint: %w", err)
	}
	return &resolvedAction{block: block, spec: spec, registration: reg, risk: risk, confirm: confirm, summary: summary, fingerprint: fingerprint}, nil
}

func (s *Service) runReservedAction(ctx context.Context, record ActionReceiptRecord, resolved *resolvedAction, input map[string]any) (*ActionReceipt, error) {
	if riskRequiresApproval(resolved.risk) || resolved.confirm {
		if s.permissions == nil {
			message := "no permission checker configured"
			receipt, err := s.finishBeforeRun(ctx, record, ActionRejected, message, true)
			if err != nil {
				return receipt, err
			}
			return receipt, &ErrActionRejected{RequestID: record.RequestID, Message: message}
		}
		decision, err := s.permissions.CheckPermission(ctx, PermissionRequest{
			WorkID: record.WorkID, BlockID: record.BlockID, ActionID: record.ActionID, RequestID: record.RequestID,
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

	handlerInput, err := cloneJSONMap(input)
	if err != nil {
		return running.toPublicReceipt(revision), err
	}
	result, handlerErr := resolved.registration.Handler(ctx, ActionHandlerContext{
		WorkID: record.WorkID, BlockID: record.BlockID, ActionID: record.ActionID, RequestID: record.RequestID,
		Input: handlerInput, Payload: append(json.RawMessage(nil), resolved.registration.Payload...),
		Fingerprint: record.Fingerprint, Risk: resolved.risk,
	})

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
	if s.actions == nil {
		return ActionRegistration{}, false
	}
	return s.actions.Lookup(blockKind, actionID)
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
