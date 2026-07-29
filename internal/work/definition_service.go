package work

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ── V2 event helpers ───────────────────────────────────────────────────────

func newServiceEventV2(workID, requestID string, eventType WorkEventType, payload []byte, createdAt time.Time) WorkEvent {
	event := newServiceEvent(workID, requestID, eventType, payload, createdAt)
	event.SchemaVersion = WorkEventSchemaVersionV2
	return event
}

// ── ApplyDefinitionResult ─────────────────────────────────────────────────

type ApplyDefinitionResult struct {
	View           *WorkView             `json:"view,omitempty"`
	Intent         *AutoSwitchFaceIntent `json:"intent,omitempty"`
	Impact         *RunImpact            `json:"impact,omitempty"`
	Revision       int64                 `json:"revision"`
	Duplicate      bool                  `json:"duplicate"`
	Committed      bool                  `json:"committed"`
	Recoverable    bool                  `json:"recoverable"`
	TransportError *WorkTransportError   `json:"transportError,omitempty"`
}

// BeginWorkPlanningResult is the typed Wails result for the idempotent
// planning write. The existing Service method remains for internal callers;
// Controller/Wails use this result so a replay is observable.
type BeginWorkPlanningResult struct {
	Result         *WorkView           `json:"result,omitempty"`
	Revision       int64               `json:"revision"`
	Duplicate      bool                `json:"duplicate"`
	Committed      bool                `json:"committed"`
	Recoverable    bool                `json:"recoverable"`
	TransportError *WorkTransportError `json:"transportError,omitempty"`
}

// ── BeginWorkPlanning ──────────────────────────────────────────────────────

func (s *Service) BeginWorkPlanning(ctx context.Context, input BeginWorkPlanningInput) (*WorkView, error) {
	return s.beginWorkPlanning(ctx, input, BlueprintRef{
		ID: "blueprint:collaboration-v2", SchemaVersion: SchemaVersionV2, Version: 1,
	})
}

// beginWorkPlanning is the single creation path for V2 planning. Blueprint
// selection is part of the durable creation intent so the projection never
// reports a generic Blueprint for a scenario-specific Work.
func (s *Service) beginWorkPlanning(ctx context.Context, input BeginWorkPlanningInput, blueprint BlueprintRef) (*WorkView, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("BeginWorkPlanning", input.RequestID)
	if err != nil {
		return nil, err
	}
	sessionID := strings.TrimSpace(input.SessionID)
	if sessionID == "" {
		return nil, errors.New("work: BeginWorkPlanning: sessionId is required")
	}
	normalizedLocale, err := NormalizeLocale(input.Locale)
	if err != nil {
		return nil, fmt.Errorf("work: BeginWorkPlanning: %w", err)
	}
	localeProvided := strings.TrimSpace(input.Locale) != ""
	blueprint.ID = strings.TrimSpace(blueprint.ID)
	if blueprint.ID == "" {
		return nil, errors.New("work: BeginWorkPlanning: blueprintRef.id is required")
	}

	now := time.Now().UTC()
	workID := workIDForRequest(requestID)
	intentBytes, _ := json.Marshal(struct {
		SessionID    string       `json:"sessionId"`
		Locale       string       `json:"locale"`
		BlueprintRef BlueprintRef `json:"blueprintRef"`
	}{SessionID: sessionID, BlueprintRef: blueprint, Locale: normalizedLocale})
	intentSum := sha256.Sum256(intentBytes)
	intentDigest := fmt.Sprintf("begin-%x", intentSum[:])
	legacyIntentBytes, _ := json.Marshal(struct {
		SessionID    string       `json:"sessionId"`
		BlueprintRef BlueprintRef `json:"blueprintRef"`
	}{SessionID: sessionID, BlueprintRef: blueprint})
	legacyIntentSum := sha256.Sum256(legacyIntentBytes)
	legacyIntentDigest := fmt.Sprintf("begin-%x", legacyIntentSum[:])

	// Idempotency: if Work exists, verify intent then return.
	existing, _, stateErr := s.store.LoadState(workID, "")
	if stateErr == nil && existing != nil {
		receipt, receiptErr := s.loadV2Receipt(workID, requestID)
		if receiptErr != nil {
			return nil, committedRecovery("planning-receipt", workID, requestID, 0, receiptErr)
		}
		if receipt == nil {
			return nil, committedRecovery("planning-receipt", workID, requestID, 0,
				errors.New("authoritative planning receipt is unavailable"))
		}
		intentMatches := receipt.IntentDigest == intentDigest
		if !intentMatches && !localeProvided {
			intentMatches = receipt.IntentDigest == legacyIntentDigest
		}
		if !intentMatches {
			return nil, &ErrWorkEventConflict{
				WorkID: workID, RequestID: requestID, Kind: WorkEventRequestConflict,
				Reason: fmt.Sprintf("BeginWorkPlanning request %q reused with different creation intent", requestID),
			}
		}
		view, loadErr := s.loadView(workID)
		if loadErr != nil {
			return nil, loadErr
		}
		return view, nil
	}

	rev := &WorkDefinitionRevision{
		WorkID:         workID,
		Revision:       1,
		ParentRevision: 0,
		Status:         DefDraft,
		Goal:           "",
		Nodes:          []NodeDef{},
		ArtifactSlots:  []ArtifactSlotDef{},
		InputSpecs:     []InputSpec{},
		CreatedBy:      "planning",
		CreatedAt:      now,
	}
	digest, err := ComputeV2RevisionDigest(rev)
	if err != nil {
		return nil, fmt.Errorf("work: BeginWorkPlanning: compute digest: %w", err)
	}
	rev.Digest = digest

	value := &Work{
		SchemaVersion:    SchemaVersionV2,
		ID:               workID,
		Name:             defaultWorkName(normalizedLocale),
		Locale:           normalizedLocale,
		State:            WorkDraft,
		ArchiveState:     ArchiveActive,
		BlueprintRef:     blueprint,
		Definition:       WorkDefinitionSnapshot{SchemaVersion: SchemaVersion, Revision: 1, Workflow: WorkflowDef{}},
		Blocks:           []BlockInstance{},
		V2LatestRevision: 1,
		V2RevisionStates: map[int64]DefinitionStatus{1: DefDraft},
		CreatedWith: RuntimeFingerprint{
			WorkSchemaVersion:  SchemaVersionV2,
			EventSchemaVersion: WorkEventSchemaVersionV2,
		},
		CreatedAt: now,
		UpdatedAt: now,
	}

	createdPayload, _ := json.Marshal(value)
	createdEvent := newServiceEvent(workID, requestID+"/created", EventWorkCreated, createdPayload, now)

	defPayload, _ := json.Marshal(value.Definition)
	defEvent := newServiceEvent(workID, requestID+"/definition", EventDefinitionFrozen, defPayload, now)

	planPayload, _ := json.Marshal(DefPlanningStartedPayload{
		WorkID: workID, SessionID: sessionID, BlueprintRef: blueprint,
	})
	planEvent := newServiceEventV2(workID, requestID+"/planning-started", EventDefPlanningStarted, planPayload, now)
	planEvent.Object = ObjectContext{
		Kind: ObjectDefinition, ID: workID, WorkID: workID,
		DefinitionID: workID, DefinitionRevision: int64Ptr(1),
	}

	revPayload, _ := json.Marshal(DefRevisionCreatedPayload{WorkID: workID, Revision: 1, ParentRevision: 0, Digest: digest})
	revEvent := newServiceEventV2(workID, requestID+"/revision-created", EventDefRevisionCreated, revPayload, now)
	revEvent.Object = ObjectContext{
		Kind: ObjectDefinition, ID: workID, WorkID: workID,
		DefinitionID: workID, DefinitionRevision: int64Ptr(1),
	}

	// Create work dir with V2 revision body staged atomically in the transaction.
	events := []WorkEvent{createdEvent, defEvent, planEvent, revEvent}
	if err := s.store.CreateWorkDir(CreateWorkDirInput{
		RequestID:      requestID,
		Work:           value,
		Definition:     &value.Definition,
		Events:         events,
		V2RevisionBody: rev,
	}); err != nil {
		return nil, fmt.Errorf("work: BeginWorkPlanning: %w", err)
	}

	view, err := s.loadView(workID)
	if err != nil {
		return nil, fmt.Errorf("work: BeginWorkPlanning: reload: %w", err)
	}
	if err := s.syncSessionRefs(view.Work, requestID+"/session-refs"); err != nil {
		return nil, committedRecovery("plan-session-refs", workID, requestID, view.Revision, err)
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("plan-view", workID, requestID, view.Revision, err)
	}
	return view, nil
}

// BeginWorkPlanningWithResult exposes idempotency and post-commit recovery
// without changing the long-standing internal BeginWorkPlanning API.
func (s *Service) BeginWorkPlanningWithResult(ctx context.Context, input BeginWorkPlanningInput) (*BeginWorkPlanningResult, error) {
	result := &BeginWorkPlanningResult{}
	workID := workIDForRequest(strings.TrimSpace(input.RequestID))
	if s != nil && s.store != nil && strings.TrimSpace(input.RequestID) != "" {
		if _, _, err := s.store.LoadState(workID, ""); err == nil {
			result.Duplicate = true
		}
	}
	view, err := s.BeginWorkPlanning(ctx, input)
	result.Result = view
	if view != nil {
		result.Revision = view.Revision
		result.Committed = true
	} else if s != nil && s.store != nil && workID != "" {
		if _, state, loadErr := s.store.LoadState(workID, ""); loadErr == nil {
			result.Revision = state.Revision
			result.Committed = true
		}
	}
	result.TransportError = TransportErrorFrom(err)
	if result.TransportError != nil {
		result.Committed = result.Committed || result.TransportError.Committed
		result.Recoverable = result.TransportError.Recoverable
	}
	return result, err
}

// ── CreateCandidateRevision ────────────────────────────────────────────────

func (s *Service) CreateCandidateRevision(ctx context.Context, workID string, candidate *WorkDefinitionRevision, requestID string, expectedRevision int64) (*WorkDefinitionRevision, error) {
	intentDigest := ""
	if candidate != nil {
		intentDigest = "candidate-" + hashCandidateIntentForWork(strings.TrimSpace(workID), candidate)
	}
	return s.createCandidateRevision(ctx, workID, candidate, requestID, expectedRevision, intentDigest, "")
}

func (s *Service) createCandidateRevision(
	ctx context.Context,
	workID string,
	candidate *WorkDefinitionRevision,
	requestID string,
	expectedRevision int64,
	intentDigest string,
	suggestedName string,
) (*WorkDefinitionRevision, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("CreateCandidateRevision", requestID)
	if err != nil {
		return nil, err
	}
	workID = strings.TrimSpace(workID)
	if workID == "" || candidate == nil {
		return nil, errors.New("work: CreateCandidateRevision: workID and candidate are required")
	}

	// Check receipt first — replay returns the exact result.
	receipt, receiptErr := s.loadV2Receipt(workID, requestID)
	if intentDigest == "" {
		intentDigest = "candidate-" + hashCandidateIntentForWork(workID, candidate)
	}
	if receiptErr != nil && !errors.Is(receiptErr, ErrWorkNotFound) {
		return nil, fmt.Errorf("work: CreateCandidateRevision: load receipt: %w", receiptErr)
	}
	if receiptErr == nil && receipt != nil {
		if receipt.Operation != "CreateCandidateRevision" {
			return nil, &ErrWorkEventConflict{
				WorkID: workID, RequestID: requestID, Kind: WorkEventRequestConflict,
				Reason: fmt.Sprintf("request %q already used for %s", requestID, receipt.Operation),
			}
		}
		// Compare intent digests directly.
		if receipt.IntentDigest != intentDigest {
			return nil, &ErrWorkEventConflict{
				WorkID: workID, RequestID: requestID, Kind: WorkEventRequestConflict,
				Reason: fmt.Sprintf("request %q reused with different intent", requestID),
			}
		}
		return s.definitionStore().LoadRevision(workID, receipt.ResultRevision)
	}

	eventRequestID := requestID + "/candidate"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.SchemaVersion < SchemaVersionV2 {
		return nil, fmt.Errorf("work: CreateCandidateRevision: Work %s is schema V1", workID)
	}

	// Check event-level idempotency (backward compat with existing events).
	if state.RequestFound {
		if state.RequestType != EventDefRevisionCreated {
			return nil, fmt.Errorf("work: CreateCandidateRevision: request %q already used by %s", requestID, state.RequestType)
		}
		return nil, committedRecovery("candidate-receipt", workID, requestID, state.RequestRevision,
			errors.New("committed candidate event has no recoverable receipt"))
	}

	if expectedRevision != state.Revision {
		return nil, revisionConflict(workID, expectedRevision, state.Revision)
	}

	// Derive latest from authoritative revision_created events. Immutable
	// sidecars can contain an orphan left by a body-first failed commit and
	// must never advance the parent pointer.
	latestRevision := current.V2LatestRevision
	if latestRevision <= 0 {
		return nil, fmt.Errorf("%w: Work %s has no committed definition revision", ErrWorkNeedsRepair, workID)
	}
	currentRev, err := s.definitionStore().LoadRevision(workID, latestRevision)
	if err != nil {
		return nil, fmt.Errorf("work: CreateCandidateRevision: load committed revision %d: %w", latestRevision, err)
	}

	newRev := CopyOnWriteRevision(currentRev)
	// Draft history may advance ahead of the active pointer. Every candidate
	// still branches from the currently active revision so ApplyDefinition
	// cannot roll the aggregate backward through an older draft chain.
	newRev.ParentRevision = current.V2CurrentRevision
	newRev.WorkID = workID
	newRev.Nodes = append([]NodeDef(nil), candidate.Nodes...)
	newRev.ArtifactSlots = append([]ArtifactSlotDef(nil), candidate.ArtifactSlots...)
	newRev.InputSpecs = append([]InputSpec(nil), candidate.InputSpecs...)
	newRev.Goal = candidate.Goal
	newRev.CreatedBy = candidate.CreatedBy
	if newRev.CreatedBy == "" {
		newRev.CreatedBy = "planning"
	}
	newRev.CreatedAt = time.Now().UTC()

	// A retry after a body-first failure reuses the exact immutable orphan when
	// its parent and semantic intent match.
	orphan, orphanErr := s.definitionStore().LoadRevision(workID, newRev.Revision)
	switch {
	case orphanErr == nil:
		if orphan.ParentRevision != current.V2CurrentRevision ||
			"candidate-"+hashCandidateIntentForWork(workID, orphan) != intentDigest {
			return nil, &ErrWorkEventConflict{
				WorkID: workID, RequestID: requestID, Kind: WorkEventRequestConflict,
				Reason: fmt.Sprintf("uncommitted definition revision %d conflicts with request %q", orphan.Revision, requestID),
			}
		}
		newRev = orphan
	case !errors.Is(orphanErr, ErrWorkNotFound):
		return nil, fmt.Errorf("work: CreateCandidateRevision: inspect retry revision %d: %w", newRev.Revision, orphanErr)
	}

	if err := ValidateDefinitionRevision(newRev); err != nil {
		return nil, fmt.Errorf("work: CreateCandidateRevision: invalid: %w", err)
	}
	digest, err := ComputeV2RevisionDigest(newRev)
	if err != nil {
		return nil, fmt.Errorf("work: CreateCandidateRevision: compute digest: %w", err)
	}
	newRev.Digest = digest

	// Build the event before entering the aggregate commit seam.
	eventReceipt := &V2IntentReceipt{
		RequestID: requestID, Operation: "CreateCandidateRevision", IntentDigest: intentDigest,
		ResultRevision: newRev.Revision, ResultDigest: digest, CreatedAt: time.Now().UTC(),
	}
	revPayload, _ := json.Marshal(DefRevisionCreatedPayload{
		WorkID: workID, Revision: newRev.Revision, ParentRevision: newRev.ParentRevision, Digest: digest,
		Receipt: eventReceipt, SuggestedName: strings.TrimSpace(suggestedName),
	})
	event := newServiceEventV2(workID, eventRequestID, EventDefRevisionCreated, revPayload, time.Now().UTC())
	event.Object = ObjectContext{
		Kind: ObjectDefinition, ID: workID, WorkID: workID,
		DefinitionID: workID, DefinitionRevision: int64Ptr(newRev.Revision),
	}
	event.BaseRevision = state.Revision
	event.Revision = state.Revision + 1

	type definitionRevisionCommitter interface {
		CommitDefinitionRevision(string, *WorkDefinitionRevision, WorkEvent) (int64, error)
	}
	if committer, ok := s.store.(definitionRevisionCommitter); ok {
		if _, err := committer.CommitDefinitionRevision(workID, newRev, event); err != nil {
			return nil, fmt.Errorf("work: CreateCandidateRevision: commit: %w", err)
		}
	} else {
		// Compatibility for non-file test stores: retain body-before-event.
		if err := s.definitionStore().StoreRevision(workID, newRev); err != nil {
			return nil, fmt.Errorf("work: CreateCandidateRevision: store body: %w", err)
		}
		if _, err := s.store.CommitEvent(workID, event); err != nil {
			return nil, err
		}
	}

	return newRev, nil
}

// ── ApplyDefinition ────────────────────────────────────────────────────────

func (s *Service) ApplyDefinition(ctx context.Context, input ApplyDefinitionInput) (*ApplyDefinitionResult, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("ApplyDefinition", input.RequestID)
	if err != nil {
		return nil, err
	}
	workID := strings.TrimSpace(input.WorkID)
	if workID == "" {
		return nil, errors.New("work: ApplyDefinition: workID is required")
	}

	// Check receipt first — replay returns the exact result.
	intentDigest := "apply-" + hashApplyIntent(input)
	receipt, receiptErr := s.loadV2Receipt(workID, requestID)
	if receiptErr != nil && !errors.Is(receiptErr, ErrWorkNotFound) {
		return nil, fmt.Errorf("work: ApplyDefinition: load receipt: %w", receiptErr)
	}
	if receiptErr == nil && receipt != nil {
		if receipt.Operation != "ApplyDefinition" {
			return nil, &ErrWorkEventConflict{
				WorkID: workID, RequestID: requestID, Kind: WorkEventRequestConflict,
				Reason: fmt.Sprintf("request %q already used for %s", requestID, receipt.Operation),
			}
		}
		if receipt.IntentDigest != intentDigest {
			return nil, &ErrWorkEventConflict{
				WorkID: workID, RequestID: requestID, Kind: WorkEventRequestConflict,
				Reason: fmt.Sprintf("request %q reused with different intent", requestID),
			}
		}
		result := &ApplyDefinitionResult{
			Revision:  receipt.ResultRevision,
			Duplicate: true,
			Committed: true,
			Intent: &AutoSwitchFaceIntent{
				WorkID: workID, RunID: receipt.ResultRunID,
				DefinitionRev: receipt.ResultRevision, Reason: "definition_applied",
			},
			Impact: impactFromJSON(receipt.Impact),
		}
		view, loadErr := s.loadView(workID)
		if loadErr != nil {
			recovery := committedRecovery("apply-view", workID, requestID, result.Revision, loadErr)
			result.Recoverable = true
			result.TransportError = TransportErrorFrom(recovery)
			return result, recovery
		}
		result.View = view
		result.Revision = view.Revision
		if s.v2 != nil && s.v2.enabled() {
			definition, defErr := s.definitionStore().LoadRevision(workID, receipt.ResultRevision)
			if defErr != nil {
				recovery := committedRecovery("v2-definition-wake", workID, requestID, view.Revision, defErr)
				result.TransportError = TransportErrorFrom(recovery)
				result.Recoverable = result.TransportError.Recoverable
				return result, recovery
			}
			if wakeErr := s.v2.ContinueDefinition(
				ctx,
				workID,
				receipt.ResultRunID,
				requestID,
				definition,
				runImpactAffectedTasks(result.Impact),
			); wakeErr != nil {
				recovery := committedRecovery("v2-definition-wake", workID, requestID, view.Revision, wakeErr)
				result.TransportError = TransportErrorFrom(recovery)
				result.Recoverable = result.TransportError.Recoverable
				return result, recovery
			}
		}
		return result, nil
	}

	eventRequestID := requestID + "/apply"
	current, state, err := s.store.LoadState(workID, eventRequestID)
	if err != nil {
		return nil, err
	}
	if current.SchemaVersion < SchemaVersionV2 {
		return nil, fmt.Errorf("work: ApplyDefinition: Work %s is schema V1", workID)
	}

	// Check event-level idempotency.
	if state.RequestFound {
		if state.RequestType != EventDefRevisionApplied {
			return nil, fmt.Errorf("work: ApplyDefinition: request %q already used by %s", requestID, state.RequestType)
		}
		return nil, committedRecovery("apply-receipt", workID, requestID, state.RequestRevision,
			errors.New("committed apply event has no recoverable receipt"))
	}

	// New request — expectedRevision check.
	if input.ExpectedRevision != state.Revision {
		return nil, revisionConflict(workID, input.ExpectedRevision, state.Revision)
	}

	candidateRev, err := s.definitionStore().LoadRevision(workID, input.Revision)
	if err != nil {
		return nil, fmt.Errorf("work: ApplyDefinition: load revision %d: %w", input.Revision, err)
	}
	if err := validateDefinitionActivation(current, candidateRev); err != nil {
		return nil, fmt.Errorf("work: ApplyDefinition: %w", err)
	}

	computedDigest, err := ComputeV2RevisionDigest(candidateRev)
	if err != nil {
		return nil, fmt.Errorf("work: ApplyDefinition: compute digest: %w", err)
	}
	if computedDigest != candidateRev.Digest {
		return nil, fmt.Errorf("work: ApplyDefinition: revision %d digest mismatch", input.Revision)
	}

	if err := ValidateDefinitionRevision(candidateRev); err != nil {
		return nil, fmt.Errorf("work: ApplyDefinition: invalid: %w", err)
	}

	var prevRev *WorkDefinitionRevision
	if candidateRev.ParentRevision > 0 {
		prevRev, err = s.definitionStore().LoadRevision(workID, candidateRev.ParentRevision)
		if err != nil {
			return nil, fmt.Errorf("work: ApplyDefinition: load active parent revision %d: %w", candidateRev.ParentRevision, err)
		}
	}
	impactBase := prevRev
	if impactBase == nil {
		impactBase = &WorkDefinitionRevision{}
	}
	runImpact := ClassifyRunImpact(impactBase, candidateRev)

	// Create Run + revision_applied in a single batch commit. The run event
	// carries the idempotency/impact receipt, making it part of the same
	// authoritative log transaction instead of a later sidecar write.
	runID := workflowRunID(workID, requestID)
	appliedAt := time.Now().UTC()
	run := WorkflowRun{
		ID: runID, WorkID: workID, RequestID: requestID,
		DefinitionDigest: candidateRev.Digest, State: RunPending,
		Stages: buildV2Stages(candidateRev, appliedAt), StartedAt: appliedAt,
	}
	applyReceipt := &V2IntentReceipt{
		RequestID: requestID, Operation: "ApplyDefinition", IntentDigest: intentDigest,
		ResultRevision: input.Revision, ResultDigest: candidateRev.Digest, ResultRunID: runID,
		Impact: impactToJSON(runImpact), CreatedAt: appliedAt,
	}
	runPayload, _ := json.Marshal(runEventPayload{Run: run, WorkState: WorkRunning, V2Receipt: applyReceipt})
	runEvent := newServiceEvent(workID, requestID+"/run", EventRunStarted, runPayload, appliedAt)
	runEvent.BaseRevision = state.Revision
	runEvent.Revision = state.Revision + 1

	applyPayload, _ := json.Marshal(DefRevisionAppliedPayload{
		WorkID:           workID,
		Revision:         input.Revision,
		PreviousRevision: candidateRev.ParentRevision,
		ExpectedRevision: input.ExpectedRevision,
		InvalidatedTasks: runImpactInvalidatedTasks(runImpact),
	})
	applyEvent := newServiceEventV2(workID, eventRequestID, EventDefRevisionApplied, applyPayload, appliedAt)
	applyEvent.Object = ObjectContext{
		Kind: ObjectDefinition, ID: workID, WorkID: workID, DefinitionID: workID,
		ExpectedRevision: int64Ptr(input.ExpectedRevision), DefinitionRevision: int64Ptr(input.Revision),
	}
	applyEvent.BaseRevision = runEvent.Revision
	applyEvent.Revision = runEvent.Revision + 1

	reuseEvents, err := buildKeptContextEvents(
		current,
		prevRev,
		candidateRev,
		runID,
		runImpact,
		appliedAt,
	)
	if err != nil {
		return nil, fmt.Errorf("work: ApplyDefinition: project kept contexts: %w", err)
	}

	// Build artifact slot declaration events from the definition.
	declareSlotEvents := BuildDeclareEvents(workID, input.Revision, requestID+"/slots",
		candidateRev.ArtifactSlots, appliedAt)
	// Project reusable runtimes and slots in the same atomic batch.
	allEvents := make([]WorkEvent, 0, 2+len(reuseEvents)+len(declareSlotEvents))
	allEvents = append(allEvents, runEvent, applyEvent)
	prevEventRev := applyEvent.Revision
	for i := range reuseEvents {
		reuseEvents[i].BaseRevision = prevEventRev
		reuseEvents[i].Revision = prevEventRev + 1
		prevEventRev = reuseEvents[i].Revision
		allEvents = append(allEvents, reuseEvents[i])
	}
	for i := range declareSlotEvents {
		declareSlotEvents[i].BaseRevision = prevEventRev
		declareSlotEvents[i].Revision = prevEventRev + 1
		prevEventRev = declareSlotEvents[i].Revision
		allEvents = append(allEvents, declareSlotEvents[i])
	}

	if _, err := s.store.CommitEvents(workID, allEvents); err != nil {
		return nil, fmt.Errorf("work: ApplyDefinition: batch commit: %w", err)
	}
	if s.v2 != nil && s.v2.enabled() {
		if wakeErr := s.v2.ContinueDefinition(
			ctx,
			workID,
			runID,
			requestID,
			candidateRev,
			runImpactAffectedTasks(runImpact),
		); wakeErr != nil {
			result := &ApplyDefinitionResult{
				Intent: &AutoSwitchFaceIntent{
					WorkID: workID, RunID: runID,
					DefinitionRev: input.Revision, Reason: "definition_applied",
				},
				Impact: runImpact, Revision: prevEventRev, Committed: true, Recoverable: true,
			}
			recovery := committedRecovery("v2-definition-wake", workID, requestID, prevEventRev, wakeErr)
			result.TransportError = TransportErrorFrom(recovery)
			return result, recovery
		}
	}

	view, err := s.loadView(workID)
	if err != nil {
		recovery := committedRecovery("apply-view", workID, requestID, prevEventRev, err)
		return &ApplyDefinitionResult{
			Revision: prevEventRev, Committed: true, Recoverable: true,
			TransportError: TransportErrorFrom(recovery),
		}, recovery
	}

	var emitErr error
	if s.v2Transport.Load() {
		emitErr = s.emitV2MutationSnapshot(view, state.Revision, requestID)
	} else {
		emitErr = s.emitSnapshot(view, requestID)
	}
	if emitErr != nil {
		recovery := committedRecovery("apply-view", workID, requestID, view.Revision, emitErr)
		return &ApplyDefinitionResult{
			View: view, Revision: view.Revision, Committed: true, Recoverable: true,
			TransportError: TransportErrorFrom(recovery),
		}, recovery
	}

	return &ApplyDefinitionResult{
		View:      view,
		Revision:  view.Revision,
		Committed: true,
		Intent: &AutoSwitchFaceIntent{
			WorkID: workID, RunID: runID, DefinitionRev: input.Revision, Reason: "definition_applied",
		},
		Impact: runImpact,
	}, nil
}

// ── Helpers ────────────────────────────────────────────────────────────────

func sha256Hash(data []byte) []byte {
	h := sha256.Sum256(data)
	return h[:]
}

// hashCandidateIntent produces a stable digest of the semantic content of a
// candidate revision, excluding fields that are derived during CoW
// (Revision, ParentRevision, Digest, Status, CreatedAt).
func hashCandidateIntentForWork(workID string, c *WorkDefinitionRevision) string {
	createdBy := c.CreatedBy
	if createdBy == "" {
		createdBy = "planning"
	}
	v := struct {
		WorkID        string
		Goal          string
		Nodes         []NodeDef
		ArtifactSlots []ArtifactSlotDef
		InputSpecs    []InputSpec
		CreatedBy     string
	}{workID, c.Goal, c.Nodes, c.ArtifactSlots, c.InputSpecs, createdBy}
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256Hash(raw))
}

// hashApplyIntent produces a stable digest of an ApplyDefinitionInput.
func hashApplyIntent(in ApplyDefinitionInput) string {
	v := struct {
		WorkID   string
		Revision int64
	}{strings.TrimSpace(in.WorkID), in.Revision}
	raw, _ := json.Marshal(v)
	return fmt.Sprintf("%x", sha256Hash(raw))
}

func (s *Service) loadV2Receipt(workID, requestID string) (*V2IntentReceipt, error) {
	type receiptLoader interface {
		LoadV2Receipt(string, string) (*V2IntentReceipt, error)
	}
	if rl, ok := s.store.(receiptLoader); ok {
		return rl.LoadV2Receipt(workID, requestID)
	}
	return nil, nil
}

func buildV2Stages(rev *WorkDefinitionRevision, now time.Time) []Stage {
	if rev == nil {
		return nil
	}
	stages := make([]Stage, 0, len(rev.Nodes))
	for _, n := range rev.Nodes {
		stages = append(stages, Stage{
			ID: n.ID, Name: n.Title, State: RunPending,
			Tasks:     []Task{{ID: n.ID, Name: n.Title, State: RunPending}},
			StartedAt: now,
		})
	}
	return stages
}

func runImpactInvalidatedTasks(ri *RunImpact) []string {
	if ri == nil {
		return nil
	}
	return append([]string(nil), ri.InvalidatedNodeIDs...)
}

func runImpactAffectedTasks(ri *RunImpact) []string {
	if ri == nil {
		return nil
	}
	seen := make(map[string]bool, len(ri.InvalidatedNodeIDs)+len(ri.NewNodeIDs))
	for _, nodeID := range ri.InvalidatedNodeIDs {
		seen[nodeID] = true
	}
	for _, nodeID := range ri.NewNodeIDs {
		seen[nodeID] = true
	}
	ids := make([]string, 0, len(seen))
	for nodeID := range seen {
		ids = append(ids, nodeID)
	}
	sort.Strings(ids)
	return ids
}

func validateDefinitionActivation(current *Work, candidate *WorkDefinitionRevision) error {
	if current == nil || candidate == nil {
		return errors.New("definition activation requires current work and candidate")
	}
	if candidate.ParentRevision != current.V2CurrentRevision {
		return fmt.Errorf(
			"candidate parent revision %d is not current active revision %d",
			candidate.ParentRevision,
			current.V2CurrentRevision,
		)
	}
	if candidate.Revision <= current.V2CurrentRevision {
		return fmt.Errorf(
			"candidate revision %d must advance current active revision %d",
			candidate.Revision,
			current.V2CurrentRevision,
		)
	}
	active := int64(0)
	activeCount := 0
	for revision, status := range current.V2RevisionStates {
		if status == DefActive {
			active = revision
			activeCount++
		}
	}
	if current.V2CurrentRevision == 0 {
		if activeCount != 0 {
			return fmt.Errorf("definition state has %d active revisions before initial activation", activeCount)
		}
	} else if activeCount != 1 || active != current.V2CurrentRevision {
		return fmt.Errorf(
			"definition active state is inconsistent: pointer=%d active=%d count=%d",
			current.V2CurrentRevision,
			active,
			activeCount,
		)
	}
	if status, ok := current.V2RevisionStates[candidate.Revision]; !ok || status != DefDraft {
		return fmt.Errorf("candidate revision %d is not draft", candidate.Revision)
	}
	return nil
}
