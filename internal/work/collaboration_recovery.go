package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"
)

// CreateCandidateRevisionInput is the Desktop intent for producing a
// copy-on-write definition candidate. Creating a candidate never switches the
// active definition; ApplyDefinition remains the only activation boundary.
type CreateCandidateRevisionInput struct {
	WorkID                 string                       `json:"workId"`
	Intent                 string                       `json:"intent"`
	BaseDefinitionRevision int64                        `json:"baseDefinitionRevision"`
	ExpectedRevision       int64                        `json:"expectedRevision"`
	RequestID              string                       `json:"requestId"`
	InferName              bool                         `json:"inferName,omitempty"`
	StructuralAnswers      []DefinitionStructuralAnswer `json:"structuralAnswers,omitempty"`
}

// CreateCandidateRevisionResult keeps a committed candidate observable when a
// later projection refresh fails.
type CreateCandidateRevisionResult struct {
	Candidate      *WorkDefinitionRevision            `json:"candidate,omitempty"`
	Clarification  *DefinitionStructuralClarification `json:"clarification,omitempty"`
	Impact         *RunImpact                         `json:"impact,omitempty"`
	Revision       int64                              `json:"revision"`
	Duplicate      bool                               `json:"duplicate"`
	Committed      bool                               `json:"committed"`
	Recoverable    bool                               `json:"recoverable"`
	TransportError *WorkTransportError                `json:"transportError,omitempty"`
}

var definitionPlanGates = struct {
	sync.Mutex
	values map[string]*definitionPlanGate
}{values: make(map[string]*definitionPlanGate)}

type definitionPlanGate struct {
	mu   sync.Mutex
	refs int
}

func lockDefinitionPlan(workID, requestID string) func() {
	key := workID + "\x00" + requestID
	definitionPlanGates.Lock()
	gate := definitionPlanGates.values[key]
	if gate == nil {
		gate = &definitionPlanGate{}
		definitionPlanGates.values[key] = gate
	}
	gate.refs++
	definitionPlanGates.Unlock()
	gate.mu.Lock()
	return func() {
		gate.mu.Unlock()
		definitionPlanGates.Lock()
		gate.refs--
		if gate.refs == 0 {
			delete(definitionPlanGates.values, key)
		}
		definitionPlanGates.Unlock()
	}
}

// CreateCandidateRevisionWithResult exposes the existing durable candidate
// writer through a transport-safe typed result.
func (s *Service) CreateCandidateRevisionWithResult(
	ctx context.Context,
	input CreateCandidateRevisionInput,
) (result *CreateCandidateRevisionResult, err error) {
	result = &CreateCandidateRevisionResult{}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.Intent = strings.TrimSpace(input.Intent)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if s == nil || s.store == nil {
		err := errors.New("work: CreateCandidateRevision: service is not configured")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if input.WorkID == "" || input.Intent == "" || input.RequestID == "" {
		err := errors.New("work: CreateCandidateRevision: workID/intent/requestID are required")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if input.BaseDefinitionRevision <= 0 {
		err := errors.New("work: CreateCandidateRevision: baseDefinitionRevision must be positive")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	var release func() error
	if locker, ok := s.store.(interface {
		AcquireDefinitionPlanLease(context.Context, string, string) (func() error, error)
	}); ok {
		release, err = locker.AcquireDefinitionPlanLease(ctx, input.WorkID, input.RequestID)
		if err != nil {
			result.TransportError = TransportErrorFrom(err)
			return result, err
		}
	} else {
		unlock := lockDefinitionPlan(input.WorkID, input.RequestID)
		release = func() error {
			unlock()
			return nil
		}
	}
	defer func() {
		if releaseErr := release(); releaseErr != nil {
			if result.Committed {
				releaseErr = committedRecovery(
					"definition-plan-lease", input.WorkID, input.RequestID, result.Revision, releaseErr,
				)
			}
			err = errors.Join(err, releaseErr)
			result.TransportError = TransportErrorFrom(err)
			if result.TransportError != nil {
				result.Committed = result.Committed || result.TransportError.Committed
				result.Recoverable = result.TransportError.Recoverable
			}
		}
	}()

	before, beforeState, beforeErr := s.store.LoadState(input.WorkID, "")
	if beforeErr != nil {
		result.TransportError = TransportErrorFrom(beforeErr)
		return result, beforeErr
	}
	intentDigest := hashDefinitionPlanIntent(input)
	replay, receiptErr := s.loadV2Receipt(input.WorkID, input.RequestID)
	if receiptErr != nil && !errors.Is(receiptErr, ErrWorkNotFound) {
		result.TransportError = TransportErrorFrom(receiptErr)
		return result, receiptErr
	}
	if replay != nil {
		result.Duplicate = true
		if replay.Operation != "CreateCandidateRevision" {
			err := &ErrWorkEventConflict{
				WorkID: input.WorkID, RequestID: input.RequestID, Kind: WorkEventRequestConflict,
				Reason: "requestID already belongs to " + replay.Operation,
			}
			result.TransportError = TransportErrorFrom(err)
			return result, err
		}
		if replay.IntentDigest != intentDigest {
			err := &ErrWorkEventConflict{
				WorkID: input.WorkID, RequestID: input.RequestID, Kind: WorkEventRequestConflict,
				Reason: "requestID reused with a different structure intent or base revision",
			}
			result.TransportError = TransportErrorFrom(err)
			return result, err
		}
		stored, loadErr := s.definitionStore().LoadRevision(input.WorkID, replay.ResultRevision)
		if loadErr != nil {
			result.TransportError = TransportErrorFrom(loadErr)
			return result, loadErr
		}
		base, loadErr := s.definitionStore().LoadRevision(input.WorkID, stored.ParentRevision)
		if loadErr != nil {
			recovery := committedRecovery(
				"candidate-base", input.WorkID, input.RequestID, beforeState.Revision, loadErr,
			)
			result.Candidate = stored
			result.Committed = true
			result.Recoverable = true
			result.Revision = beforeState.Revision
			result.TransportError = TransportErrorFrom(recovery)
			return result, recovery
		}
		result.Candidate = stored
		result.Committed = true
		result.Revision = beforeState.Revision
		result.Impact = ClassifyRunImpact(base, stored)
		return result, nil
	}

	baseRevision := before.V2CurrentRevision
	if baseRevision == 0 {
		baseRevision = before.V2LatestRevision
	}
	if input.BaseDefinitionRevision != baseRevision {
		err := revisionConflict(input.WorkID, input.BaseDefinitionRevision, baseRevision)
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	base, loadErr := s.definitionStore().LoadRevision(input.WorkID, baseRevision)
	if loadErr != nil {
		result.TransportError = TransportErrorFrom(loadErr)
		return result, loadErr
	}
	if input.ExpectedRevision != beforeState.Revision {
		err := revisionConflict(input.WorkID, input.ExpectedRevision, beforeState.Revision)
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	planner := s.definitionPlanner()
	if planner == nil {
		err := fmt.Errorf("%w: retry after a planner/provider is configured", ErrDefinitionPlannerUnavailable)
		result.Recoverable = true
		result.TransportError = &WorkTransportError{
			Code: "planner_unavailable", Message: err.Error(), WorkID: input.WorkID,
			RequestID: input.RequestID, Recoverable: true,
		}
		return result, err
	}
	plannerWork, cloneErr := cloneWork(before)
	if cloneErr != nil {
		result.TransportError = TransportErrorFrom(cloneErr)
		return result, cloneErr
	}
	emitProgress := s.definitionProgressEmitter(input.WorkID, input.RequestID, beforeState.Revision)
	plan, planErr := planner.PlanDefinition(ctx, DefinitionPlanInput{
		Intent:            input.Intent,
		Work:              plannerWork,
		Base:              cloneDefinitionForView(base),
		StructuralAnswers: append([]DefinitionStructuralAnswer(nil), input.StructuralAnswers...),
		OnProgress:        emitProgress,
	})
	if planErr != nil {
		err := fmt.Errorf("%w: %v", ErrDefinitionPlannerFailed, planErr)
		result.Recoverable = true
		result.TransportError = &WorkTransportError{
			Code: "planner_failed", Message: err.Error(), WorkID: input.WorkID,
			RequestID: input.RequestID, Recoverable: true,
		}
		return result, err
	}
	if plan == nil {
		err := fmt.Errorf("%w: planner returned no definition", ErrDefinitionPlannerFailed)
		result.Recoverable = true
		result.TransportError = &WorkTransportError{
			Code: "planner_failed", Message: err.Error(), WorkID: input.WorkID,
			RequestID: input.RequestID, Recoverable: true,
		}
		return result, err
	}
	clarification, clarificationErr := nextDefinitionStructuralClarification(plan, input.StructuralAnswers)
	if clarificationErr != nil {
		err := fmt.Errorf("%w: invalid structural question: %v", ErrDefinitionPlannerFailed, clarificationErr)
		result.Recoverable = true
		result.TransportError = &WorkTransportError{
			Code: "planner_failed", Message: err.Error(), WorkID: input.WorkID,
			RequestID: input.RequestID, Recoverable: true,
		}
		return result, err
	}
	if clarification != nil {
		result.Clarification = clarification
		result.Revision = beforeState.Revision
		result.Recoverable = true
		emitProgress(DefinitionPlanProgress{
			Kind: "clarification",
			Text: result.Clarification.Question,
		})
		return result, nil
	}
	candidateInput := &WorkDefinitionRevision{
		Goal:          strings.TrimSpace(plan.Goal),
		Nodes:         append([]NodeDef(nil), plan.Nodes...),
		ArtifactSlots: append([]ArtifactSlotDef(nil), plan.ArtifactSlots...),
		InputSpecs:    append([]InputSpec(nil), plan.InputSpecs...),
		CreatedBy:     base.CreatedBy,
	}
	if validationErr := validateDefinitionPlan(input.WorkID, base.Revision, candidateInput); validationErr != nil {
		err := fmt.Errorf("%w: invalid planner output: %v", ErrDefinitionPlannerFailed, validationErr)
		result.Recoverable = true
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if candidateSemanticEqual(base, candidateInput) {
		err := fmt.Errorf("%w: planner returned an unchanged definition", ErrDefinitionPlannerNoChange)
		result.Recoverable = true
		result.TransportError = &WorkTransportError{
			Code: "planner_no_change", Message: err.Error(), WorkID: input.WorkID,
			RequestID: input.RequestID, Recoverable: true,
		}
		return result, err
	}
	candidateInput.CreatedBy = "planner"
	suggestedName := ""
	if input.InferName {
		suggestedName = workNameFromPrompt(candidateInput.Goal, before.Name)
	}
	// Planning can take long enough for unrelated runtime/input events to
	// advance the Work event log. Rebase only the aggregate Work revision when
	// the active Definition is still the exact base that the planner saw. This
	// preserves the generated candidate and avoids a second model call while
	// keeping real Definition changes explicit.
	candidateExpectedRevision := input.ExpectedRevision
	var candidate *WorkDefinitionRevision
	for attempt := 0; attempt < 2; attempt++ {
		candidate, err = s.createCandidateRevision(
			ctx, input.WorkID, candidateInput, input.RequestID, candidateExpectedRevision, intentDigest, suggestedName,
		)
		if err == nil || !isRevisionEventConflict(err) || attempt > 0 {
			break
		}
		latest, latestState, loadErr := s.store.LoadState(input.WorkID, "")
		if loadErr != nil {
			err = errors.Join(err, fmt.Errorf("work: reload candidate base after revision conflict: %w", loadErr))
			break
		}
		latestBase := latest.V2CurrentRevision
		if latestBase == 0 {
			latestBase = latest.V2LatestRevision
		}
		if latestBase != baseRevision {
			break
		}
		candidateExpectedRevision = latestState.Revision
	}
	result.Candidate = candidate
	if candidate != nil {
		result.Committed = true
		result.Impact = ClassifyRunImpact(base, candidate)
	}
	if _, state, loadErr := s.store.LoadState(input.WorkID, ""); loadErr == nil {
		result.Revision = state.Revision
	} else if result.Committed {
		err = errors.Join(err, committedRecovery(
			"candidate-refresh", input.WorkID, input.RequestID, result.Revision, loadErr,
		))
	}
	result.TransportError = TransportErrorFrom(err)
	if result.TransportError != nil {
		result.Committed = result.Committed || result.TransportError.Committed
		result.Recoverable = result.TransportError.Recoverable
	}
	return result, err
}

func isRevisionEventConflict(err error) bool {
	var conflict *ErrWorkEventConflict
	return errors.As(err, &conflict) && conflict.Kind == WorkEventRevisionConflict
}

// ErrDefinitionPlannerUnavailable is retryable configuration state. The
// endpoint never clones the active definition as a fallback.
var ErrDefinitionPlannerUnavailable = errors.New("work: definition planner is unavailable")

var (
	ErrDefinitionPlannerFailed   = errors.New("work: definition planner failed")
	ErrDefinitionPlannerNoChange = errors.New("work: definition planner produced no change")
)

func hashDefinitionPlanIntent(input CreateCandidateRevisionInput) string {
	value := struct {
		WorkID                 string
		Intent                 string
		BaseDefinitionRevision int64
		InferName              bool
		StructuralAnswers      []DefinitionStructuralAnswer
	}{
		WorkID:                 strings.TrimSpace(input.WorkID),
		Intent:                 strings.TrimSpace(input.Intent),
		BaseDefinitionRevision: input.BaseDefinitionRevision,
		InferName:              input.InferName,
		StructuralAnswers:      input.StructuralAnswers,
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("definition-plan-%x", sum[:])
}

func retryArtifactSlotIntentDigest(input RetryArtifactSlotRequest) string {
	value := struct {
		WorkID             string
		SlotID             string
		DefinitionRevision int64
	}{
		WorkID:             strings.TrimSpace(input.WorkID),
		SlotID:             strings.TrimSpace(input.SlotID),
		DefinitionRevision: input.DefinitionRevision,
	}
	raw, _ := json.Marshal(value)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("retry-artifact-slot-%x", sum[:])
}

func retryArtifactSlotNoopDigest(input RetryArtifactSlotRequest) string {
	return retryArtifactSlotIntentDigest(input) + "/satisfied"
}

func validateDefinitionPlan(workID string, baseRevision int64, candidate *WorkDefinitionRevision) error {
	if candidate == nil {
		return errors.New("candidate is nil")
	}
	preview := *candidate
	preview.WorkID = workID
	preview.Revision = baseRevision + 1
	preview.ParentRevision = baseRevision
	preview.Status = DefDraft
	if err := ValidateDefinitionRevision(&preview); err != nil {
		return err
	}
	producers := make(map[string]int, len(preview.ArtifactSlots))
	for _, node := range preview.Nodes {
		for _, slotID := range node.ProducesSlotIDs {
			producers[slotID]++
		}
	}
	for _, slot := range preview.ArtifactSlots {
		if producers[slot.ID] != 1 {
			return fmt.Errorf("artifact slot %q must have exactly one producer, got %d", slot.ID, producers[slot.ID])
		}
	}
	return nil
}

// RetryArtifactSlotRequest identifies a slot. Producer task identity is
// derived from the active definition and active run; callers cannot retry a
// historical task by supplying their own run/task IDs.
type RetryArtifactSlotRequest struct {
	WorkID             string `json:"workId"`
	SlotID             string `json:"slotId"`
	DefinitionRevision int64  `json:"definitionRevision"`
	ExpectedRevision   int64  `json:"expectedRevision"`
	RequestID          string `json:"requestId"`
}

// RetryArtifactSlotResult is the durable result of resetting a failed,
// partial, or stale slot and waking its unique producer.
type RetryArtifactSlotResult struct {
	Slot           *ArtifactSlot       `json:"slot,omitempty"`
	Revision       int64               `json:"revision"`
	Duplicate      bool                `json:"duplicate"`
	Committed      bool                `json:"committed"`
	Recoverable    bool                `json:"recoverable"`
	TransportError *WorkTransportError `json:"transportError,omitempty"`
}

// RetryArtifactSlot persists the slot transition before scheduling. If
// invalidation or scheduling fails after that commit, the caller receives a
// committed-recovery error and can safely repeat the same request.
func (s *Service) RetryArtifactSlot(
	ctx context.Context,
	input RetryArtifactSlotRequest,
) (*RetryArtifactSlotResult, error) {
	result := &RetryArtifactSlotResult{}
	if err := checkServiceContext(ctx); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if err := s.requireStore(); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.SlotID = strings.TrimSpace(input.SlotID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.SlotID == "" || input.RequestID == "" {
		err := errors.New("work: RetryArtifactSlot: workID/slotID/requestID are required")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if input.ExpectedRevision <= 0 {
		err := errors.New("work: RetryArtifactSlot: expectedRevision must be positive")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if input.DefinitionRevision <= 0 {
		err := errors.New("work: RetryArtifactSlot: definitionRevision must be positive")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}

	slotRequestID := input.RequestID + "/slot/artifact-slot"
	retryIntentDigest := retryArtifactSlotIntentDigest(input)
	noopIntentDigest := retryArtifactSlotNoopDigest(input)
	current, slotState, err := s.store.LoadState(input.WorkID, slotRequestID)
	if err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	var slotResult *ArtifactSlotResult
	slotCommitted := false
	alreadySatisfied := false
	if slotState.RequestFound {
		result.Duplicate = true
		slotCommitted = true
		slotResult, alreadySatisfied, err = s.loadArtifactRetryReceipt(
			input, slotRequestID, retryIntentDigest, noopIntentDigest,
		)
	} else {
		if input.ExpectedRevision > slotState.Revision {
			err = revisionConflict(input.WorkID, input.ExpectedRevision, slotState.Revision)
		}
		const maxArtifactRetryRebases = 3
		for attempt := 0; err == nil && attempt < maxArtifactRetryRebases; attempt++ {
			if attempt > 0 {
				current, slotState, err = s.store.LoadState(input.WorkID, slotRequestID)
				if err != nil {
					break
				}
				if slotState.RequestFound {
					slotResult, alreadySatisfied, err = s.loadArtifactRetryReceipt(
						input, slotRequestID, retryIntentDigest, noopIntentDigest,
					)
					slotCommitted = true
					result.Duplicate = true
					break
				}
			}

			if err = validateArtifactRetryRevision(current, input); err == nil {
				err = CheckSchemaVersionV2("Work", current.SchemaVersion)
			}
			if err == nil {
				err = s.validateRetryArtifactSlot(current, input.SlotID, input.DefinitionRevision)
			}
			var definition *WorkDefinitionRevision
			if err == nil {
				definition, err = s.definitionStore().LoadRevision(input.WorkID, input.DefinitionRevision)
			}
			var slot *ArtifactSlot
			if err == nil {
				slot, _ = FindArtifactSlotRevision(current, input.DefinitionRevision, input.SlotID)
				err = validateArtifactRetryContract(input, slot, definition)
			}
			attemptSatisfied := err == nil &&
				(slot.State == SlotGenerating || slot.State == SlotReady)
			if err == nil && !attemptSatisfied {
				runID := activeDefinitionRunID(current, definition.Digest)
				_, _, err = artifactProducerRuntime(current, definition, runID, input.SlotID)
			}
			if err != nil {
				break
			}

			if attemptSatisfied {
				slotResult = artifactSlotResult(slot, slotState.Revision)
				slotCommitted = true
				alreadySatisfied = true
				err = nil
				break
			}
			slotIntent := UpdateArtifactSlotInput{
				WorkID:           input.WorkID,
				SlotID:           input.SlotID,
				RequestID:        input.RequestID + "/slot",
				State:            SlotGenerating,
				Refs:             append([]ArtifactRef(nil), slot.ArtifactRefs...),
				UpstreamDigest:   slot.UpstreamDigest,
				Progress:         slot.Progress,
				Summary:          slot.Summary,
				Revision:         slot.Revision + 1,
				ExpectedRevision: slotState.Revision,
				DefinitionRev:    input.DefinitionRevision,
				intentDigest:     retryIntentDigest,
			}
			slotResult, err = s.UpdateArtifactSlot(ctx, slotIntent)
			if err == nil {
				slotCommitted = true
				alreadySatisfied = attemptSatisfied
				break
			}
			if isRevisionEventConflict(err) && attempt+1 < maxArtifactRetryRebases {
				continue
			}

			_, committedState, stateErr := s.store.LoadState(input.WorkID, slotRequestID)
			if stateErr != nil {
				err = errors.Join(err, stateErr)
			} else if committedState.RequestFound {
				slotCommitted = true
				var loadErr error
				slotResult, alreadySatisfied, loadErr = s.loadArtifactRetryReceipt(
					input, slotRequestID, retryIntentDigest, noopIntentDigest,
				)
				if loadErr != nil {
					err = errors.Join(err, loadErr)
				} else {
					err = nil
					result.Duplicate = true
				}
			}
			break
		}
	}
	if slotResult == nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	slotCopy := slotResult.Slot
	result.Slot = &slotCopy
	result.Revision = slotResult.WorkRevision
	result.Committed = slotCommitted
	if err != nil {
		if !slotCommitted {
			result.TransportError = TransportErrorFrom(err)
			return result, err
		}
		err = committedRecovery("retry-artifact-slot", input.WorkID, input.RequestID, result.Revision, err)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}

	projection, loadErr := s.store.LoadProjection(input.WorkID)
	if loadErr != nil {
		err = committedRecovery("retry-artifact-reload", input.WorkID, input.RequestID, result.Revision, loadErr)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	if projection.V2CurrentRevision != input.DefinitionRevision {
		err = committedRecovery(
			"retry-artifact-revision", input.WorkID, input.RequestID, result.Revision,
			fmt.Errorf(
				"definition revision changed from %d to %d before producer invalidation",
				input.DefinitionRevision, projection.V2CurrentRevision,
			),
		)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	if alreadySatisfied {
		if authoritative, _ := FindArtifactSlotRevision(projection, input.DefinitionRevision, input.SlotID); authoritative != nil {
			slotCopy := *authoritative
			slotCopy.ArtifactRefs = append([]ArtifactRef(nil), authoritative.ArtifactRefs...)
			result.Slot = &slotCopy
		}
		result.TransportError = nil
		return result, nil
	}
	definition, loadErr := s.definitionStore().LoadRevision(input.WorkID, projection.V2CurrentRevision)
	if loadErr != nil {
		err = committedRecovery("retry-artifact-definition", input.WorkID, input.RequestID, result.Revision, loadErr)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	runID := activeDefinitionRunID(projection, definition.Digest)
	node, runtime, identityErr := artifactProducerRuntime(projection, definition, runID, input.SlotID)
	if identityErr != nil {
		err = committedRecovery("retry-artifact-identity", input.WorkID, input.RequestID, result.Revision, identityErr)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}

	if runtime.State != TaskFailedRetryable && runtime.State != TaskInvalidated {
		invalidateID := input.RequestID + "/invalidate"
		_, invalidateState, stateErr := s.store.LoadState(input.WorkID, invalidateID)
		if stateErr != nil {
			err = committedRecovery("retry-artifact-invalidate-load", input.WorkID, input.RequestID, result.Revision, stateErr)
			result.TransportError = TransportErrorFrom(err)
			result.Recoverable = true
			return result, err
		}
		if !invalidateState.RequestFound {
			payload, _ := json.Marshal(TaskInvalidatedPayload{
				TaskID: runtime.TaskID, WorkID: input.WorkID, RunID: runID,
				Reason: "artifact slot retry: " + input.SlotID,
			})
			event := newServiceEventV2(input.WorkID, invalidateID, EventTaskInvalidated, payload, time.Now().UTC())
			event.BaseRevision, event.Revision = invalidateState.Revision, invalidateState.Revision+1
			event.Object = ObjectContext{
				Kind: ObjectTask, ID: runtime.TaskID, WorkID: input.WorkID,
				RunID: runID, TaskID: runtime.TaskID,
				DefinitionRevision: int64Ptr(definition.Revision),
				ExpectedRevision:   int64Ptr(invalidateState.Revision),
			}
			if _, commitErr := s.store.CommitEvent(input.WorkID, event); commitErr != nil {
				err = committedRecovery("retry-artifact-invalidate", input.WorkID, input.RequestID, result.Revision, commitErr)
				result.TransportError = TransportErrorFrom(err)
				result.Recoverable = true
				return result, err
			}
		}
		projection, loadErr = s.store.LoadProjection(input.WorkID)
		if loadErr != nil {
			err = committedRecovery("retry-artifact-invalidated-reload", input.WorkID, input.RequestID, result.Revision, loadErr)
			result.TransportError = TransportErrorFrom(err)
			result.Recoverable = true
			return result, err
		}
		runtime = projection.V2TaskRuntimes[runtime.TaskID]
	}

	producerRequestID := input.RequestID + "/producer"
	projection, loadErr = s.store.LoadProjection(input.WorkID)
	if loadErr != nil {
		err = committedRecovery("retry-artifact-pre-schedule", input.WorkID, input.RequestID, result.Revision, loadErr)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	if projection.V2CurrentRevision != input.DefinitionRevision {
		err = committedRecovery(
			"retry-artifact-revision", input.WorkID, input.RequestID, result.Revision,
			fmt.Errorf(
				"definition revision changed from %d to %d before producer scheduling",
				input.DefinitionRevision, projection.V2CurrentRevision,
			),
		)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	_, producerState, stateErr := s.store.LoadState(input.WorkID, producerRequestID+"/retry-node")
	if stateErr != nil {
		err = committedRecovery("retry-artifact-revision", input.WorkID, input.RequestID, result.Revision, stateErr)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	retryExpectedRevision := producerState.Revision
	if producerState.RequestFound {
		retryExpectedRevision = producerState.RequestRevision - 1
	}
	nodeResult, retryErr := s.RetryWorkNode(ctx, RetryWorkNodeRequest{
		WorkID: input.WorkID, RunID: runID, TaskID: runtime.TaskID,
		ExpectedRevision: retryExpectedRevision, RequestID: producerRequestID,
	})
	if nodeResult != nil && nodeResult.Revision > result.Revision {
		result.Revision = nodeResult.Revision
	}
	if retryErr != nil {
		err = committedRecovery("retry-artifact-schedule", input.WorkID, input.RequestID, result.Revision, retryErr)
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}
	_ = node
	result.TransportError = nil
	return result, nil
}

func (s *Service) loadArtifactRetryReceipt(
	input RetryArtifactSlotRequest,
	slotRequestID, retryIntentDigest, noopIntentDigest string,
) (*ArtifactSlotResult, bool, error) {
	loader, ok := s.store.(interface {
		LoadArtifactSlotUpdate(string, string) (*ArtifactSlotResult, string, error)
	})
	if !ok {
		return nil, false, errors.New("work: RetryArtifactSlot: store cannot recover slot receipt")
	}
	slotResult, storedDigest, err := loader.LoadArtifactSlotUpdate(input.WorkID, slotRequestID)
	if err != nil {
		return nil, false, err
	}
	if slotResult == nil {
		return nil, false, fmt.Errorf("%w: empty artifact retry receipt %s", ErrWorkNeedsRepair, slotRequestID)
	}
	legacyIntent := UpdateArtifactSlotInput{
		WorkID:           input.WorkID,
		SlotID:           input.SlotID,
		RequestID:        slotRequestID,
		State:            SlotGenerating,
		Refs:             append([]ArtifactRef(nil), slotResult.Slot.ArtifactRefs...),
		UpstreamDigest:   slotResult.Slot.UpstreamDigest,
		Summary:          slotResult.Slot.Summary,
		Revision:         slotResult.Slot.Revision,
		ExpectedRevision: slotResult.WorkRevision - 1,
		DefinitionRev:    input.DefinitionRevision,
	}
	if slotResult.Slot.ID != input.SlotID ||
		slotResult.Slot.DefinitionRev != input.DefinitionRevision ||
		(storedDigest != retryIntentDigest &&
			storedDigest != noopIntentDigest &&
			storedDigest != artifactSlotIntentDigest(legacyIntent)) {
		conflict := &ErrWorkEventConflict{
			WorkID: input.WorkID, RequestID: input.RequestID,
			Kind:   WorkEventRequestConflict,
			Reason: "RetryArtifactSlot requestID reused with a different slot or revision",
		}
		return nil, false, fmt.Errorf("%w: %w", ErrWorkRequestIDConflict, conflict)
	}
	return slotResult, storedDigest == noopIntentDigest, nil
}

func candidateSemanticEqual(left, right *WorkDefinitionRevision) bool {
	normalize := func(value *WorkDefinitionRevision) *WorkDefinitionRevision {
		if value == nil {
			return nil
		}
		copy := *value
		copy.WorkID = ""
		copy.Revision = 0
		copy.ParentRevision = 0
		copy.Status = ""
		copy.CreatedAt = time.Time{}
		copy.Digest = ""
		if copy.CreatedBy == "" {
			copy.CreatedBy = "planning"
		}
		copy.Nodes = append([]NodeDef(nil), value.Nodes...)
		for i := range copy.Nodes {
			node := &copy.Nodes[i]
			if len(node.DependsOn) == 0 {
				node.DependsOn = nil
			}
			if len(node.InputSpecIDs) == 0 {
				node.InputSpecIDs = nil
			}
			if len(node.ToolHints) == 0 {
				node.ToolHints = nil
			}
			if len(node.BlockIDs) == 0 {
				node.BlockIDs = nil
			}
			if len(node.ProducesSlotIDs) == 0 {
				node.ProducesSlotIDs = nil
			}
			if len(node.ConsumesSlotIDs) == 0 {
				node.ConsumesSlotIDs = nil
			}
		}
		if len(copy.Nodes) == 0 {
			copy.Nodes = nil
		}
		if len(copy.ArtifactSlots) == 0 {
			copy.ArtifactSlots = nil
		}
		if len(copy.InputSpecs) == 0 {
			copy.InputSpecs = nil
		}
		return &copy
	}
	return reflect.DeepEqual(normalize(left), normalize(right))
}

func clonePatchIntentReceipt(receipt PatchIntentReceipt) PatchIntentReceipt {
	copy := receipt
	copy.InvalidatedIDs = append([]string(nil), receipt.InvalidatedIDs...)
	copy.AffectedBlockIDs = append([]string(nil), receipt.AffectedBlockIDs...)
	copy.AffectedArtifactSlotIDs = append([]string(nil), receipt.AffectedArtifactSlotIDs...)
	copy.StaleArtifactSlotIDs = append([]string(nil), receipt.StaleArtifactSlotIDs...)
	if receipt.ResultPatch != nil {
		patch := *receipt.ResultPatch
		patch.Operations = append([]PatchOp(nil), receipt.ResultPatch.Operations...)
		patch.AffectedNodeIDs = append([]string(nil), receipt.ResultPatch.AffectedNodeIDs...)
		patch.AffectedBlockIDs = append([]string(nil), receipt.ResultPatch.AffectedBlockIDs...)
		patch.AffectedArtifactSlotIDs = append([]string(nil), receipt.ResultPatch.AffectedArtifactSlotIDs...)
		patch.StaleArtifactSlotIDs = append([]string(nil), receipt.ResultPatch.StaleArtifactSlotIDs...)
		patch.InvalidatedTaskIDs = append([]string(nil), receipt.ResultPatch.InvalidatedTaskIDs...)
		copy.ResultPatch = &patch
	}
	return copy
}

func validateArtifactRetryRevision(current *Work, input RetryArtifactSlotRequest) error {
	if current == nil {
		return errors.New("work: RetryArtifactSlot: authoritative Work is unavailable")
	}
	if current.V2CurrentRevision != input.DefinitionRevision {
		return &ErrWorkEventConflict{
			WorkID: input.WorkID, RequestID: input.RequestID, Kind: WorkEventRequestConflict,
			Reason: fmt.Sprintf(
				"definition revision %d is not active revision %d",
				input.DefinitionRevision, current.V2CurrentRevision,
			),
		}
	}
	return nil
}

func (s *Service) validateRetryArtifactSlot(current *Work, slotID string, definitionRevision int64) error {
	if current == nil || current.V2CurrentRevision <= 0 {
		return errors.New("work: RetryArtifactSlot: active definition is required")
	}
	if current.ArchiveState != ArchiveActive {
		return fmt.Errorf("work: RetryArtifactSlot: Work is %s", current.ArchiveState)
	}
	slot, _ := FindArtifactSlotRevision(current, definitionRevision, slotID)
	if slot == nil {
		return &ErrWorkEventConflict{
			WorkID: current.ID, Kind: WorkEventRequestConflict,
			Reason: fmt.Sprintf("active slot %q was removed", slotID),
		}
	}
	switch slot.State {
	case SlotReserved, SlotFailed, SlotPartial, SlotStale, SlotGenerating, SlotReady:
		return nil
	default:
		return fmt.Errorf("work: RetryArtifactSlot requires reserved, failed, partial, or stale slot; current state is %s", slot.State)
	}
}

func validateArtifactRetryContract(
	input RetryArtifactSlotRequest,
	slot *ArtifactSlot,
	definition *WorkDefinitionRevision,
) error {
	if slot == nil || definition == nil {
		return &ErrWorkEventConflict{
			WorkID: input.WorkID, RequestID: input.RequestID, Kind: WorkEventRequestConflict,
			Reason: "active artifact slot contract is unavailable",
		}
	}
	for _, declared := range definition.ArtifactSlots {
		if declared.ID != input.SlotID {
			continue
		}
		if slot.DefinitionRev == definition.Revision &&
			slot.Title == declared.Title &&
			slot.Kind == declared.Kind &&
			slot.ExpectedCount == declared.ExpectedCount &&
			slot.Required == declared.Required {
			return nil
		}
		return &ErrWorkEventConflict{
			WorkID: input.WorkID, RequestID: input.RequestID, Kind: WorkEventRequestConflict,
			Reason: fmt.Sprintf("artifact slot %q contract is incompatible with active definition", input.SlotID),
		}
	}
	return &ErrWorkEventConflict{
		WorkID: input.WorkID, RequestID: input.RequestID, Kind: WorkEventRequestConflict,
		Reason: fmt.Sprintf("artifact slot %q was removed from active definition", input.SlotID),
	}
}

func artifactProducerRuntime(
	projection *Work,
	definition *WorkDefinitionRevision,
	runID, slotID string,
) (*NodeDef, *V2TaskRuntime, error) {
	if projection == nil || definition == nil || runID == "" {
		return nil, nil, errors.New("work: RetryArtifactSlot: active definition run is required")
	}
	var producer *NodeDef
	for i := range definition.Nodes {
		for _, produced := range definition.Nodes[i].ProducesSlotIDs {
			if produced != slotID {
				continue
			}
			if producer != nil {
				return nil, nil, fmt.Errorf("work: RetryArtifactSlot: slot %q has multiple producers", slotID)
			}
			producer = &definition.Nodes[i]
		}
	}
	if producer == nil {
		return nil, nil, fmt.Errorf("work: RetryArtifactSlot: slot %q has no producer", slotID)
	}
	for _, runtime := range projection.V2TaskRuntimes {
		if runtime != nil && runtime.NodeID == producer.ID && runtime.RunID == runID &&
			runtime.DefinitionRev == definition.Revision {
			switch runtime.State {
			case TaskFailedRetryable, TaskInvalidated, TaskCompleted:
				return producer, runtime, nil
			default:
				return nil, nil, fmt.Errorf(
					"work: RetryArtifactSlot: producer %q cannot be retried from %s",
					producer.ID, runtime.State,
				)
			}
		}
	}
	return nil, nil, fmt.Errorf("work: RetryArtifactSlot: active producer runtime %q not found", producer.ID)
}

// ── PreviewArtifact ────────────────────────────────────────────────────────

// PreviewArtifact produces a graded ArtifactPreview for the given artifact
// reference, matched by full identity: definitionRevision + slotId + slotRevision + artifactId.
// This is read-only — no Work state is mutated.
func (s *Service) PreviewArtifact(
	ctx context.Context,
	input PreviewArtifactRequest,
) (*PreviewArtifactResult, error) {
	result := &PreviewArtifactResult{}
	if err := checkServiceContext(ctx); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if err := s.requireStore(); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.SlotID = strings.TrimSpace(input.SlotID)
	input.ArtifactRefID = strings.TrimSpace(input.ArtifactRefID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.ArtifactRefID == "" || input.SlotID == "" ||
		input.DefinitionRevision <= 0 || input.SlotRevision <= 0 {
		err := errors.New("work: PreviewArtifact: workId, positive definitionRevision, slotId, positive slotRevision, and artifactId are required")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}

	proj, err := s.store.LoadProjection(input.WorkID)
	if err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}

	// Exact match by full identity.
	ref, found := findArtifactRefExact(proj, input.DefinitionRevision, input.SlotID, input.SlotRevision, input.ArtifactRefID)
	if !found {
		err := fmt.Errorf("work: PreviewArtifact: artifact %q not found at defRev=%d slot=%s slotRev=%d", input.ArtifactRefID, input.DefinitionRevision, input.SlotID, input.SlotRevision)
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}

	if s.previewSvc == nil {
		s.previewSvc = NewPreviewService(s.store, "")
	}
	preview, err := s.previewSvc.Preview(ctx, input, *ref)
	if err != nil {
		result.Preview = preview
		result.TransportError = TransportErrorFrom(err)
		result.Recoverable = true
		return result, err
	}

	result.Preview = preview
	result.Committed = preview.Error == ""
	result.Recoverable = preview.Grade != PreviewFallback
	return result, nil
}

// RequestArtifactConversion executes an async conversion with external-approval gating.
func (s *Service) RequestArtifactConversion(
	ctx context.Context,
	input RequestArtifactConversionInput,
) (*RequestArtifactConversionResult, error) {
	result := &RequestArtifactConversionResult{}
	if err := checkServiceContext(ctx); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if err := s.requireStore(); err != nil {
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	input.WorkID = strings.TrimSpace(input.WorkID)
	input.SlotID = strings.TrimSpace(input.SlotID)
	input.ArtifactRefID = strings.TrimSpace(input.ArtifactRefID)
	input.RequestID = strings.TrimSpace(input.RequestID)
	if input.WorkID == "" || input.ArtifactRefID == "" || input.RequestID == "" ||
		input.SlotID == "" || input.DefinitionRevision <= 0 || input.SlotRevision <= 0 {
		err := errors.New("work: RequestArtifactConversion: workId, positive definitionRevision, slotId, positive slotRevision, artifactId, and requestId are required")
		result.TransportError = TransportErrorFrom(err)
		return result, err
	}
	if s.previewSvc == nil {
		s.previewSvc = NewPreviewService(s.store, "")
	}
	return s.previewSvc.RequestConversion(ctx, input)
}

// findArtifactRefExact matches by full identity: definitionRevision + slotId + slotRevision + artifactId.
func findArtifactRefExact(w *Work, definitionRevision int64, slotID string, slotRevision int64, refID string) (*ArtifactRef, bool) {
	if w == nil || definitionRevision <= 0 || strings.TrimSpace(slotID) == "" ||
		slotRevision <= 0 || strings.TrimSpace(refID) == "" {
		return nil, false
	}
	for i := range w.V2ArtifactSlots {
		slot := &w.V2ArtifactSlots[i]
		if slot.DefinitionRev != definitionRevision ||
			slot.ID != slotID ||
			slot.Revision != slotRevision {
			continue
		}
		for j := range slot.ArtifactRefs {
			if slot.ArtifactRefs[j].ID == refID {
				return &slot.ArtifactRefs[j], true
			}
		}
	}
	return nil, false
}
