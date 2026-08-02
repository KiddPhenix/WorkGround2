package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/fileutil"
)

const reusableFlowDir = "reusable-flows"

// SaveReusableFlow atomically creates one immutable flow file. A source Work
// owns one stable flow ID; concurrent first saves converge on the first valid
// record instead of producing duplicates.
func (s *FileWorkStore) SaveReusableFlow(record *reusableFlowRecord) (*reusableFlowRecord, error) {
	if record == nil {
		return nil, ErrWorkNilInput
	}
	if err := validateReusableFlowRecord(record); err != nil {
		return nil, err
	}
	s.indexMu.Lock()
	defer s.indexMu.Unlock()
	path, err := s.reusableFlowPath(record.Flow.ID)
	if err != nil {
		return nil, err
	}
	if existing, loadErr := loadReusableFlowFile(path); loadErr == nil {
		if existing.Flow.SourceWorkID != record.Flow.SourceWorkID {
			return nil, fmt.Errorf("%w: reusable flow ID %q belongs to another Work", ErrWorkRequestIDConflict, record.Flow.ID)
		}
		if existing.SaveRequestID == record.SaveRequestID && existing.Flow.Digest != record.Flow.Digest {
			return nil, fmt.Errorf("%w: reusable save request %q was reused with different content", ErrWorkRequestIDConflict, record.SaveRequestID)
		}
		return cloneReusableFlowRecord(existing)
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return nil, loadErr
	}
	raw, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("work: encode reusable flow %s: %w", record.Flow.ID, err)
	}
	if err := fileutil.AtomicWriteFile(path, append(raw, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("work: write reusable flow %s: %w", record.Flow.ID, err)
	}
	return cloneReusableFlowRecord(record)
}

// LoadReusableFlow loads and verifies one immutable flow snapshot.
func (s *FileWorkStore) LoadReusableFlow(flowID string) (*reusableFlowRecord, error) {
	s.indexMu.RLock()
	defer s.indexMu.RUnlock()
	path, err := s.reusableFlowPath(flowID)
	if err != nil {
		return nil, err
	}
	record, err := loadReusableFlowFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return cloneReusableFlowRecord(record)
}

// FindReusableFlowBySource uses the deterministic source-derived ID, avoiding
// an eventually inconsistent secondary index.
func (s *FileWorkStore) FindReusableFlowBySource(sourceWorkID string) (*reusableFlowRecord, error) {
	sourceWorkID = strings.TrimSpace(sourceWorkID)
	if sourceWorkID == "" {
		return nil, errors.New("work: reusable source Work ID is required")
	}
	record, err := s.LoadReusableFlow(reusableFlowID(sourceWorkID))
	if err != nil || record == nil {
		return record, err
	}
	if record.Flow.SourceWorkID != sourceWorkID {
		return nil, fmt.Errorf("%w: reusable flow source mismatch", ErrWorkNeedsRepair)
	}
	return record, nil
}

func (s *FileWorkStore) reusableFlowPath(flowID string) (string, error) {
	flowID = strings.TrimSpace(flowID)
	if !strings.HasPrefix(flowID, "flow-") || len(flowID) != len("flow-")+24 {
		return "", fmt.Errorf("work: invalid reusable flow ID %q", flowID)
	}
	for _, char := range strings.TrimPrefix(flowID, "flow-") {
		if char < '0' || char > '9' && char < 'a' || char > 'f' {
			return "", fmt.Errorf("work: invalid reusable flow ID %q", flowID)
		}
	}
	// The Work index explicitly reserves the blueprints directory. Keeping
	// reusable definitions below it prevents them from being mistaken for Work
	// runtime directories during index recovery.
	root := filepath.Join(s.workDir, "blueprints", reusableFlowDir)
	return filepath.Join(root, flowID+".json"), nil
}

func loadReusableFlowFile(path string) (*reusableFlowRecord, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var record reusableFlowRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return nil, fmt.Errorf("%w: parse reusable flow %s: %v", ErrWorkNeedsRepair, path, err)
	}
	if err := validateReusableFlowRecord(&record); err != nil {
		return nil, fmt.Errorf("%w: invalid reusable flow %s: %v", ErrWorkNeedsRepair, path, err)
	}
	return &record, nil
}

func validateReusableFlowRecord(record *reusableFlowRecord) error {
	if record == nil || record.Flow.SchemaVersion != SchemaVersion {
		return errors.New("work: reusable flow schemaVersion is unsupported")
	}
	if record.Flow.ID != reusableFlowID(record.Flow.SourceWorkID) {
		return errors.New("work: reusable flow identity does not match source Work")
	}
	if strings.TrimSpace(record.Flow.Name) == "" || strings.TrimSpace(record.SaveRequestID) == "" {
		return errors.New("work: reusable flow name and save request are required")
	}
	seen := make(map[string]bool, len(record.Flow.Fields))
	for _, field := range record.Flow.Fields {
		if strings.TrimSpace(field.Key) == "" || strings.TrimSpace(field.Label) == "" || seen[field.Key] {
			return errors.New("work: reusable flow fields require unique keys and labels")
		}
		if len(field.Value) > 0 && !json.Valid(field.Value) {
			return fmt.Errorf("work: reusable field %q has invalid JSON", field.Key)
		}
		seen[field.Key] = true
	}
	digest, err := reusableFlowDigest(record.Flow, &record.Template, record.V2Definition)
	if err != nil {
		return err
	}
	if digest != record.Flow.Digest {
		return ErrWorkDigestMismatch
	}
	return nil
}

func cloneReusableFlowRecord(record *reusableFlowRecord) (*reusableFlowRecord, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	var cloned reusableFlowRecord
	if err := json.Unmarshal(raw, &cloned); err != nil {
		return nil, err
	}
	return &cloned, nil
}
