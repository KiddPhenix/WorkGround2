package work

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// previewStateStore is the typed persistence boundary used by PreviewService.
// Implementations must serialize read-modify-write operations across instances.
type previewStateStore interface {
	LoadPreviewCache(workID, key string) (*ArtifactPreview, bool, error)
	LoadVisiblePreviewCache(workID, key string) (*ArtifactPreview, bool, error)
	PutPreviewCache(workID, key string, preview *ArtifactPreview) error
	LoadConversionReceipt(workID, requestID string) (*conversionReceipt, bool, error)
	MutateConversionReceipt(
		workID string,
		requestID string,
		mutate func(current *conversionReceipt, found bool) (*conversionReceipt, error),
	) (*conversionReceipt, bool, error)
	ListConversionReceipts(workID string) ([]conversionReceipt, error)
	CommitConversionResult(workID string, commit conversionCommit) (*conversionReceipt, error)
}

type conversionCommit struct {
	Identity      ArtifactSourceRequest
	RequestID     string
	IntentDigest  string
	ContentDigest string
	LeaseOwner    string
	CacheKey      string
	Preview       *ArtifactPreview
	Validate      func(ArtifactRef) error
}

type previewVisibility struct {
	RequestID    string `json:"requestId"`
	IntentDigest string `json:"intentDigest"`
}

type previewCacheEntry struct {
	Preview    json.RawMessage    `json:"preview"`
	Conversion *previewVisibility `json:"conversion,omitempty"`
}

func (s *FileWorkStore) previewCachePath(workID string) (string, error) {
	wp, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(wp, "preview-cache.json"), nil
}

func (s *FileWorkStore) conversionReceiptsPath(workID string) (string, error) {
	wp, err := s.workPath(workID)
	if err != nil {
		return "", err
	}
	return filepath.Join(wp, "conversion-receipts.json"), nil
}

func (s *FileWorkStore) LoadPreviewCache(workID, key string) (preview *ArtifactPreview, found bool, retErr error) {
	return s.loadPreviewCache(workID, key, false)
}

// LoadVisiblePreviewCache returns conversion-derived entries only when the
// durable receipt is completed and binds the same cache key and intent.
func (s *FileWorkStore) LoadVisiblePreviewCache(workID, key string) (preview *ArtifactPreview, found bool, retErr error) {
	return s.loadPreviewCache(workID, key, true)
}

func (s *FileWorkStore) loadPreviewCache(workID, key string, visibleOnly bool) (preview *ArtifactPreview, found bool, retErr error) {
	if key == "" {
		return nil, false, errors.New("work: preview cache key is required")
	}
	retErr = s.withWorkOp(workID, func() error {
		path, err := s.previewCachePath(workID)
		if err != nil {
			return err
		}
		cache, recovered, err := loadPreviewCacheFile(path)
		if err != nil {
			return err
		}
		if recovered {
			return nil
		}
		entry, ok := cache[key]
		if !ok {
			return nil
		}
		value, visibility, _, err := decodePreviewCacheEntry(entry)
		if err != nil {
			delete(cache, key)
			if err := writePreviewCacheFile(path, cache); err != nil {
				return fmt.Errorf("work: clean corrupt preview cache entry %q: %w", key, err)
			}
			return nil
		}
		if visibleOnly {
			if visibility == nil && value.ConversionState == ConversionCompleted {
				return nil
			}
			if visibility != nil {
				receiptsPath, err := s.conversionReceiptsPath(workID)
				if err != nil {
					return err
				}
				receipts, err := loadConversionReceiptsFile(receiptsPath)
				if err != nil {
					return err
				}
				receipt, ok := receipts[visibility.RequestID]
				if !ok || receipt.State != ConversionCompleted ||
					receipt.IntentDigest != visibility.IntentDigest ||
					receipt.ResultDigest != key {
					return nil
				}
			}
		}
		preview = value
		found = true
		return nil
	})
	return preview, found, retErr
}

func (s *FileWorkStore) PutPreviewCache(workID, key string, preview *ArtifactPreview) error {
	if key == "" {
		return errors.New("work: preview cache key is required")
	}
	if preview == nil {
		return errors.New("work: preview cache value is required")
	}
	return s.withWorkOp(workID, func() error {
		path, err := s.previewCachePath(workID)
		if err != nil {
			return err
		}
		cache, _, err := loadPreviewCacheFile(path)
		if err != nil {
			return err
		}
		if existing, ok := cache[key]; ok {
			if _, visibility, _, decodeErr := decodePreviewCacheEntry(existing); decodeErr == nil && visibility != nil {
				return nil
			}
		}
		direct := *preview
		if direct.ConversionState == ConversionCompleted {
			direct.ConversionState = ConversionIdle
		}
		value, err := marshalPreviewCacheEntry(&direct, nil)
		if err != nil {
			return fmt.Errorf("work: marshal preview cache entry %q: %w", key, err)
		}
		cache[key] = value
		return writePreviewCacheFile(path, cache)
	})
}

func decodePreviewCacheEntry(raw json.RawMessage) (*ArtifactPreview, *previewVisibility, bool, error) {
	var entry previewCacheEntry
	if err := json.Unmarshal(raw, &entry); err == nil && len(entry.Preview) > 0 {
		var preview ArtifactPreview
		if err := json.Unmarshal(entry.Preview, &preview); err != nil {
			return nil, nil, false, err
		}
		return &preview, entry.Conversion, false, nil
	}
	var preview ArtifactPreview
	if err := json.Unmarshal(raw, &preview); err != nil {
		return nil, nil, true, err
	}
	return &preview, nil, true, nil
}

func marshalPreviewCacheEntry(preview *ArtifactPreview, visibility *previewVisibility) ([]byte, error) {
	raw, err := json.Marshal(preview)
	if err != nil {
		return nil, err
	}
	return json.Marshal(previewCacheEntry{Preview: raw, Conversion: visibility})
}

// loadPreviewCacheFile treats cache corruption as disposable derived state.
// The corrupt file is removed under the cross-instance work lock so the next
// read is a clean miss. Removal failures remain visible.
func loadPreviewCacheFile(path string) (map[string]json.RawMessage, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]json.RawMessage), false, nil
		}
		return nil, false, fmt.Errorf("work: read preview cache: %w", err)
	}
	var cache map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cache); err != nil {
		if removeErr := os.Remove(path); removeErr != nil && !os.IsNotExist(removeErr) {
			return nil, false, fmt.Errorf("work: remove corrupt preview cache: %w", removeErr)
		}
		return make(map[string]json.RawMessage), true, nil
	}
	if cache == nil {
		cache = make(map[string]json.RawMessage)
	}
	return cache, false, nil
}

func writePreviewCacheFile(path string, cache map[string]json.RawMessage) error {
	data, err := json.Marshal(cache)
	if err != nil {
		return fmt.Errorf("work: marshal preview cache: %w", err)
	}
	if err := writeDerivedFile(path, data, 0o600); err != nil {
		return fmt.Errorf("work: persist preview cache: %w", err)
	}
	return nil
}

func (s *FileWorkStore) LoadConversionReceipt(workID, requestID string) (receipt *conversionReceipt, found bool, retErr error) {
	if requestID == "" {
		return nil, false, errors.New("work: conversion requestID is required")
	}
	retErr = s.withWorkOp(workID, func() error {
		path, err := s.conversionReceiptsPath(workID)
		if err != nil {
			return err
		}
		receipts, err := loadConversionReceiptsFile(path)
		if err != nil {
			return err
		}
		value, ok := receipts[requestID]
		if ok {
			cp := value
			receipt = &cp
			found = true
		}
		return nil
	})
	return receipt, found, retErr
}

func (s *FileWorkStore) MutateConversionReceipt(
	workID string,
	requestID string,
	mutate func(current *conversionReceipt, found bool) (*conversionReceipt, error),
) (receipt *conversionReceipt, found bool, retErr error) {
	if requestID == "" {
		return nil, false, errors.New("work: conversion requestID is required")
	}
	if mutate == nil {
		return nil, false, errors.New("work: conversion receipt mutation is required")
	}
	retErr = s.withWorkOp(workID, func() error {
		path, err := s.conversionReceiptsPath(workID)
		if err != nil {
			return err
		}
		receipts, err := loadConversionReceiptsFile(path)
		if err != nil {
			return err
		}
		current, exists := receipts[requestID]
		var currentPtr *conversionReceipt
		if exists {
			cp := current
			currentPtr = &cp
		}
		next, err := mutate(currentPtr, exists)
		if err != nil {
			return err
		}
		if next == nil {
			delete(receipts, requestID)
			receipt = nil
			found = false
		} else {
			if next.RequestID != requestID {
				return fmt.Errorf("work: conversion receipt requestID changed: %q != %q", next.RequestID, requestID)
			}
			receipts[requestID] = *next
			cp := *next
			receipt = &cp
			found = true
		}
		data, err := json.Marshal(receipts)
		if err != nil {
			return fmt.Errorf("work: marshal conversion receipts: %w", err)
		}
		if err := writeDerivedFile(path, data, 0o600); err != nil {
			return fmt.Errorf("work: persist conversion receipts: %w", err)
		}
		return nil
	})
	return receipt, found, retErr
}

func (s *FileWorkStore) ListConversionReceipts(workID string) (out []conversionReceipt, retErr error) {
	retErr = s.withWorkOp(workID, func() error {
		path, err := s.conversionReceiptsPath(workID)
		if err != nil {
			return err
		}
		receipts, err := loadConversionReceiptsFile(path)
		if err != nil {
			return err
		}
		out = make([]conversionReceipt, 0, len(receipts))
		for _, receipt := range receipts {
			out = append(out, receipt)
		}
		return nil
	})
	return out, retErr
}

// CommitConversionResult is the only transition to completed. It validates the
// exact current artifact projection and source under the cross-instance work
// lease, then writes disposable cache before the authoritative receipt. A
// receipt write failure can leave only an unexposed orphan cache entry.
func (s *FileWorkStore) CommitConversionResult(
	workID string,
	commit conversionCommit,
) (receipt *conversionReceipt, retErr error) {
	if commit.RequestID == "" || commit.IntentDigest == "" || commit.ContentDigest == "" ||
		commit.CacheKey == "" || commit.Preview == nil || commit.Validate == nil {
		return nil, errors.New("work: incomplete conversion commit")
	}
	retErr = s.withWorkOp(workID, func() error {
		wp, err := s.workPath(workID)
		if err != nil {
			return err
		}
		projection, err := s.loadProjection(wp, workID)
		if err != nil {
			return err
		}
		ref, found := findArtifactRefExact(
			projection,
			commit.Identity.DefinitionRevision,
			commit.Identity.SlotID,
			commit.Identity.SlotRevision,
			commit.Identity.ArtifactRefID,
		)
		if !found {
			return errors.New("conversion: artifact revision changed; late result discarded")
		}

		receiptsPath, err := s.conversionReceiptsPath(workID)
		if err != nil {
			return err
		}
		receipts, err := loadConversionReceiptsFile(receiptsPath)
		if err != nil {
			return err
		}
		current, found := receipts[commit.RequestID]
		if !found || current.IntentDigest != commit.IntentDigest ||
			current.WorkID != workID || current.ContentDigest != commit.ContentDigest {
			return errors.New("conversion: receipt changed before final commit")
		}
		if current.State == ConversionCompleted {
			cp := current
			receipt = &cp
			return nil
		}
		if current.State != ConversionPending && current.State != ConversionRunning {
			return fmt.Errorf("conversion: receipt is not committable from state %q", current.State)
		}
		if current.State == ConversionRunning && (commit.LeaseOwner == "" || current.LeaseOwner != commit.LeaseOwner) {
			return errors.New("conversion: final commit lease was lost")
		}

		cachePath, err := s.previewCachePath(workID)
		if err != nil {
			return err
		}
		cache, _, err := loadPreviewCacheFile(cachePath)
		if err != nil {
			return err
		}
		value, err := marshalPreviewCacheEntry(commit.Preview, &previewVisibility{
			RequestID:    commit.RequestID,
			IntentDigest: commit.IntentDigest,
		})
		if err != nil {
			return fmt.Errorf("work: marshal conversion result: %w", err)
		}
		cache[commit.CacheKey] = value
		if err := writePreviewCacheFile(cachePath, cache); err != nil {
			return err
		}

		// This validation intentionally happens after the cache write and while
		// the final Work lease is still held. Failure leaves only an orphan cache.
		if err := commit.Validate(*ref); err != nil {
			return err
		}
		if ref.BlobDigest != "" && ref.BlobDigest != commit.ContentDigest {
			return errors.New("conversion: authoritative blob digest changed before final commit")
		}

		current.State = ConversionCompleted
		current.ResultDigest = commit.CacheKey
		current.Error = ""
		current.LeaseOwner = ""
		current.LeaseUntil = time.Time{}
		current.UpdatedAt = time.Now()
		receipts[commit.RequestID] = current
		data, err := json.Marshal(receipts)
		if err != nil {
			return fmt.Errorf("work: marshal conversion receipts: %w", err)
		}
		if err := writeDerivedFile(receiptsPath, data, 0o600); err != nil {
			return fmt.Errorf("work: persist completed conversion receipt: %w", err)
		}
		cp := current
		receipt = &cp
		return nil
	})
	return receipt, retErr
}

func loadConversionReceiptsFile(path string) (map[string]conversionReceipt, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]conversionReceipt), nil
		}
		return nil, fmt.Errorf("work: read conversion receipts: %w", err)
	}
	var receipts map[string]conversionReceipt
	if err := json.Unmarshal(raw, &receipts); err != nil {
		return nil, fmt.Errorf("work: corrupt conversion receipts require repair: %w", err)
	}
	if receipts == nil {
		receipts = make(map[string]conversionReceipt)
	}
	return receipts, nil
}
