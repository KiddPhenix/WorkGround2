package work

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ReusableField is one value a saved flow may ask for on every run.
type ReusableField struct {
	Key      string          `json:"key"`
	Label    string          `json:"label"`
	Kind     string          `json:"kind"`
	Required bool            `json:"required"`
	Variable bool            `json:"variable"`
	Value    json.RawMessage `json:"value,omitempty"`
}

// ReusableFlow is the public immutable metadata for a saved flow.
type ReusableFlow struct {
	SchemaVersion int             `json:"schemaVersion"`
	ID            string          `json:"id"`
	Name          string          `json:"name"`
	SourceWorkID  string          `json:"sourceWorkId"`
	Fields        []ReusableField `json:"fields"`
	Digest        string          `json:"digest"`
	CreatedAt     time.Time       `json:"createdAt" ts_type:"string"`
}

// ReusableFlowSetup is the read-only preparation result used by the restart
// choice UI. Existing is non-nil after the first successful save.
type ReusableFlowSetup struct {
	Existing      *ReusableFlow   `json:"existing,omitempty"`
	SuggestedName string          `json:"suggestedName"`
	Fields        []ReusableField `json:"fields"`
	FixedItems    []string        `json:"fixedItems"`
}

// ReusableFlowRun is returned after a new independent Work has been created
// and its first run has been started.
type ReusableFlowRun struct {
	Flow      ReusableFlow `json:"flow"`
	Work      *Work        `json:"work"`
	Run       *WorkflowRun `json:"run,omitempty"`
	Duplicate bool         `json:"duplicate"`
}

type reusableFlowRecord struct {
	Flow          ReusableFlow            `json:"flow"`
	Template      Work                    `json:"template"`
	V2Definition  *WorkDefinitionRevision `json:"v2Definition,omitempty"`
	SaveRequestID string                  `json:"saveRequestId"`
}

// PrepareReusableFlow returns authoritative repeatable fields and detects a
// previously saved flow without mutating either the Work or the flow store.
func (s *Service) PrepareReusableFlow(ctx context.Context, input PrepareReusableFlowInput) (*ReusableFlowSetup, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	sourceID := strings.TrimSpace(input.SourceWorkID)
	if sourceID == "" {
		return nil, errors.New("work: PrepareReusableFlow: sourceWorkId is required")
	}
	repo, err := s.reusableFlowStore()
	if err != nil {
		return nil, err
	}
	existing, err := repo.FindReusableFlowBySource(sourceID)
	if err != nil {
		return nil, fmt.Errorf("work: PrepareReusableFlow: find saved flow: %w", err)
	}
	if existing != nil {
		flow := cloneReusableFlow(existing.Flow)
		return &ReusableFlowSetup{
			Existing: &flow, SuggestedName: flow.Name,
			Fields: cloneReusableFields(flow.Fields), FixedItems: reusableFixedItems(),
		}, nil
	}
	source, _, err := s.store.LoadState(sourceID, "")
	if err != nil {
		return nil, fmt.Errorf("work: PrepareReusableFlow: load source: %w", err)
	}
	fields, _, err := s.reusableFields(source)
	if err != nil {
		return nil, err
	}
	return &ReusableFlowSetup{
		SuggestedName: strings.TrimSpace(source.Name), Fields: fields,
		FixedItems: reusableFixedItems(),
	}, nil
}

// SaveReusableFlow freezes the current definition and repeatable values. The
// store owns idempotency across restarts and rejects a reused request with a
// different intent.
func (s *Service) SaveReusableFlow(ctx context.Context, input SaveReusableFlowInput) (*ReusableFlow, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("SaveReusableFlow", input.RequestID)
	if err != nil {
		return nil, err
	}
	sourceID := strings.TrimSpace(input.SourceWorkID)
	if sourceID == "" {
		return nil, errors.New("work: SaveReusableFlow: sourceWorkId is required")
	}
	repo, err := s.reusableFlowStore()
	if err != nil {
		return nil, err
	}
	if existing, findErr := repo.FindReusableFlowBySource(sourceID); findErr != nil {
		return nil, fmt.Errorf("work: SaveReusableFlow: find saved flow: %w", findErr)
	} else if existing != nil {
		flow := cloneReusableFlow(existing.Flow)
		return &flow, nil
	}
	source, _, err := s.store.LoadState(sourceID, "")
	if err != nil {
		return nil, fmt.Errorf("work: SaveReusableFlow: load source: %w", err)
	}
	fields, definition, err := s.reusableFields(source)
	if err != nil {
		return nil, err
	}
	selected, err := normalizeReusableVariableKeys(fields, input.VariableKeys)
	if err != nil {
		return nil, fmt.Errorf("work: SaveReusableFlow: %w", err)
	}
	for index := range fields {
		fields[index].Variable = selected[fields[index].Key]
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = strings.TrimSpace(source.Name)
	}
	if name == "" {
		return nil, errors.New("work: SaveReusableFlow: name is required")
	}
	template, err := reusableTemplate(source)
	if err != nil {
		return nil, fmt.Errorf("work: SaveReusableFlow: build template: %w", err)
	}
	now := time.Now().UTC()
	flow := ReusableFlow{
		SchemaVersion: SchemaVersion, ID: reusableFlowID(sourceID), Name: name,
		SourceWorkID: sourceID, Fields: fields, CreatedAt: now,
	}
	flow.Digest, err = reusableFlowDigest(flow, template, definition)
	if err != nil {
		return nil, err
	}
	record := &reusableFlowRecord{
		Flow: flow, Template: *template, V2Definition: definition,
		SaveRequestID: requestID,
	}
	saved, err := repo.SaveReusableFlow(record)
	if err != nil {
		return nil, fmt.Errorf("work: SaveReusableFlow: persist: %w", err)
	}
	result := cloneReusableFlow(saved.Flow)
	return &result, nil
}

// RunReusableFlow creates and immediately starts a new Work. It never mutates
// the source Work. The same requestID safely resumes a partial create/apply.
func (s *Service) RunReusableFlow(ctx context.Context, input RunReusableFlowInput) (*ReusableFlowRun, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("RunReusableFlow", input.RequestID)
	if err != nil {
		return nil, err
	}
	repo, err := s.reusableFlowStore()
	if err != nil {
		return nil, err
	}
	flowID := strings.TrimSpace(input.FlowID)
	if flowID == "" {
		return nil, errors.New("work: RunReusableFlow: flowId is required")
	}
	record, err := repo.LoadReusableFlow(flowID)
	if err != nil {
		return nil, fmt.Errorf("work: RunReusableFlow: load flow: %w", err)
	}
	if record == nil {
		return nil, fmt.Errorf("work: RunReusableFlow: flow %q not found", flowID)
	}
	values, err := reusableRunValues(record.Flow.Fields, input.Values)
	if err != nil {
		return nil, fmt.Errorf("work: RunReusableFlow: %w", err)
	}
	runHash, err := reusableRunHash(record.Flow.Digest, values)
	if err != nil {
		return nil, err
	}
	if record.V2Definition != nil || record.Template.SchemaVersion >= SchemaVersionV2 {
		return s.runReusableV2(ctx, record, values, requestID, runHash)
	}
	return s.runReusableV1(ctx, record, values, requestID, runHash)
}

func (s *Service) reusableFlowStore() (ReusableFlowStore, error) {
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	repo, ok := s.store.(ReusableFlowStore)
	if !ok {
		return nil, errors.New("work: reusable flow persistence is unavailable")
	}
	return repo, nil
}

func (s *Service) reusableFields(source *Work) ([]ReusableField, *WorkDefinitionRevision, error) {
	if source == nil {
		return nil, nil, ErrWorkNilInput
	}
	if source.SchemaVersion >= SchemaVersionV2 && source.V2LatestRevision > 0 {
		revision := source.V2CurrentRevision
		if revision == 0 {
			revision = source.V2LatestRevision
		}
		definition, err := s.definitionStore().LoadRevision(source.ID, revision)
		if err != nil {
			return nil, nil, fmt.Errorf("work: load reusable V2 definition: %w", err)
		}
		fields := []ReusableField{{Key: "goal", Label: "工作目标", Kind: "text", Required: true, Value: mustReusableJSON(definition.Goal)}}
		for _, spec := range definition.InputSpecs {
			if spec.Kind == InputApproval {
				continue
			}
			value := append(json.RawMessage(nil), spec.DefaultValue...)
			if latest := latestReusableInput(source.V2Inputs, spec.ID); latest != nil {
				value = append(json.RawMessage(nil), latest.Value...)
			}
			fields = append(fields, ReusableField{
				Key: "input:" + spec.ID, Label: spec.Label, Kind: string(spec.Kind),
				Required: spec.Required, Value: value,
			})
		}
		return fields, definition, nil
	}
	fields := []ReusableField{{Key: "prompt", Label: "任务内容", Kind: "text", Required: true, Value: mustReusableJSON(source.Prompt)}}
	var schema struct {
		Properties map[string]struct {
			Title string `json:"title"`
			Type  string `json:"type"`
		} `json:"properties"`
		Required []string `json:"required"`
	}
	if len(source.Definition.InputSchema) > 0 && json.Unmarshal(source.Definition.InputSchema, &schema) == nil {
		required := make(map[string]bool, len(schema.Required))
		for _, key := range schema.Required {
			required[key] = true
		}
		keys := make([]string, 0, len(schema.Properties))
		for key := range schema.Properties {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			property := schema.Properties[key]
			label := strings.TrimSpace(property.Title)
			if label == "" {
				label = key
			}
			kind := property.Type
			if kind == "" {
				kind = "text"
			}
			fields = append(fields, ReusableField{
				Key: "input:" + key, Label: label, Kind: kind, Required: required[key],
				Value: mustReusableJSON(source.Inputs[key]),
			})
		}
	}
	return fields, nil, nil
}

func latestReusableInput(inputs []WorkInput, specID string) *WorkInput {
	var latest *WorkInput
	for index := range inputs {
		candidate := &inputs[index]
		if candidate.SpecID != specID || len(candidate.Value) == 0 {
			continue
		}
		if latest == nil || candidate.UpdatedAt.After(latest.UpdatedAt) ||
			(candidate.UpdatedAt.Equal(latest.UpdatedAt) && candidate.Revision > latest.Revision) {
			latest = candidate
		}
	}
	return latest
}

func reusableTemplate(source *Work) (*Work, error) {
	template, err := cloneWork(source)
	if err != nil {
		return nil, err
	}
	template.State = WorkDraft
	template.ArchiveState = ArchiveActive
	template.Runs = nil
	template.ActionReceipts = nil
	template.Conclusions = nil
	template.ReferencedWorks = nil
	template.RerunOf = ""
	template.CopiedFrom = ""
	template.ReusableFlowID = ""
	template.ReusableRunHash = ""
	template.RerunUpgraded = false
	template.MigrationPath = nil
	template.ArchivedAt = nil
	template.V2ArtifactSlots = nil
	template.V2ArtifactReceipts = nil
	template.V2Inputs = nil
	template.V2InputReceipts = nil
	template.V2PatchPreviews = nil
	template.V2PatchReceipts = nil
	template.V2TaskRuntimes = nil
	if source.SchemaVersion < SchemaVersionV2 {
		template.Blocks = cloneBlocks(source.Blocks)
		template.Placements = append([]BlockPlacement(nil), source.Placements...)
	} else {
		template.Blocks = nil
		template.Placements = nil
	}
	return template, nil
}

// rebindCornerstones rebuilds cornerstone stable IDs for a new target Work.
// Content, status, tags and other business fields are preserved; only the
// identity fields (ID and WorkID) are recomputed for the new owner.
func rebindCornerstones(cs []Cornerstone, newWorkID string, blobs map[string][]byte) ([]Cornerstone, error) {
	if len(cs) == 0 {
		return cs, nil
	}
	result := make([]Cornerstone, len(cs))
	seen := make(map[string]struct{}, len(cs))
	for i := range cs {
		result[i] = cs[i]
		content := cs[i].Content
		if digest := cs[i].Ref.BlobDigest; digest != "" {
			data, ok := blobs[digest]
			if !ok {
				return nil, fmt.Errorf("rebind cornerstone %q: source blob %q is missing", cs[i].ID, digest)
			}
			content = string(data)
		}
		id, err := computeStableCornerstoneID(newWorkID, PinCornerstoneInput{
			Type: cs[i].Type, Content: content, Ref: cs[i].Ref,
		})
		if err != nil {
			return nil, fmt.Errorf("rebind cornerstone %q: %w", cs[i].ID, err)
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("rebind cornerstone %q: duplicate target ID %q", cs[i].ID, id)
		}
		seen[id] = struct{}{}
		result[i].ID = id
		result[i].WorkID = newWorkID
	}
	return result, nil
}

func (s *Service) runReusableV1(ctx context.Context, record *reusableFlowRecord, values map[string]json.RawMessage, requestID, runHash string) (*ReusableFlowRun, error) {
	createID := requestID + "/work"
	workID := workIDForRequest(createID)
	if existing, _, err := s.store.LoadState(workID, ""); err == nil {
		if existing.ReusableFlowID != record.Flow.ID || existing.ReusableRunHash != runHash {
			return nil, fmt.Errorf("%w: reusable run request %q was reused with different values", ErrWorkRequestIDConflict, requestID)
		}
		run, runErr := s.RunWork(ctx, workID, requestID+"/run")
		if runErr != nil {
			return nil, runErr
		}
		view, loadErr := s.loadView(workID)
		if loadErr != nil {
			return nil, loadErr
		}
		return &ReusableFlowRun{Flow: cloneReusableFlow(record.Flow), Work: view.Work, Run: run, Duplicate: true}, nil
	} else if !errors.Is(err, ErrWorkNotFound) {
		return nil, fmt.Errorf("work: RunReusableFlow: inspect V1 target: %w", err)
	}
	value, err := cloneWork(&record.Template)
	if err != nil {
		return nil, err
	}
	applyReusableV1Values(value, values)
	now := time.Now().UTC()
	value.ID = workID
	blobs, err := s.copySourceBlobs(record.Flow.SourceWorkID)
	if err != nil {
		return nil, fmt.Errorf("work: RunReusableFlow: copy source blobs: %w", err)
	}
	value.Cornerstones, err = rebindCornerstones(value.Cornerstones, workID, blobs)
	if err != nil {
		return nil, fmt.Errorf("work: RunReusableFlow: %w", err)
	}
	value.Name = record.Flow.Name + " - 再次运行"
	value.State = WorkDraft
	value.ArchiveState = ArchiveActive
	value.RerunOf = record.Flow.SourceWorkID
	value.ReusableFlowID = record.Flow.ID
	value.ReusableRunHash = runHash
	value.CreatedAt, value.UpdatedAt = now, now
	for index := range value.Blocks {
		value.Blocks[index].CreatedAt = now
		value.Blocks[index].UpdatedAt = now
	}
	createdPayload, _ := json.Marshal(value)
	definitionPayload, _ := json.Marshal(&value.Definition)
	events := []WorkEvent{
		newServiceEvent(workID, createID+"/created", EventWorkCreated, createdPayload, now),
		newServiceEvent(workID, createID+"/definition", EventDefinitionFrozen, definitionPayload, now),
	}
	if err := s.store.CreateWorkDir(CreateWorkDirInput{RequestID: createID, Work: value, Definition: &value.Definition, Events: events, Blobs: blobs}); err != nil {
		return nil, fmt.Errorf("work: RunReusableFlow: create V1 Work: %w", err)
	}
	run, err := s.RunWork(ctx, workID, requestID+"/run")
	if err != nil {
		return nil, err
	}
	view, err := s.loadView(workID)
	if err != nil {
		return nil, err
	}
	return &ReusableFlowRun{Flow: cloneReusableFlow(record.Flow), Work: view.Work, Run: run}, nil
}

func (s *Service) runReusableV2(ctx context.Context, record *reusableFlowRecord, values map[string]json.RawMessage, requestID, runHash string) (*ReusableFlowRun, error) {
	if record.V2Definition == nil {
		return nil, errors.New("work: RunReusableFlow: saved V2 definition is missing")
	}
	createID := requestID + "/work"
	applyID := requestID + "/apply"
	workID := workIDForRequest(createID)
	definition := cloneDefinitionForView(record.V2Definition)
	now := time.Now().UTC()
	definition.WorkID = workID
	definition.Revision = 1
	definition.ParentRevision = 0
	definition.Status = DefDraft
	definition.CreatedBy = "reusable:" + record.Flow.ID
	definition.CreatedAt = now
	applyReusableV2Values(definition, values)
	digest, err := ComputeV2RevisionDigest(definition)
	if err != nil {
		return nil, err
	}
	definition.Digest = digest
	const applyBaseRevision int64 = 3
	duplicate := false
	if existing, _, loadErr := s.store.LoadState(workID, ""); loadErr == nil {
		if existing.ReusableFlowID != record.Flow.ID || existing.ReusableRunHash != runHash {
			return nil, fmt.Errorf("%w: reusable run request %q was reused with different values", ErrWorkRequestIDConflict, requestID)
		}
		duplicate = true
	} else if !errors.Is(loadErr, ErrWorkNotFound) {
		return nil, fmt.Errorf("work: RunReusableFlow: inspect V2 target: %w", loadErr)
	} else {
		value, cloneErr := cloneWork(&record.Template)
		if cloneErr != nil {
			return nil, cloneErr
		}
		value.ID = workID
		value.Name = record.Flow.Name + " - 再次运行"
		value.SchemaVersion = SchemaVersionV2
		value.State = WorkDraft
		value.ArchiveState = ArchiveActive
		value.RerunOf = record.Flow.SourceWorkID
		value.ReusableFlowID = record.Flow.ID
		value.ReusableRunHash = runHash
		value.Runs = nil
		value.V2CurrentRevision = 0
		value.V2LatestRevision = 1
		value.V2RevisionStates = map[int64]DefinitionStatus{1: DefDraft}
		value.V2Inputs = seedReusableV2Inputs(workID, workflowRunID(workID, applyID), definition, values, now)
		value.CreatedAt, value.UpdatedAt = now, now
		createdPayload, _ := json.Marshal(value)
		definitionPayload, _ := json.Marshal(&value.Definition)
		revisionPayload, _ := json.Marshal(DefRevisionCreatedPayload{
			WorkID: workID, Revision: 1, ParentRevision: 0, Digest: definition.Digest,
			SuggestedName: value.Name,
		})
		revisionEvent := newServiceEventV2(workID, createID+"/revision", EventDefRevisionCreated, revisionPayload, now)
		revisionEvent.Object = ObjectContext{
			Kind: ObjectDefinition, ID: workID, WorkID: workID,
			DefinitionID: workID, DefinitionRevision: int64Ptr(1),
		}
		events := []WorkEvent{
			newServiceEvent(workID, createID+"/created", EventWorkCreated, createdPayload, now),
			newServiceEvent(workID, createID+"/definition", EventDefinitionFrozen, definitionPayload, now),
			revisionEvent,
		}
		if err := s.store.CreateWorkDir(CreateWorkDirInput{
			RequestID: createID, Work: value, Definition: &value.Definition,
			Events: events, V2RevisionBody: definition,
		}); err != nil {
			return nil, fmt.Errorf("work: RunReusableFlow: create V2 Work: %w", err)
		}
	}
	result, err := s.ApplyDefinition(ctx, ApplyDefinitionInput{
		WorkID: workID, Revision: 1, ExpectedRevision: applyBaseRevision, RequestID: applyID,
	})
	if err != nil {
		return nil, err
	}
	var value *Work
	if result != nil && result.View != nil {
		value = result.View.Work
	}
	if value == nil {
		view, loadErr := s.loadView(workID)
		if loadErr != nil {
			return nil, loadErr
		}
		value = view.Work
	}
	var run *WorkflowRun
	if value != nil && len(value.Runs) > 0 {
		run = &value.Runs[len(value.Runs)-1]
	}
	return &ReusableFlowRun{Flow: cloneReusableFlow(record.Flow), Work: value, Run: run, Duplicate: duplicate || result.Duplicate}, nil
}

func seedReusableV2Inputs(workID, runID string, definition *WorkDefinitionRevision, values map[string]json.RawMessage, now time.Time) []WorkInput {
	if definition == nil {
		return nil
	}
	specs := make(map[string]InputSpec, len(definition.InputSpecs))
	for _, spec := range definition.InputSpecs {
		specs[spec.ID] = spec
	}
	var result []WorkInput
	for _, node := range definition.Nodes {
		for _, specID := range node.InputSpecIDs {
			spec, ok := specs[specID]
			value := values["input:"+specID]
			if !ok || spec.Kind == InputApproval || len(value) == 0 || string(value) == "null" {
				continue
			}
			inputID, _ := v2InputIdentity(runID, node.ID, specID)
			result = append(result, WorkInput{
				ID: inputID, WorkID: workID, RunID: runID, TaskID: node.ID,
				BlockID: v2InputBlockID(node), SpecID: specID,
				Value: append(json.RawMessage(nil), value...), State: InputSubmitted,
				Source: "reusable_flow", UpdatedBy: "user", ReadyForStart: true,
				Revision: 1, UpdatedAt: now,
			})
		}
	}
	return result
}

func applyReusableV1Values(value *Work, values map[string]json.RawMessage) {
	if raw := values["prompt"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &value.Prompt)
	}
	if value.Inputs == nil {
		value.Inputs = map[string]any{}
	}
	for key, raw := range values {
		if !strings.HasPrefix(key, "input:") {
			continue
		}
		var decoded any
		if json.Unmarshal(raw, &decoded) == nil {
			value.Inputs[strings.TrimPrefix(key, "input:")] = decoded
		}
	}
}

func applyReusableV2Values(definition *WorkDefinitionRevision, values map[string]json.RawMessage) {
	if raw := values["goal"]; len(raw) > 0 {
		_ = json.Unmarshal(raw, &definition.Goal)
	}
	for index := range definition.InputSpecs {
		if raw := values["input:"+definition.InputSpecs[index].ID]; len(raw) > 0 {
			definition.InputSpecs[index].DefaultValue = append(json.RawMessage(nil), raw...)
		}
	}
}

func normalizeReusableVariableKeys(fields []ReusableField, keys []string) (map[string]bool, error) {
	allowed := make(map[string]bool, len(fields))
	for _, field := range fields {
		allowed[field.Key] = true
	}
	selected := make(map[string]bool, len(keys))
	for _, key := range keys {
		key = strings.TrimSpace(key)
		if !allowed[key] {
			return nil, fmt.Errorf("variable field %q is unavailable", key)
		}
		selected[key] = true
	}
	return selected, nil
}

func reusableRunValues(fields []ReusableField, overrides map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	values := make(map[string]json.RawMessage, len(fields))
	variables := make(map[string]ReusableField, len(fields))
	for _, field := range fields {
		values[field.Key] = append(json.RawMessage(nil), field.Value...)
		if field.Variable {
			variables[field.Key] = field
		}
	}
	for key, value := range overrides {
		field, ok := variables[key]
		if !ok {
			return nil, fmt.Errorf("field %q is fixed by the saved flow", key)
		}
		if !json.Valid(value) {
			return nil, fmt.Errorf("field %q is not valid JSON", key)
		}
		values[key] = append(json.RawMessage(nil), value...)
		_ = field
	}
	for key, field := range variables {
		value := values[key]
		if field.Required && (len(value) == 0 || string(value) == "null" || string(value) == `""`) {
			return nil, fmt.Errorf("field %q is required", key)
		}
	}
	return values, nil
}

func reusableFlowID(sourceWorkID string) string {
	digest := sha256.Sum256([]byte(sourceWorkID))
	return "flow-" + hex.EncodeToString(digest[:12])
}

func reusableFlowDigest(flow ReusableFlow, template *Work, definition *WorkDefinitionRevision) (string, error) {
	flow.Digest = ""
	payload := struct {
		Flow       ReusableFlow            `json:"flow"`
		Template   *Work                   `json:"template"`
		Definition *WorkDefinitionRevision `json:"definition,omitempty"`
	}{flow, template, definition}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func reusableRunHash(flowDigest string, values map[string]json.RawMessage) (string, error) {
	raw, err := json.Marshal(struct {
		Flow   string                     `json:"flow"`
		Values map[string]json.RawMessage `json:"values"`
	}{flowDigest, values})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func cloneReusableFlow(flow ReusableFlow) ReusableFlow {
	flow.Fields = cloneReusableFields(flow.Fields)
	return flow
}

func cloneReusableFields(fields []ReusableField) []ReusableField {
	result := make([]ReusableField, len(fields))
	for index := range fields {
		result[index] = fields[index]
		result[index].Value = append(json.RawMessage(nil), fields[index].Value...)
	}
	return result
}

func reusableFixedItems() []string {
	return []string{"工作结构", "工具与执行方式", "成果格式"}
}

func mustReusableJSON(value any) json.RawMessage {
	raw, _ := json.Marshal(value)
	return raw
}
