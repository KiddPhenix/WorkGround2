package work

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ── V2 persisted event types ───────────────────────────────────────────────
//
// V2 events carry schemaVersion = SchemaVersionV2. A binary that only
// understands V1 must reject V2 events and offer read-only access via
// CheckSchemaVersion. V1 events remain immutable once written.
//
// All V2 write events carry requestID and expectedRevision (or
// definitionRevision) for idempotent retry and conflict detection.

// V2 event type constants extend V1 WorkEventType. They are valid only when
// the WorkEvent.SchemaVersion ≥ SchemaVersionV2.
const (
	// ── Definition lifecycle ────────────────────────────────────────────
	EventDefPlanningStarted WorkEventType = "definition.planning_started"
	EventDefRevisionCreated WorkEventType = "definition.revision_created"
	EventDefRevisionApplied WorkEventType = "definition.revision_applied"

	// ── Artifact slot lifecycle ─────────────────────────────────────────
	EventArtifactSlotDeclared WorkEventType = "artifact_slot.declared"
	EventArtifactSlotUpdated  WorkEventType = "artifact_slot.updated"

	// ── Input lifecycle ─────────────────────────────────────────────────
	EventInputRequested          WorkEventType = "input.requested"
	EventInputDraftSaved         WorkEventType = "input.draft_saved"
	EventInputSubmitted          WorkEventType = "input.submitted"
	EventInputRejected           WorkEventType = "input.rejected"
	EventInputCornerstoneChanged WorkEventType = "input.cornerstone_changed"

	// ── Discussion / patch ──────────────────────────────────────────────
	EventPatchPreviewed WorkEventType = "discussion.patch_previewed"
	EventPatchApplied   WorkEventType = "discussion.patch_applied"

	// ── Task V2 states ─────────────────────────────────────────────────
	EventTaskInvalidated     WorkEventType = "task.invalidated"
	EventTaskReady           WorkEventType = "task.ready"
	EventTaskWaitingInput    WorkEventType = "task.waiting_input"
	EventTaskWaitingApproval WorkEventType = "task.waiting_approval"

	// ── Task runtime lifecycle ─────────────────────────────────────────
	EventTaskRuntimeCreated WorkEventType = "task.runtime_created"
	EventTaskRuntimeUpdated WorkEventType = "task.runtime_updated"
	EventTaskStaleResult    WorkEventType = "task.stale_result"
)

// IsV2EventType reports whether t is a V2-only persisted event type.
func IsV2EventType(t WorkEventType) bool {
	return v2EventTypes[t]
}

var v2EventTypes = map[WorkEventType]bool{
	EventDefPlanningStarted:      true,
	EventDefRevisionCreated:      true,
	EventDefRevisionApplied:      true,
	EventArtifactSlotDeclared:    true,
	EventArtifactSlotUpdated:     true,
	EventInputRequested:          true,
	EventInputDraftSaved:         true,
	EventInputSubmitted:          true,
	EventInputRejected:           true,
	EventInputCornerstoneChanged: true,
	EventPatchPreviewed:          true,
	EventPatchApplied:            true,
	EventTaskInvalidated:         true,
	EventTaskReady:               true,
	EventTaskWaitingInput:        true,
	EventTaskWaitingApproval:     true,
	EventTaskRuntimeCreated:      true,
	EventTaskRuntimeUpdated:      true,
	EventTaskStaleResult:         true,
}

// ── V2 object kinds ────────────────────────────────────────────────────────

const (
	ObjectDefinition   ObjectKind = "definition"
	ObjectArtifactSlot ObjectKind = "artifact_slot"
	ObjectInput        ObjectKind = "input"
	ObjectPatch        ObjectKind = "patch"
)

// ── V2 event validation ────────────────────────────────────────────────────

// ValidateV2WorkEvent checks envelope + delegates payload validation.
func ValidateV2WorkEvent(ev WorkEvent) error {
	if ev.SchemaVersion != SchemaVersionV2 {
		return fmt.Errorf("work: V2 event requires schemaVersion=%d, got %d",
			SchemaVersionV2, ev.SchemaVersion)
	}
	if ev.ID == "" {
		return fmt.Errorf("work: V2 event %q requires non-empty ID", ev.Type)
	}
	if ev.RequestID == "" {
		return fmt.Errorf("work: V2 event %q requires non-empty requestID", ev.Type)
	}
	if ev.WorkID == "" {
		return fmt.Errorf("work: V2 event %q requires non-empty workID", ev.Type)
	}
	if !IsV2EventType(ev.Type) {
		return fmt.Errorf("work: unknown V2 event type %q", ev.Type)
	}
	if ev.Revision < 0 {
		return fmt.Errorf("work: V2 event %q revision must be non-negative", ev.Type)
	}
	if ev.BaseRevision < 0 {
		return fmt.Errorf("work: V2 event %q baseRevision must be non-negative", ev.Type)
	}
	// Validate ObjectContext.
	ctx := ev.Object
	if ctx.WorkID == "" {
		return fmt.Errorf("work: V2 event %q ObjectContext.workID required", ev.Type)
	}
	if ctx.WorkID != ev.WorkID {
		return fmt.Errorf("work: V2 event %q ObjectContext.workID %q != envelope %q", ev.Type, ctx.WorkID, ev.WorkID)
	}
	if err := validateV2ObjectContext(ev.Type, ctx); err != nil {
		return err
	}
	// Cross-check: payload IDs must match ObjectContext.
	if err := validateV2PayloadContextCrossCheck(ev.Type, ev.Payload, ctx); err != nil {
		return err
	}
	// Validate payload.
	if err := ValidateV2WorkEventPayload(ev.Type, ev.Payload); err != nil {
		return err
	}
	return nil
}

// v2ContextRule defines the ObjectContext requirements for one V2 event type.
type v2ContextRule struct {
	Kind                     ObjectKind
	PrimaryID                string // "definitionID", "artifactSlotID", "inputID", "patchID", "taskID"
	RequiresRunID            bool
	RequiresTaskID           bool
	RequiresBlockID          bool
	RequiresInputID          bool
	RequiresSpecID           bool
	RequiresDefinitionID     bool
	RequiresArtifactSlotID   bool
	RequiresPatchID          bool
	RequiresExpectedRevision bool
	RequiresDefinitionRev    bool
}

var v2ContextRules = map[WorkEventType]v2ContextRule{
	EventDefPlanningStarted:      {Kind: ObjectDefinition, PrimaryID: "definitionID", RequiresDefinitionID: true},
	EventDefRevisionCreated:      {Kind: ObjectDefinition, PrimaryID: "definitionID", RequiresDefinitionID: true, RequiresDefinitionRev: true},
	EventDefRevisionApplied:      {Kind: ObjectDefinition, PrimaryID: "definitionID", RequiresDefinitionID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventArtifactSlotDeclared:    {Kind: ObjectArtifactSlot, PrimaryID: "artifactSlotID", RequiresArtifactSlotID: true, RequiresDefinitionRev: true},
	EventArtifactSlotUpdated:     {Kind: ObjectArtifactSlot, PrimaryID: "artifactSlotID", RequiresArtifactSlotID: true, RequiresDefinitionRev: true},
	EventInputRequested:          {Kind: ObjectInput, PrimaryID: "inputID", RequiresRunID: true, RequiresTaskID: true, RequiresBlockID: true, RequiresInputID: true, RequiresSpecID: true, RequiresDefinitionRev: true},
	EventInputDraftSaved:         {Kind: ObjectInput, PrimaryID: "inputID", RequiresRunID: true, RequiresTaskID: true, RequiresBlockID: true, RequiresInputID: true, RequiresSpecID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventInputSubmitted:          {Kind: ObjectInput, PrimaryID: "inputID", RequiresRunID: true, RequiresTaskID: true, RequiresBlockID: true, RequiresInputID: true, RequiresSpecID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventInputRejected:           {Kind: ObjectInput, PrimaryID: "inputID", RequiresRunID: true, RequiresTaskID: true, RequiresBlockID: true, RequiresInputID: true, RequiresSpecID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventInputCornerstoneChanged: {Kind: ObjectInput, PrimaryID: "inputID", RequiresRunID: true, RequiresTaskID: true, RequiresBlockID: true, RequiresInputID: true, RequiresSpecID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventPatchPreviewed:          {Kind: ObjectPatch, PrimaryID: "patchID", RequiresPatchID: true, RequiresDefinitionRev: true},
	EventPatchApplied:            {Kind: ObjectPatch, PrimaryID: "patchID", RequiresPatchID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventTaskInvalidated:         {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true},
	EventTaskReady:               {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true},
	EventTaskWaitingInput:        {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true},
	EventTaskWaitingApproval:     {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true},
	EventTaskRuntimeCreated:      {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventTaskRuntimeUpdated:      {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
	EventTaskStaleResult:         {Kind: ObjectTask, PrimaryID: "taskID", RequiresRunID: true, RequiresTaskID: true, RequiresExpectedRevision: true, RequiresDefinitionRev: true},
}

func validateV2ObjectContext(typ WorkEventType, ctx ObjectContext) error {
	r, ok := v2ContextRules[typ]
	if !ok {
		return nil // unknown type — validated elsewhere
	}
	if ctx.Kind != r.Kind {
		return fmt.Errorf("work: %s ObjectContext.Kind %q != required %q", typ, ctx.Kind, r.Kind)
	}
	if ctx.ID == "" {
		return fmt.Errorf("work: %s ObjectContext.ID required", typ)
	}
	if r.PrimaryID != "" && !ctxIDMatches(r.PrimaryID, ctx) {
		return fmt.Errorf("work: %s ObjectContext.ID %q != required typed ID", typ, ctx.ID)
	}
	if r.RequiresRunID && ctx.RunID == "" {
		return fmt.Errorf("work: %s ObjectContext.runID required", typ)
	}
	if r.RequiresTaskID && ctx.TaskID == "" {
		return fmt.Errorf("work: %s ObjectContext.taskID required", typ)
	}
	if r.RequiresBlockID && ctx.BlockID == "" {
		return fmt.Errorf("work: %s ObjectContext.blockID required", typ)
	}
	if r.RequiresInputID && ctx.InputID == "" {
		return fmt.Errorf("work: %s ObjectContext.inputID required", typ)
	}
	if r.RequiresSpecID && ctx.SpecID == "" {
		return fmt.Errorf("work: %s ObjectContext.specID required", typ)
	}
	if r.RequiresDefinitionID && ctx.DefinitionID == "" {
		return fmt.Errorf("work: %s ObjectContext.definitionID required", typ)
	}
	if r.RequiresArtifactSlotID && ctx.ArtifactSlotID == "" {
		return fmt.Errorf("work: %s ObjectContext.artifactSlotID required", typ)
	}
	if r.RequiresPatchID && ctx.PatchID == "" {
		return fmt.Errorf("work: %s ObjectContext.patchID required", typ)
	}
	if r.RequiresExpectedRevision {
		if ctx.ExpectedRevision == nil {
			return fmt.Errorf("work: %s ObjectContext.expectedRevision required (0 valid)", typ)
		}
		if *ctx.ExpectedRevision < 0 {
			return fmt.Errorf("work: %s ObjectContext.expectedRevision must be >=0", typ)
		}
	}
	if r.RequiresDefinitionRev {
		if ctx.DefinitionRevision == nil {
			return fmt.Errorf("work: %s ObjectContext.definitionRevision required (0 valid)", typ)
		}
		if *ctx.DefinitionRevision < 0 {
			return fmt.Errorf("work: %s ObjectContext.definitionRevision must be >=0", typ)
		}
	}
	return nil
}

func ctxIDMatches(primaryID string, ctx ObjectContext) bool {
	switch primaryID {
	case "definitionID":
		return ctx.ID == ctx.DefinitionID
	case "artifactSlotID":
		return ctx.ID == ctx.ArtifactSlotID
	case "inputID":
		return ctx.ID == ctx.InputID
	case "patchID":
		return ctx.ID == ctx.PatchID
	case "taskID":
		return ctx.ID == ctx.TaskID
	}
	return true
}

// validateV2PayloadContextCrossCheck unmarshals the payload and verifies that
// all IDs present in the payload match the corresponding ObjectContext fields.
func validateV2PayloadContextCrossCheck(typ WorkEventType, payload json.RawMessage, ctx ObjectContext) error {
	// Common: workId always checked.
	var common struct {
		WorkID string `json:"workId"`
	}
	if err := json.Unmarshal(payload, &common); err != nil {
		return fmt.Errorf("work: %s cross-check unmarshal: %w", typ, err)
	}
	if common.WorkID != "" && common.WorkID != ctx.WorkID {
		return fmt.Errorf("work: %s payload.workId %q != ctx.workID %q", typ, common.WorkID, ctx.WorkID)
	}
	switch typ {
	case EventDefPlanningStarted, EventDefRevisionCreated, EventDefRevisionApplied:
		if ctx.DefinitionID != "" && ctx.DefinitionID != ctx.WorkID {
			return fmt.Errorf("work: %s ctx.definitionID %q != payload.workId %q", typ, ctx.DefinitionID, ctx.WorkID)
		}
	case EventArtifactSlotDeclared, EventArtifactSlotUpdated:
		var p struct {
			SlotID string `json:"slotId"`
		}
		if json.Unmarshal(payload, &p) == nil && p.SlotID != "" && p.SlotID != ctx.ArtifactSlotID {
			return fmt.Errorf("work: %s payload.slotId %q != ctx.artifactSlotID %q", typ, p.SlotID, ctx.ArtifactSlotID)
		}
	case EventInputRequested, EventInputDraftSaved, EventInputSubmitted, EventInputRejected, EventInputCornerstoneChanged:
		var p struct {
			InputID string `json:"inputId"`
			RunID   string `json:"runId"`
			TaskID  string `json:"taskId"`
			BlockID string `json:"blockId"`
			SpecID  string `json:"specId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s cross-check: %w", typ, err)
		}
		if p.RunID != "" && p.RunID != ctx.RunID {
			return fmt.Errorf("work: %s payload.runId %q != ctx.runID %q", typ, p.RunID, ctx.RunID)
		}
		if p.TaskID != "" && p.TaskID != ctx.TaskID {
			return fmt.Errorf("work: %s payload.taskId %q != ctx.taskID %q", typ, p.TaskID, ctx.TaskID)
		}
		if p.BlockID != "" && p.BlockID != ctx.BlockID {
			return fmt.Errorf("work: %s payload.blockId %q != ctx.blockID %q", typ, p.BlockID, ctx.BlockID)
		}
		if p.InputID != "" && p.InputID != ctx.InputID {
			return fmt.Errorf("work: %s payload.inputId %q != ctx.inputID %q", typ, p.InputID, ctx.InputID)
		}
		if p.SpecID != "" && p.SpecID != ctx.SpecID {
			return fmt.Errorf("work: %s payload.specId %q != ctx.specID %q", typ, p.SpecID, ctx.SpecID)
		}
	case EventPatchPreviewed, EventPatchApplied:
		var p struct {
			PatchID string `json:"patchId"`
			RunID   string `json:"runId"`
			TaskID  string `json:"taskId"`
			BlockID string `json:"blockId"`
		}
		if json.Unmarshal(payload, &p) == nil && p.PatchID != "" && p.PatchID != ctx.PatchID {
			return fmt.Errorf("work: %s payload.patchId %q != ctx.patchID %q", typ, p.PatchID, ctx.PatchID)
		}
		if p.RunID != "" && p.RunID != ctx.RunID {
			return fmt.Errorf("work: %s payload.runId %q != ctx.runID %q", typ, p.RunID, ctx.RunID)
		}
		if p.TaskID != "" && p.TaskID != ctx.TaskID {
			return fmt.Errorf("work: %s payload.taskId %q != ctx.taskID %q", typ, p.TaskID, ctx.TaskID)
		}
		if p.BlockID != "" && p.BlockID != ctx.BlockID {
			return fmt.Errorf("work: %s payload.blockId %q != ctx.blockID %q", typ, p.BlockID, ctx.BlockID)
		}
	case EventTaskInvalidated, EventTaskReady, EventTaskWaitingInput, EventTaskWaitingApproval:
		var p struct {
			TaskID string `json:"taskId"`
			RunID  string `json:"runId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s cross-check: %w", typ, err)
		}
		if p.RunID != "" && p.RunID != ctx.RunID {
			return fmt.Errorf("work: %s payload.runId %q != ctx.runID %q", typ, p.RunID, ctx.RunID)
		}
		if p.TaskID != "" && p.TaskID != ctx.TaskID {
			return fmt.Errorf("work: %s payload.taskId %q != ctx.taskID %q", typ, p.TaskID, ctx.TaskID)
		}
	case EventTaskRuntimeCreated, EventTaskRuntimeUpdated, EventTaskStaleResult:
		var p struct {
			TaskID           string `json:"taskId"`
			RunID            string `json:"runId"`
			ExpectedRevision int64  `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s cross-check: %w", typ, err)
		}
		if p.RunID != ctx.RunID {
			return fmt.Errorf("work: %s payload.runId %q != ctx.runID %q", typ, p.RunID, ctx.RunID)
		}
		if p.TaskID != ctx.TaskID {
			return fmt.Errorf("work: %s payload.taskId %q != ctx.taskID %q", typ, p.TaskID, ctx.TaskID)
		}
		if ctx.ExpectedRevision == nil || p.ExpectedRevision != *ctx.ExpectedRevision {
			return fmt.Errorf("work: %s payload.expectedRevision %d != ctx.expectedRevision", typ, p.ExpectedRevision)
		}
	}
	return nil
}

// wireInt64 is used to detect present-vs-missing for required numeric fields
// that may legitimately be 0 (e.g. revision, parentRevision).
type wireInt64 struct {
	V  int64
	OK bool
}

func (w *wireInt64) UnmarshalJSON(b []byte) error {
	w.OK = true
	return json.Unmarshal(b, &w.V)
}

func wireNonNeg(w wireInt64, label string) error {
	if !w.OK {
		return fmt.Errorf("work: %s is required", label)
	}
	if w.V < 0 {
		return fmt.Errorf("work: %s must be non-negative, got %d", label, w.V)
	}
	return nil
}

func wirePositive(w wireInt64, label string) error {
	if !w.OK {
		return fmt.Errorf("work: %s is required", label)
	}
	if w.V <= 0 {
		return fmt.Errorf("work: %s must be positive, got %d", label, w.V)
	}
	return nil
}

// ValidateV2WorkEventPayload unmarshals the payload to the typed struct for
// the given event type and validates all required fields.
func ValidateV2WorkEventPayload(typ WorkEventType, payload json.RawMessage) error {
	if !IsV2EventType(typ) {
		return fmt.Errorf("work: unknown V2 event type %q", typ)
	}
	if len(payload) == 0 {
		return fmt.Errorf("work: V2 event %q requires non-empty payload", typ)
	}

	switch typ {
	case EventDefPlanningStarted:
		var p struct {
			WorkID    string `json:"workId"`
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.WorkID == "" {
			return fmt.Errorf("work: %s requires workId", typ)
		}
		if p.SessionID == "" {
			return fmt.Errorf("work: %s requires sessionId", typ)
		}

	case EventDefRevisionCreated:
		var p struct {
			WorkID         string    `json:"workId"`
			Revision       wireInt64 `json:"revision"`
			ParentRevision wireInt64 `json:"parentRevision"`
			Digest         string    `json:"digest"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.WorkID == "" {
			return fmt.Errorf("work: %s requires workId", typ)
		}
		if p.Digest == "" {
			return fmt.Errorf("work: %s requires digest", typ)
		}
		if err := wireNonNeg(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ParentRevision, string(typ)+" parentRevision"); err != nil {
			return err
		}

	case EventDefRevisionApplied:
		var p struct {
			WorkID           string    `json:"workId"`
			Revision         wireInt64 `json:"revision"`
			PreviousRevision wireInt64 `json:"previousRevision"`
			ExpectedRevision wireInt64 `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.WorkID == "" {
			return fmt.Errorf("work: %s requires workId", typ)
		}
		if err := wireNonNeg(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.PreviousRevision, string(typ)+" previousRevision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ExpectedRevision, string(typ)+" expectedRevision"); err != nil {
			return err
		}

	case EventArtifactSlotDeclared:
		var p struct {
			SlotID        string    `json:"slotId"`
			WorkID        string    `json:"workId"`
			DefinitionRev wireInt64 `json:"definitionRev"`
			Title         string    `json:"title"`
			Kind          string    `json:"kind"`
			ExpectedCount wireInt64 `json:"expectedCount"`
			Required      bool      `json:"required"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.SlotID == "" || p.Title == "" || p.Kind == "" || p.WorkID == "" {
			return fmt.Errorf("work: %s requires slotId/title/kind/workId", typ)
		}
		if err := wireNonNeg(p.DefinitionRev, string(typ)+" definitionRev"); err != nil {
			return err
		}
		if err := wirePositive(p.ExpectedCount, string(typ)+" expectedCount"); err != nil {
			return err
		}

	case EventArtifactSlotUpdated:
		var p struct {
			SlotID string `json:"slotId"`
			WorkID string `json:"workId"`
			State  string `json:"state"`
			Refs   []struct {
				Status string `json:"status"`
			} `json:"refs"`
			Revision wireInt64 `json:"revision"`
			Progress *float64  `json:"progress"`
			Error    *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.SlotID == "" || p.WorkID == "" {
			return fmt.Errorf("work: %s requires slotId/workId", typ)
		}
		if !validSlotStates[p.State] {
			return fmt.Errorf("work: %s invalid state %q", typ, p.State)
		}
		if err := wireNonNeg(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if p.Progress != nil && (*p.Progress < 0 || *p.Progress > 1) {
			return fmt.Errorf("work: %s progress must be 0..1", typ)
		}
		for i, ref := range p.Refs {
			if !validArtifactRefStatus(ref.Status) {
				return fmt.Errorf("work: %s refs[%d].status %q is invalid", typ, i, ref.Status)
			}
		}
		if p.State == string(SlotFailed) && p.Error == nil {
			return fmt.Errorf("work: %s state=failed requires error", typ)
		}

	case EventInputRequested:
		var p InputRequestedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.InputID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" || p.BlockID == "" || p.SpecID == "" {
			return fmt.Errorf("work: %s requires inputId/workId/runId/taskId/blockId/specId", typ)
		}
		if p.CustomSpec != nil {
			if p.CustomSpec.ID != p.SpecID || strings.TrimSpace(p.CustomSpec.Label) == "" {
				return fmt.Errorf("work: %s customSpec must match specId and include label", typ)
			}
			if p.CustomSpec.Kind != InputText && p.CustomSpec.Kind != InputFile {
				return fmt.Errorf("work: %s customSpec kind must be text or file", typ)
			}
		}

	case EventInputDraftSaved:
		var p struct {
			InputID          string          `json:"inputId"`
			WorkID           string          `json:"workId"`
			RunID            string          `json:"runId"`
			TaskID           string          `json:"taskId"`
			BlockID          string          `json:"blockId"`
			SpecID           string          `json:"specId"`
			Value            json.RawMessage `json:"value"`
			Revision         wireInt64       `json:"revision"`
			ExpectedRevision wireInt64       `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.InputID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" || p.BlockID == "" || p.SpecID == "" {
			return fmt.Errorf("work: %s requires inputId/workId/runId/taskId/blockId/specId", typ)
		}
		if err := wireNonNeg(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ExpectedRevision, string(typ)+" expectedRevision"); err != nil {
			return err
		}

	case EventInputSubmitted:
		var p struct {
			InputID          string          `json:"inputId"`
			WorkID           string          `json:"workId"`
			RunID            string          `json:"runId"`
			TaskID           string          `json:"taskId"`
			BlockID          string          `json:"blockId"`
			SpecID           string          `json:"specId"`
			Value            json.RawMessage `json:"value"`
			Revision         wireInt64       `json:"revision"`
			ExpectedRevision wireInt64       `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.InputID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" || p.BlockID == "" || p.SpecID == "" {
			return fmt.Errorf("work: %s requires inputId/workId/runId/taskId/blockId/specId", typ)
		}
		if p.Value == nil {
			return fmt.Errorf("work: %s requires value", typ)
		}
		if err := wireNonNeg(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ExpectedRevision, string(typ)+" expectedRevision"); err != nil {
			return err
		}

	case EventInputRejected:
		var p struct {
			InputID          string          `json:"inputId"`
			WorkID           string          `json:"workId"`
			RunID            string          `json:"runId"`
			TaskID           string          `json:"taskId"`
			BlockID          string          `json:"blockId"`
			SpecID           string          `json:"specId"`
			Value            json.RawMessage `json:"value"`
			Revision         wireInt64       `json:"revision"`
			ExpectedRevision wireInt64       `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.InputID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" || p.BlockID == "" || p.SpecID == "" {
			return fmt.Errorf("work: %s requires inputId/workId/runId/taskId/blockId/specId", typ)
		}
		if p.Value == nil {
			return fmt.Errorf("work: %s requires value", typ)
		}
		if err := wirePositive(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ExpectedRevision, string(typ)+" expectedRevision"); err != nil {
			return err
		}

	case EventInputCornerstoneChanged:
		var p struct {
			InputID          string    `json:"inputId"`
			WorkID           string    `json:"workId"`
			RunID            string    `json:"runId"`
			TaskID           string    `json:"taskId"`
			BlockID          string    `json:"blockId"`
			SpecID           string    `json:"specId"`
			CornerstoneID    string    `json:"cornerstoneId"`
			Pinned           bool      `json:"pinned"`
			Revision         wireInt64 `json:"revision"`
			ExpectedRevision wireInt64 `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.InputID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" || p.BlockID == "" || p.SpecID == "" {
			return fmt.Errorf("work: %s requires inputId/workId/runId/taskId/blockId/specId", typ)
		}
		if p.Pinned && p.CornerstoneID == "" {
			return fmt.Errorf("work: %s requires cornerstoneId when pinned", typ)
		}
		if err := wirePositive(p.Revision, string(typ)+" revision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ExpectedRevision, string(typ)+" expectedRevision"); err != nil {
			return err
		}

	case EventPatchPreviewed:
		var p struct {
			PatchID                 string        `json:"patchId"`
			WorkID                  string        `json:"workId"`
			RunID                   string        `json:"runId"`
			TaskID                  string        `json:"taskId"`
			BlockID                 string        `json:"blockId"`
			SessionID               string        `json:"sessionId"`
			Scope                   string        `json:"scope"`
			BaseDefinitionRev       wireInt64     `json:"baseDefinitionRev"`
			BaseBlockRev            wireInt64     `json:"baseBlockRev"`
			Operations              []PatchOp     `json:"operations"`
			Actions                 []PatchAction `json:"actions"`
			AffectedNodeIDs         []string      `json:"affectedNodeIds"`
			AffectedBlockIDs        []string      `json:"affectedBlockIds"`
			AffectedArtifactSlotIDs []string      `json:"affectedArtifactSlotIds"`
			StaleArtifactSlotIDs    []string      `json:"staleArtifactSlotIds"`
			InvalidatedTasks        []string      `json:"invalidatedTasks"`
			RequiresRerun           bool          `json:"requiresRerun"`
			Digest                  string        `json:"digest"`
			ExpiresAt               *time.Time    `json:"expiresAt"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.PatchID == "" || p.WorkID == "" || p.RunID == "" || p.TaskID == "" ||
			p.BlockID == "" || p.SessionID == "" {
			return fmt.Errorf("work: %s requires patchId/workId/runId/taskId/blockId/sessionId", typ)
		}
		if p.Scope != string(PatchBlock) && p.Scope != string(PatchWorkflow) {
			return fmt.Errorf("work: %s invalid scope %q", typ, p.Scope)
		}
		if !p.BaseDefinitionRev.OK || p.BaseDefinitionRev.V <= 0 ||
			!p.BaseBlockRev.OK || p.BaseBlockRev.V <= 0 ||
			len(p.Operations) == 0 || p.Digest == "" || p.ExpiresAt == nil || p.ExpiresAt.IsZero() {
			return fmt.Errorf("work: %s requires immutable body/digest/expiry/base revisions", typ)
		}
		if err := ValidatePatchOps(p.Operations); err != nil {
			return fmt.Errorf("work: %s invalid operations: %w", typ, err)
		}
		for i, operation := range p.Operations {
			path, err := CompilePatchPath(operation.Path)
			if err != nil {
				return fmt.Errorf("work: %s operation[%d]: %w", typ, i, err)
			}
			if p.Scope == string(PatchBlock) &&
				(path.Kind != PathBlocks || path.Segments[1] != p.BlockID) {
				return fmt.Errorf("work: %s block scope operation[%d] escapes block %q", typ, i, p.BlockID)
			}
			if p.Scope == string(PatchWorkflow) && path.Kind == PathBlocks {
				return fmt.Errorf("work: %s workflow scope operation[%d] targets runtime block", typ, i)
			}
		}
		for i, action := range p.Actions {
			switch action.Action {
			case PatchActionReuse, PatchActionRerun:
				if strings.TrimSpace(action.NodeID) == "" || strings.TrimSpace(action.ArtifactSlotID) != "" {
					return fmt.Errorf("work: %s action[%d] %q requires only nodeId", typ, i, action.Action)
				}
			case PatchActionReformat:
				if strings.TrimSpace(action.ArtifactSlotID) == "" || strings.TrimSpace(action.NodeID) != "" {
					return fmt.Errorf("work: %s action[%d] reformat requires only artifactSlotId", typ, i)
				}
			case PatchActionAskUser:
				return fmt.Errorf("work: %s cannot persist unresolved ask_user action", typ)
			default:
				return fmt.Errorf("work: %s action[%d] has invalid action %q", typ, i, action.Action)
			}
		}
		for i, v := range p.AffectedNodeIDs {
			if v == "" {
				return fmt.Errorf("work: %s affectedNodeIds[%d] is empty", typ, i)
			}
		}
		for field, values := range map[string][]string{
			"affectedBlockIds":        p.AffectedBlockIDs,
			"affectedArtifactSlotIds": p.AffectedArtifactSlotIDs,
			"staleArtifactSlotIds":    p.StaleArtifactSlotIDs,
		} {
			for i, v := range values {
				if v == "" {
					return fmt.Errorf("work: %s %s[%d] is empty", typ, field, i)
				}
			}
		}
		for i, v := range p.InvalidatedTasks {
			if v == "" {
				return fmt.Errorf("work: %s invalidatedTasks[%d] is empty", typ, i)
			}
		}

	case EventPatchApplied:
		var p struct {
			PatchID          string    `json:"patchId"`
			WorkID           string    `json:"workId"`
			Scope            string    `json:"scope"`
			NewRevision      wireInt64 `json:"newRevision"`
			ExpectedRevision wireInt64 `json:"expectedRevision"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.PatchID == "" || p.WorkID == "" {
			return fmt.Errorf("work: %s requires patchId/workId", typ)
		}
		if p.Scope != string(PatchBlock) && p.Scope != string(PatchWorkflow) {
			return fmt.Errorf("work: %s invalid scope %q", typ, p.Scope)
		}
		if err := wireNonNeg(p.NewRevision, string(typ)+" newRevision"); err != nil {
			return err
		}
		if err := wireNonNeg(p.ExpectedRevision, string(typ)+" expectedRevision"); err != nil {
			return err
		}

	case EventTaskInvalidated, EventTaskReady:
		var p struct {
			TaskID string `json:"taskId"`
			WorkID string `json:"workId"`
			RunID  string `json:"runId"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.TaskID == "" || p.WorkID == "" || p.RunID == "" {
			return fmt.Errorf("work: %s requires taskId/workId/runId", typ)
		}

	case EventTaskWaitingInput:
		var p struct {
			TaskID   string   `json:"taskId"`
			WorkID   string   `json:"workId"`
			RunID    string   `json:"runId"`
			InputIDs []string `json:"inputIds"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.TaskID == "" || p.WorkID == "" || p.RunID == "" {
			return fmt.Errorf("work: %s requires taskId/workId/runId", typ)
		}
		if len(p.InputIDs) == 0 {
			return fmt.Errorf("work: %s requires at least one inputIds", typ)
		}

	case EventTaskWaitingApproval:
		var p struct {
			TaskID        string `json:"taskId"`
			WorkID        string `json:"workId"`
			RunID         string `json:"runId"`
			ApprovalToken string `json:"approvalToken"`
		}
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.TaskID == "" || p.WorkID == "" || p.RunID == "" {
			return fmt.Errorf("work: %s requires taskId/workId/runId", typ)
		}
		if p.ApprovalToken == "" {
			return fmt.Errorf("work: %s requires approvalToken", typ)
		}

	case EventTaskRuntimeCreated:
		var p TaskRuntimeCreatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.TaskID == "" || p.WorkID == "" || p.RunID == "" || p.NodeID == "" {
			return fmt.Errorf("work: %s requires taskId/workId/runId/nodeId", typ)
		}
		if p.DefinitionRev <= 0 {
			return fmt.Errorf("work: %s requires positive definitionRev", typ)
		}
		if p.ExpectedRevision != 0 {
			return fmt.Errorf("work: %s expectedRevision must be zero", typ)
		}
		if p.Runtime.TaskID != p.TaskID || p.Runtime.WorkID != p.WorkID ||
			p.Runtime.RunID != p.RunID || p.Runtime.NodeID != p.NodeID ||
			p.Runtime.DefinitionRev != p.DefinitionRev || p.Runtime.Revision != 1 {
			return fmt.Errorf("work: %s payload/runtime context mismatch", typ)
		}

	case EventTaskRuntimeUpdated:
		var p TaskRuntimeUpdatedPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.TaskID == "" || p.WorkID == "" || p.RunID == "" {
			return fmt.Errorf("work: %s requires taskId/workId/runId", typ)
		}
		if !isValidTaskV2State(p.State) {
			return fmt.Errorf("work: %s invalid state %q", typ, p.State)
		}
		if p.ExpectedRevision <= 0 {
			return fmt.Errorf("work: %s requires positive expectedRevision", typ)
		}
		if p.Runtime.TaskID != p.TaskID || p.Runtime.WorkID != p.WorkID ||
			p.Runtime.RunID != p.RunID || p.Runtime.State != p.State ||
			p.Runtime.DefinitionRev <= 0 || p.Runtime.Revision <= 1 {
			return fmt.Errorf("work: %s payload/runtime context mismatch", typ)
		}

	case EventTaskStaleResult:
		var p TaskStaleResultPayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return fmt.Errorf("work: %s payload: %w", typ, err)
		}
		if p.TaskID == "" || p.WorkID == "" || p.RunID == "" || p.AttemptID == "" {
			return fmt.Errorf("work: %s requires taskId/workId/runId/attemptId", typ)
		}
		if p.StaleToken == "" || p.CurrentToken == "" {
			return fmt.Errorf("work: %s requires staleToken and currentToken", typ)
		}
		if p.ExpectedRevision <= 0 {
			return fmt.Errorf("work: %s requires positive expectedRevision", typ)
		}

	default:
		return fmt.Errorf("work: unhandled V2 event type %q", typ)
	}
	return nil
}

var validSlotStates = map[string]bool{
	string(SlotReserved): true, string(SlotGenerating): true,
	string(SlotReady): true, string(SlotPartial): true,
	string(SlotFailed): true, string(SlotStale): true,
}

func isValidTaskV2State(s TaskStateV2) bool {
	switch s {
	case TaskPending, TaskReady, TaskRunning, TaskWaitingInput, TaskWaitingApproval,
		TaskCompleted, TaskFailedRetryable, TaskFailedTerminal, TaskCanceled, TaskInvalidated:
		return true
	}
	return false
}

//
// These are the structured payloads carried by V2 events. Each is the
// json.RawMessage content of a WorkEvent.Payload field.

// DefPlanningStartedPayload is carried by EventDefPlanningStarted.
type DefPlanningStartedPayload struct {
	WorkID       string       `json:"workId"`
	SessionID    string       `json:"sessionId"`
	BlueprintRef BlueprintRef `json:"blueprintRef,omitempty"`
}

// DefRevisionCreatedPayload is carried by EventDefRevisionCreated.
type DefRevisionCreatedPayload struct {
	WorkID         string           `json:"workId"`
	Revision       int64            `json:"revision"`
	ParentRevision int64            `json:"parentRevision"`
	Digest         string           `json:"digest"`
	Receipt        *V2IntentReceipt `json:"receipt,omitempty"`
	SuggestedName  string           `json:"suggestedName,omitempty"`
}

// DefRevisionAppliedPayload is carried by EventDefRevisionApplied.
type DefRevisionAppliedPayload struct {
	WorkID           string   `json:"workId"`
	Revision         int64    `json:"revision"`
	PreviousRevision int64    `json:"previousRevision"`
	ExpectedRevision int64    `json:"expectedRevision"`
	InvalidatedTasks []string `json:"invalidatedTasks,omitempty"`
}

// ArtifactSlotDeclaredPayload is carried by EventArtifactSlotDeclared.
type ArtifactSlotDeclaredPayload struct {
	SlotID        string `json:"slotId"`
	WorkID        string `json:"workId"`
	DefinitionRev int64  `json:"definitionRev"`
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	ExpectedCount int    `json:"expectedCount"`
	Required      bool   `json:"required"`
}

// ArtifactSlotUpdatedPayload is carried by EventArtifactSlotUpdated.
type ArtifactSlotUpdatedPayload struct {
	SlotID         string                     `json:"slotId"`
	WorkID         string                     `json:"workId"`
	State          ArtifactSlotState          `json:"state"`
	Refs           []ArtifactRef              `json:"refs,omitempty"`
	UpstreamDigest string                     `json:"upstreamDigest,omitempty"`
	Progress       *float64                   `json:"progress,omitempty"`
	Summary        string                     `json:"summary,omitempty"`
	Error          *ArtifactError             `json:"error,omitempty"`
	Revision       int64                      `json:"revision"`
	Receipt        *ArtifactSlotUpdateReceipt `json:"receipt,omitempty"`
}

// InputRequestedPayload is carried by EventInputRequested.
type InputRequestedPayload struct {
	InputID    string              `json:"inputId"`
	WorkID     string              `json:"workId"`
	RunID      string              `json:"runId"`
	TaskID     string              `json:"taskId"`
	BlockID    string              `json:"blockId"`
	SpecID     string              `json:"specId"`
	CustomSpec *InputSpec          `json:"customSpec,omitempty"`
	Receipt    *InputIntentReceipt `json:"receipt,omitempty"`
}

// InputDraftSavedPayload is carried by EventInputDraftSaved.
type InputDraftSavedPayload struct {
	InputID          string              `json:"inputId"`
	WorkID           string              `json:"workId"`
	RunID            string              `json:"runId"`
	TaskID           string              `json:"taskId"`
	BlockID          string              `json:"blockId"`
	SpecID           string              `json:"specId"`
	Value            json.RawMessage     `json:"value"`
	Extra            string              `json:"extra,omitempty"`
	Source           string              `json:"source,omitempty"`
	UpdatedBy        string              `json:"updatedBy,omitempty"`
	Revision         int64               `json:"revision"`
	ExpectedRevision int64               `json:"expectedRevision"`
	Receipt          *InputIntentReceipt `json:"receipt,omitempty"`
}

// InputSubmittedPayload is carried by EventInputSubmitted.
type InputSubmittedPayload struct {
	InputID          string              `json:"inputId"`
	WorkID           string              `json:"workId"`
	RunID            string              `json:"runId"`
	TaskID           string              `json:"taskId"`
	BlockID          string              `json:"blockId"`
	SpecID           string              `json:"specId"`
	Value            json.RawMessage     `json:"value"`
	Extra            string              `json:"extra,omitempty"`
	Source           string              `json:"source,omitempty"`
	UpdatedBy        string              `json:"updatedBy,omitempty"`
	Revision         int64               `json:"revision"`
	ExpectedRevision int64               `json:"expectedRevision"`
	AffectedTaskIDs  []string            `json:"affectedTaskIds,omitempty"`
	Receipt          *InputIntentReceipt `json:"receipt,omitempty"`
}

// InputRejectedPayload is carried by EventInputRejected.
type InputRejectedPayload struct {
	InputID          string              `json:"inputId"`
	WorkID           string              `json:"workId"`
	RunID            string              `json:"runId"`
	TaskID           string              `json:"taskId"`
	BlockID          string              `json:"blockId"`
	SpecID           string              `json:"specId"`
	Value            json.RawMessage     `json:"value"`
	Extra            string              `json:"extra,omitempty"`
	Reason           string              `json:"reason,omitempty"`
	Source           string              `json:"source,omitempty"`
	UpdatedBy        string              `json:"updatedBy,omitempty"`
	Revision         int64               `json:"revision"`
	ExpectedRevision int64               `json:"expectedRevision"`
	Receipt          *InputIntentReceipt `json:"receipt,omitempty"`
}

// InputCornerstoneChangedPayload is carried by EventInputCornerstoneChanged.
type InputCornerstoneChangedPayload struct {
	InputID          string              `json:"inputId"`
	WorkID           string              `json:"workId"`
	RunID            string              `json:"runId"`
	TaskID           string              `json:"taskId"`
	BlockID          string              `json:"blockId"`
	SpecID           string              `json:"specId"`
	CornerstoneID    string              `json:"cornerstoneId"`
	Pinned           bool                `json:"pinned"`
	Revision         int64               `json:"revision"`
	ExpectedRevision int64               `json:"expectedRevision"`
	Receipt          *InputIntentReceipt `json:"receipt,omitempty"`
}

// PatchPreviewedPayload is carried by EventPatchPreviewed.
type PatchPreviewedPayload struct {
	PatchID                 string              `json:"patchId"`
	WorkID                  string              `json:"workId"`
	RunID                   string              `json:"runId"`
	TaskID                  string              `json:"taskId"`
	BlockID                 string              `json:"blockId"`
	SessionID               string              `json:"sessionId"`
	Scope                   PatchScope          `json:"scope"`
	BaseDefinitionRev       int64               `json:"baseDefinitionRev,omitempty"`
	BaseBlockRev            int64               `json:"baseBlockRev,omitempty"`
	Operations              []PatchOp           `json:"operations,omitempty"`
	Actions                 []PatchAction       `json:"actions,omitempty"`
	AffectedNodeIDs         []string            `json:"affectedNodeIds"`
	AffectedBlockIDs        []string            `json:"affectedBlockIds"`
	AffectedArtifactSlotIDs []string            `json:"affectedArtifactSlotIds"`
	StaleArtifactSlotIDs    []string            `json:"staleArtifactSlotIds"`
	InvalidatedTasks        []string            `json:"invalidatedTasks"`
	RequiresRerun           bool                `json:"requiresRerun"`
	Digest                  string              `json:"digest,omitempty"`
	ExpiresAt               *time.Time          `json:"expiresAt,omitempty"`
	Receipt                 *PatchIntentReceipt `json:"receipt,omitempty"`
}

// PatchAppliedPayload is carried by EventPatchApplied.
type PatchAppliedPayload struct {
	PatchID            string              `json:"patchId"`
	WorkID             string              `json:"workId"`
	RunID              string              `json:"runId"`
	TaskID             string              `json:"taskId"`
	BlockID            string              `json:"blockId"`
	Scope              PatchScope          `json:"scope"`
	NewRevision        int64               `json:"newRevision"`
	ExpectedRevision   int64               `json:"expectedRevision"`
	InvalidatedTaskIDs []string            `json:"invalidatedTaskIds"`
	Receipt            *PatchIntentReceipt `json:"receipt,omitempty"`
}

// TaskInvalidatedPayload is carried by EventTaskInvalidated.
type TaskInvalidatedPayload struct {
	TaskID string `json:"taskId"`
	WorkID string `json:"workId"`
	RunID  string `json:"runId"`
	Reason string `json:"reason,omitempty"`
}

// TaskReadyPayload is carried by EventTaskReady.
type TaskReadyPayload struct {
	TaskID string `json:"taskId"`
	WorkID string `json:"workId"`
	RunID  string `json:"runId"`
}

// TaskWaitingPayload is carried by EventTaskWaitingInput and
// EventTaskWaitingApproval.
type TaskWaitingPayload struct {
	TaskID        string   `json:"taskId"`
	WorkID        string   `json:"workId"`
	RunID         string   `json:"runId"`
	InputIDs      []string `json:"inputIds,omitempty"`
	ApprovalToken string   `json:"approvalToken,omitempty"`
}

// ── Task runtime payloads ──────────────────────────────────────────────────

// TaskRuntimeCreatedPayload is carried by EventTaskRuntimeCreated.
type TaskRuntimeCreatedPayload struct {
	TaskID           string        `json:"taskId"`
	WorkID           string        `json:"workId"`
	RunID            string        `json:"runId"`
	NodeID           string        `json:"nodeId"`
	ExpectedRevision int64         `json:"expectedRevision"`
	DefinitionRev    int64         `json:"definitionRev"`
	SideEffectClass  string        `json:"sideEffectClass,omitempty"`
	InputDigest      string        `json:"inputDigest,omitempty"`
	DependencyDigest string        `json:"dependencyDigest,omitempty"`
	ExecutionToken   string        `json:"executionToken,omitempty"`
	Runtime          V2TaskRuntime `json:"runtime"`
}

// TaskRuntimeUpdatedPayload is carried by EventTaskRuntimeUpdated.
type TaskRuntimeUpdatedPayload struct {
	TaskID           string        `json:"taskId"`
	WorkID           string        `json:"workId"`
	RunID            string        `json:"runId"`
	ExpectedRevision int64         `json:"expectedRevision"`
	State            TaskStateV2   `json:"state"`
	Runtime          V2TaskRuntime `json:"runtime"`
	// Attempt is set when a new attempt is created within this update.
	Attempt *V2Attempt `json:"attempt,omitempty"`
}

// TaskStaleResultPayload is carried by EventTaskStaleResult.
type TaskStaleResultPayload struct {
	TaskID           string          `json:"taskId"`
	WorkID           string          `json:"workId"`
	RunID            string          `json:"runId"`
	ExpectedRevision int64           `json:"expectedRevision"`
	AttemptID        string          `json:"attemptId"`
	StaleToken       string          `json:"staleToken"`
	CurrentToken     string          `json:"currentToken"`
	ResultRef        string          `json:"resultRef,omitempty"`
	PreviousReceipt  *AttemptReceipt `json:"previousReceipt,omitempty"`
}
