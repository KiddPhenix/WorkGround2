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
			plan.DefinitionDiff = []ChangeSummary{{
				Field: "blueprint.version", Previous: fmt.Sprint(source.Version),
				Current: fmt.Sprint(target.Version), Breaking: true,
			}}
			plan.Blocking = true
			plan.Warnings = []string{"当前版本尚无 Blueprint 数据迁移器；请使用“原定义重执行”，避免静默降级或丢失 Block 数据。"}
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
	s.rerunPlans[token] = preparedRerun{record: record, plan: plan}
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
	source := &prepared.record.Snapshot
	name := source.Name + " - 重执行"
	return s.createDerived(ctx, source, source.Definition, name, requestID, "", source.ID, false, []int{source.BlueprintRef.Version})
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
