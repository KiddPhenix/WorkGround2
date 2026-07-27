package work

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

const rerunPlanTTL = 10 * time.Minute

// CopyWork creates an independent Draft while retaining the frozen definition,
// editable blocks, inputs and cornerstone evidence.
func (s *Service) CopyWork(ctx context.Context, input CopyWorkInput) (*Work, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("CopyWork", input.RequestID)
	if err != nil {
		return nil, err
	}
	sourceID := strings.TrimSpace(input.SourceWorkID)
	if sourceID == "" {
		return nil, errors.New("work: CopyWork: sourceWorkId is required")
	}
	source, _, err := s.store.LoadState(sourceID, "")
	if err != nil {
		return nil, fmt.Errorf("work: CopyWork: load source: %w", err)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = source.Name + " - 副本"
	}
	return s.createDerived(ctx, source, source.Definition, name, requestID, sourceID, "", false, nil)
}

// PrepareRerun validates one immutable archive record and keeps the reviewed
// plan in memory until its explicit expiry.
func (s *Service) PrepareRerun(ctx context.Context, input PrepareRerunInput) (*RerunPlan, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	if err := s.requireStore(); err != nil {
		return nil, err
	}
	recordID := strings.TrimSpace(input.RecordID)
	if recordID == "" {
		return nil, errors.New("work: PrepareRerun: recordId is required")
	}
	if input.Mode != RerunOriginalDefinition && input.Mode != RerunLatestDefinition {
		return nil, fmt.Errorf("work: PrepareRerun: unsupported mode %q", input.Mode)
	}
	record, err := s.store.LoadArchive(recordID)
	if err != nil {
		return nil, fmt.Errorf("work: PrepareRerun: load archive: %w", err)
	}
	if record == nil || record.Snapshot.ID == "" {
		return nil, errors.New("work: PrepareRerun: archive has no snapshot")
	}
	source := record.Snapshot.BlueprintRef
	target := source
	targetDefinition := record.Snapshot.Definition
	targetBlocks := append([]BlockInstance(nil), record.Snapshot.Blocks...)
	targetPlacements := append([]BlockPlacement(nil), record.Snapshot.Placements...)
	migrationPath := []int{source.Version}
	upgraded := false
	plan := RerunPlan{
		SourceDefinition: source,
		TargetDefinition: target,
		ExpiresAt:        time.Now().UTC().Add(rerunPlanTTL),
	}
	if input.Mode == RerunLatestDefinition {
		if s.blueprint == nil {
			return nil, errors.New("work: PrepareRerun: BlueprintRegistry is required")
		}
		latest, lookupErr := s.blueprint.LookupLatest(source.ID)
		if lookupErr != nil {
			return nil, lookupErr
		}
		target = BlueprintRef{ID: latest.ID, SchemaVersion: latest.SchemaVersion, Version: latest.Version}
		plan.TargetDefinition = target
		if target != source {
			upgraded = true
			migrationPath = []int{source.Version, target.Version}
			plan.DefinitionDiff = []ChangeSummary{{
				Field: "blueprint.version", Previous: fmt.Sprint(source.Version),
				Current: fmt.Sprint(target.Version), Breaking: true,
			}}
			targetSnapshot, snapshotErr := CreateDefinitionSnapshotWithTools(ctx, latest, record.Snapshot.Inputs, s.tools)
			if snapshotErr != nil {
				plan.Blocking = true
				plan.Warnings = append(plan.Warnings, "最新 Blueprint 无法生成可执行定义："+snapshotErr.Error())
			} else {
				targetDefinition = *targetSnapshot
				migrated, placements, issues, warnings := migrateRerunBlocks(
					record.Snapshot.Blocks,
					record.Snapshot.Definition.BlockSpecs,
					targetSnapshot.BlockSpecs,
					s.blockSchemaRegistry(),
					time.Now().UTC(),
				)
				targetBlocks = migrated
				targetPlacements = placements
				plan.BlockIssues = issues
				plan.Warnings = append(plan.Warnings, warnings...)
				for _, issue := range issues {
					if issue.Blocking {
						plan.Blocking = true
						break
					}
				}
				for _, targetSpec := range targetSnapshot.BlockSpecs {
					sourceSpec := findBlockSpec(record.Snapshot.Definition.BlockSpecs, targetSpec.ID)
					if sourceSpec != nil && sourceSpec.SchemaVersion != targetSpec.SchemaVersion {
						plan.DefinitionDiff = append(plan.DefinitionDiff, ChangeSummary{
							Field:    "block." + targetSpec.ID + ".schemaVersion",
							Previous: fmt.Sprint(sourceSpec.SchemaVersion),
							Current:  fmt.Sprint(targetSpec.SchemaVersion),
							Breaking: false,
						})
					}
				}
			}
		}
	}
	token, err := newRerunToken()
	if err != nil {
		return nil, err
	}
	plan.PlanToken = token
	s.rerunMu.Lock()
	now := time.Now().UTC()
	for key, value := range s.rerunPlans {
		if !value.plan.ExpiresAt.After(now) {
			delete(s.rerunPlans, key)
		}
	}
	s.rerunPlans[token] = preparedRerun{
		record:        record,
		plan:          plan,
		definition:    targetDefinition,
		blocks:        targetBlocks,
		placements:    targetPlacements,
		migrationPath: migrationPath,
		upgraded:      upgraded,
	}
	s.rerunMu.Unlock()
	return &plan, nil
}

// ExecuteRerun creates a new Draft from the exact reviewed archive snapshot.
// A blocking or expired plan remains explicit and can be prepared again.
func (s *Service) ExecuteRerun(ctx context.Context, planToken, requestID string) (*Work, error) {
	if err := checkServiceContext(ctx); err != nil {
		return nil, err
	}
	requestID, err := requireRequestID("ExecuteRerun", requestID)
	if err != nil {
		return nil, err
	}
	planToken = strings.TrimSpace(planToken)
	s.rerunMu.Lock()
	prepared, ok := s.rerunPlans[planToken]
	s.rerunMu.Unlock()
	if !ok {
		return nil, errors.New("work: ExecuteRerun: plan token is unknown or expired; prepare again")
	}
	if !prepared.plan.ExpiresAt.After(time.Now().UTC()) {
		return nil, errors.New("work: ExecuteRerun: plan expired; prepare again")
	}
	if prepared.plan.Blocking {
		return nil, errors.New("work: ExecuteRerun: plan has blocking compatibility issues")
	}
	if prepared.upgraded && s.blueprint != nil {
		latest, err := s.blueprint.LookupLatest(prepared.plan.SourceDefinition.ID)
		if err != nil {
			return nil, fmt.Errorf("work: ExecuteRerun: rerun plan dependency changed: %w", err)
		}
		latestRef := BlueprintRef{ID: latest.ID, SchemaVersion: latest.SchemaVersion, Version: latest.Version}
		if latestRef != prepared.plan.TargetDefinition {
			return nil, errors.New("work: ExecuteRerun: rerun plan is stale; prepare again")
		}
	}
	source, err := cloneWork(&prepared.record.Snapshot)
	if err != nil {
		return nil, fmt.Errorf("work: ExecuteRerun: clone prepared source: %w", err)
	}
	source.Blocks = cloneBlocks(prepared.blocks)
	source.Placements = append([]BlockPlacement(nil), prepared.placements...)
	name := source.Name + " - 重执行"
	return s.createDerived(
		ctx,
		source,
		prepared.definition,
		name,
		requestID,
		"",
		prepared.record.Snapshot.ID,
		prepared.upgraded,
		prepared.migrationPath,
	)
}

func migrateRerunBlocks(
	sourceBlocks []BlockInstance,
	sourceSpecs, targetSpecs []BlockSpec,
	registry *BlockSchemaRegistry,
	now time.Time,
) ([]BlockInstance, []BlockPlacement, []BlockCompatIssue, []string) {
	if registry == nil {
		registry = NewBlockSchemaRegistry()
	}
	sourceByID := make(map[string]BlockInstance, len(sourceBlocks))
	for _, block := range sourceBlocks {
		sourceByID[block.ID] = block
	}
	targetIDs := make(map[string]bool, len(targetSpecs))
	blocks := make([]BlockInstance, 0, len(targetSpecs))
	placements := make([]BlockPlacement, 0, len(targetSpecs))
	var issues []BlockCompatIssue
	var warnings []string

	for _, spec := range targetSpecs {
		targetIDs[spec.ID] = true
		placement := spec.Placement
		placement.BlockID = spec.ID
		placements = append(placements, placement)
		source, found := sourceByID[spec.ID]
		if !found || source.Tombstone {
			initial, _ := buildInitialBlocks([]BlockSpec{spec}, now)
			if len(initial) > 0 {
				blocks = append(blocks, initial[0])
			}
			warnings = append(warnings, fmt.Sprintf("Block %s 在最新定义中重新初始化。", spec.ID))
			continue
		}
		sourceSpec := findBlockSpec(sourceSpecs, spec.ID)
		if source.Kind != spec.Kind || (sourceSpec != nil && sourceSpec.Kind != spec.Kind) {
			issues = append(issues, BlockCompatIssue{
				BlockID: spec.ID, Kind: spec.Kind,
				Problem:  fmt.Sprintf("kind 从 %s 变为 %s，缺少显式转换器", source.Kind, spec.Kind),
				Blocking: true,
			})
			continue
		}
		if source.SchemaVersion > spec.SchemaVersion {
			issues = append(issues, BlockCompatIssue{
				BlockID: spec.ID, Kind: spec.Kind,
				Problem: fmt.Sprintf("不支持 schema 降级 v%d→v%d",
					source.SchemaVersion, spec.SchemaVersion),
				Blocking: true,
			})
			continue
		}
		next := source
		next.Title = spec.Label
		next.CreatedAt = now
		next.UpdatedAt = now
		if source.SchemaVersion < spec.SchemaVersion {
			if source.Status == BlockEmpty {
				initial, _ := buildInitialBlocks([]BlockSpec{spec}, now)
				blocks = append(blocks, initial[0])
				warnings = append(warnings, fmt.Sprintf(
					"Block %s 尚无数据，已按 schema v%d 重新初始化。", spec.ID, spec.SchemaVersion,
				))
				continue
			}
			data, path, err := registry.Migrate(spec.Kind, source.SchemaVersion, spec.SchemaVersion, source.Data)
			if err != nil {
				issues = append(issues, BlockCompatIssue{
					BlockID: spec.ID, Kind: spec.Kind, Problem: err.Error(), Blocking: true,
				})
				continue
			}
			next.Data = data
			next.SchemaVersion = spec.SchemaVersion
			next.Revision++
			warnings = append(warnings, fmt.Sprintf("Block %s schema 已迁移：%v。", spec.ID, path))
		} else if next.Status == BlockReady || next.Status == BlockStale {
			if err := registry.Validate(next.Kind, next.SchemaVersion, next.Data); err != nil {
				issues = append(issues, BlockCompatIssue{
					BlockID: spec.ID, Kind: spec.Kind, Problem: err.Error(), Blocking: true,
				})
				continue
			}
		}
		blocks = append(blocks, next)
	}

	for _, source := range sourceBlocks {
		if targetIDs[source.ID] || source.Tombstone {
			continue
		}
		issues = append(issues, BlockCompatIssue{
			BlockID:  source.ID,
			Kind:     source.Kind,
			Problem:  "最新定义已移除此 Block；原始数据保留在归档中，不复制到新 Work",
			Blocking: false,
		})
	}
	return blocks, sortPlacements(placements), issues, warnings
}

func findBlockSpec(specs []BlockSpec, id string) *BlockSpec {
	for i := range specs {
		if specs[i].ID == id {
			return &specs[i]
		}
	}
	return nil
}

func cloneBlocks(blocks []BlockInstance) []BlockInstance {
	result := make([]BlockInstance, len(blocks))
	for i := range blocks {
		result[i] = blocks[i]
		result[i].Data = append(json.RawMessage(nil), blocks[i].Data...)
		result[i].Actions = append([]BlockActionSpec(nil), blocks[i].Actions...)
		result[i].Fallback = cloneBlockFallback(blocks[i].Fallback)
		if blocks[i].Freshness != nil {
			freshness := *blocks[i].Freshness
			result[i].Freshness = &freshness
		}
	}
	return result
}

func (s *Service) createDerived(
	ctx context.Context,
	source *Work,
	definition WorkDefinitionSnapshot,
	name, requestID, copiedFrom, rerunOf string,
	upgraded bool,
	migrationPath []int,
) (*Work, error) {
	if source == nil {
		return nil, ErrWorkNilInput
	}
	clone, err := cloneWork(source)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	clone.ID = workIDForRequest(requestID)
	clone.Name = name
	clone.State = WorkDraft
	clone.ArchiveState = ArchiveActive
	clone.Definition = definition
	clone.BlueprintRef = definition.BlueprintRef
	clone.Runs = nil
	clone.ActionReceipts = nil
	clone.Conclusions = nil
	clone.CopiedFrom = copiedFrom
	clone.RerunOf = rerunOf
	clone.RerunUpgraded = upgraded
	clone.MigrationPath = append([]int(nil), migrationPath...)
	clone.ArchivedAt = nil
	clone.CreatedAt = now
	clone.UpdatedAt = now
	for i := range clone.Blocks {
		clone.Blocks[i].CreatedAt = now
		clone.Blocks[i].UpdatedAt = now
	}
	createdPayload, err := json.Marshal(clone)
	if err != nil {
		return nil, err
	}
	definitionPayload, err := json.Marshal(&definition)
	if err != nil {
		return nil, err
	}
	blobs, err := s.copySourceBlobs(source.ID)
	if err != nil {
		return nil, err
	}
	events := []WorkEvent{
		newServiceEvent(clone.ID, requestID+"/created", EventWorkCreated, createdPayload, now),
		newServiceEvent(clone.ID, requestID+"/definition", EventDefinitionFrozen, definitionPayload, now),
	}
	if err := s.store.CreateWorkDir(CreateWorkDirInput{
		RequestID: requestID, Work: clone, Definition: &definition, Events: events, Blobs: blobs,
	}); err != nil {
		return nil, fmt.Errorf("work: create derived Work: %w", err)
	}
	view, err := s.loadView(clone.ID)
	if err != nil {
		return nil, err
	}
	if err := s.syncSessionRefs(view.Work, requestID+"/session-refs"); err != nil {
		return nil, committedRecovery("derived-session-refs", clone.ID, requestID, view.Revision, err)
	}
	if err := s.emitSnapshot(view, requestID); err != nil {
		return nil, committedRecovery("derived-view", clone.ID, requestID, view.Revision, err)
	}
	return view.Work, nil
}

func (s *Service) copySourceBlobs(sourceID string) (map[string][]byte, error) {
	blobs, ok := s.store.(BlobStore)
	if !ok {
		return nil, nil
	}
	digests, err := blobs.ListDigests(sourceID)
	if err != nil {
		return nil, fmt.Errorf("work: list source blobs: %w", err)
	}
	result := make(map[string][]byte, len(digests))
	for _, digest := range digests {
		data, getErr := blobs.Get(sourceID, digest)
		if getErr != nil {
			return nil, fmt.Errorf("work: copy source blob %s: %w", digest, getErr)
		}
		result[digest] = data
	}
	return result, nil
}

func newRerunToken() (string, error) {
	var value [24]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("work: create rerun plan token: %w", err)
	}
	return "rerun-" + hex.EncodeToString(value[:]), nil
}
