package collab

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

const (
	SnapshotFormatVersion     = 1
	DefaultSnapshotChunkBytes = 512 * 1024
	MaxSnapshotChunkBytes     = 4 * 1024 * 1024
	MaxSnapshotBytes          = 512 * 1024 * 1024
	MaxSnapshotChunks         = MaxSnapshotBytes / DefaultSnapshotChunkBytes
	maxCachedSnapshots        = 8
	maxSnapshotCacheBytes     = MaxSnapshotBytes
	snapshotTTL               = 5 * time.Minute
)

// SnapshotManifest describes one immutable, sequence-bound encoded Snapshot.
// Clients must validate every chunk and the root hash before replacing state.
type SnapshotManifest struct {
	Version        int                 `json:"version"`
	SnapshotID     string              `json:"snapshotId"`
	Room           string              `json:"room"`
	BaseSequence   uint64              `json:"baseSequence"`
	Encoding       string              `json:"encoding"`
	TotalBytes     int64               `json:"totalBytes"`
	RootSHA256     string              `json:"rootSha256"`
	ChunkSizeBytes int                 `json:"chunkSizeBytes"`
	Chunks         []SnapshotChunkMeta `json:"chunks"`
	CreatedAt      time.Time           `json:"createdAt" ts_type:"string"`
	ExpiresAt      time.Time           `json:"expiresAt" ts_type:"string"`
}

type SnapshotChunkMeta struct {
	Index  int    `json:"index"`
	Offset int64  `json:"offset"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}

type SnapshotChunk struct {
	SnapshotID string `json:"snapshotId"`
	Index      int    `json:"index"`
	SHA256     string `json:"sha256"`
	Data       []byte `json:"data"`
}

type snapshotBlob struct {
	manifest SnapshotManifest
	data     []byte
	usedAt   time.Time
}

// SnapshotManifest freezes the current public projection at one sequence and
// keeps its canonical JSON available for independently retryable chunk reads.
func (s *Service) SnapshotManifest(ctx context.Context, room, session string) (SnapshotManifest, error) {
	snapshot, err := s.Snapshot(ctx, room, session)
	if err != nil {
		return SnapshotManifest{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return SnapshotManifest{}, fmt.Errorf("collab: encode snapshot: %w", err)
	}
	if len(data) > MaxSnapshotBytes {
		return SnapshotManifest{}, fail(CodeConflict, "snapshot exceeds the chunked transfer limit")
	}
	now := s.now().UTC()
	root := hashSnapshotBytes(data)
	id := "snapshot-" + root
	chunks := make([]SnapshotChunkMeta, 0, (len(data)+DefaultSnapshotChunkBytes-1)/DefaultSnapshotChunkBytes)
	for offset, index := 0, 0; offset < len(data); offset, index = offset+DefaultSnapshotChunkBytes, index+1 {
		end := offset + DefaultSnapshotChunkBytes
		if end > len(data) {
			end = len(data)
		}
		chunks = append(chunks, SnapshotChunkMeta{Index: index, Offset: int64(offset), Size: end - offset, SHA256: hashSnapshotBytes(data[offset:end])})
	}
	if len(chunks) == 0 {
		chunks = append(chunks, SnapshotChunkMeta{Index: 0, SHA256: hashSnapshotBytes(nil)})
	}
	manifest := SnapshotManifest{
		Version: SnapshotFormatVersion, SnapshotID: id, Room: snapshot.Room.ID,
		BaseSequence: snapshot.LatestSequence, Encoding: "json", TotalBytes: int64(len(data)), RootSHA256: root,
		ChunkSizeBytes: DefaultSnapshotChunkBytes, Chunks: chunks, CreatedAt: now, ExpiresAt: now.Add(snapshotTTL),
	}
	s.rememberSnapshot(manifest, data, now)
	return cloneSnapshotManifest(manifest), nil
}

func (s *Service) SnapshotChunk(ctx context.Context, room, session, snapshotID string, index int) (SnapshotChunk, error) {
	if err := ctx.Err(); err != nil {
		return SnapshotChunk{}, err
	}
	room, snapshotID = strings.TrimSpace(room), strings.TrimSpace(snapshotID)
	if room == "" || snapshotID == "" || index < 0 {
		return SnapshotChunk{}, fail(CodeInvalid, "room, snapshotId, and non-negative chunk index are required")
	}
	// Authorize every independently retryable chunk request before consulting
	// the cache so snapshot identifiers never become bearer capabilities.
	s.store.mu.RLock()
	state, ok := s.store.room(room)
	if !ok {
		s.store.mu.RUnlock()
		return SnapshotChunk{}, fail(CodeNotFound, "room does not exist")
	}
	allowed := s.anySessionLocked(state, session)
	s.store.mu.RUnlock()
	if !allowed {
		return SnapshotChunk{}, fail(CodeUnauthorized, "connection session is invalid")
	}

	now := s.now().UTC()
	s.snapshotMu.Lock()
	s.pruneSnapshotsLocked(now)
	blob := s.snapshots[snapshotID]
	if blob == nil || blob.manifest.Room != room {
		s.snapshotMu.Unlock()
		return SnapshotChunk{}, fail(CodeNotFound, "snapshot is unavailable; request a new manifest")
	}
	if index >= len(blob.manifest.Chunks) {
		s.snapshotMu.Unlock()
		return SnapshotChunk{}, fail(CodeInvalid, "snapshot chunk index is out of range")
	}
	blob.usedAt = now
	blob.manifest.ExpiresAt = now.Add(snapshotTTL)
	meta := blob.manifest.Chunks[index]
	start, end := int(meta.Offset), int(meta.Offset)+meta.Size
	data := append([]byte(nil), blob.data[start:end]...)
	s.snapshotMu.Unlock()
	return SnapshotChunk{SnapshotID: snapshotID, Index: index, SHA256: meta.SHA256, Data: data}, nil
}

func (s *Service) rememberSnapshot(manifest SnapshotManifest, data []byte, now time.Time) {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	s.pruneSnapshotsLocked(now)
	if existing := s.snapshots[manifest.SnapshotID]; existing != nil {
		existing.usedAt = now
		existing.manifest.ExpiresAt = manifest.ExpiresAt
		return
	}
	for len(s.snapshots) >= maxCachedSnapshots || s.snapshotCacheBytesLocked()+len(data) > maxSnapshotCacheBytes {
		if !s.evictOldestSnapshotLocked() {
			break
		}
	}
	// data is a fresh json.Marshal result owned by this immutable cache entry.
	s.snapshots[manifest.SnapshotID] = &snapshotBlob{manifest: cloneSnapshotManifest(manifest), data: data, usedAt: now}
}

func (s *Service) pruneSnapshotsLocked(now time.Time) {
	for id, blob := range s.snapshots {
		if !blob.manifest.ExpiresAt.After(now) {
			delete(s.snapshots, id)
		}
	}
}

func (s *Service) evictOldestSnapshotLocked() bool {
	if len(s.snapshots) == 0 {
		return false
	}
	ids := make([]string, 0, len(s.snapshots))
	for id := range s.snapshots {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		a, b := s.snapshots[ids[i]], s.snapshots[ids[j]]
		return a.usedAt.Before(b.usedAt) || a.usedAt.Equal(b.usedAt) && ids[i] < ids[j]
	})
	delete(s.snapshots, ids[0])
	return true
}

func (s *Service) snapshotCacheBytesLocked() int {
	total := 0
	for _, blob := range s.snapshots {
		total += len(blob.data)
	}
	return total
}

func cloneSnapshotManifest(value SnapshotManifest) SnapshotManifest {
	value.Chunks = append([]SnapshotChunkMeta(nil), value.Chunks...)
	return value
}

func hashSnapshotBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
