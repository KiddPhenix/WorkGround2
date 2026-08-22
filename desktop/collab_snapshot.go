package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"workground2/internal/collab"
)

const (
	collaborationSnapshotWorkers = 4
	collaborationChunkAttempts   = 3
)

type collaborationChunkedSnapshotPeer interface {
	SnapshotManifest(context.Context) (collab.SnapshotManifest, error)
	SnapshotChunk(context.Context, string, int) (collab.SnapshotChunk, error)
}

func fetchCollaborationSnapshot(ctx context.Context, peer collaborationPeer) (collab.Snapshot, error) {
	chunked, ok := peer.(collaborationChunkedSnapshotPeer)
	if !ok {
		return peer.Snapshot(ctx)
	}
	for generation := 0; generation < 2; generation++ {
		manifest, err := chunked.SnapshotManifest(ctx)
		if err != nil {
			if snapshotChunksUnsupported(err) {
				return peer.Snapshot(ctx)
			}
			return collab.Snapshot{}, err
		}
		value, err := assembleCollaborationSnapshot(ctx, manifest, chunked.SnapshotChunk)
		if err == nil {
			return value, nil
		}
		if generation == 0 && snapshotGenerationUnavailable(err) {
			continue
		}
		return collab.Snapshot{}, err
	}
	return collab.Snapshot{}, fmt.Errorf("collaboration snapshot generation repeatedly expired")
}

func assembleCollaborationSnapshot(ctx context.Context, manifest collab.SnapshotManifest, fetch func(context.Context, string, int) (collab.SnapshotChunk, error)) (collab.Snapshot, error) {
	if err := validateCollaborationSnapshotManifest(manifest); err != nil {
		return collab.Snapshot{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	staged := make([]byte, int(manifest.TotalBytes))
	jobs := make(chan int)
	errCh := make(chan error, 1)
	var wg sync.WaitGroup
	workers := collaborationSnapshotWorkers
	if workers > len(manifest.Chunks) {
		workers = len(manifest.Chunks)
	}
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for index := range jobs {
				data, err := fetchCollaborationSnapshotChunk(ctx, manifest, index, fetch)
				if err != nil {
					select {
					case errCh <- err:
						cancel()
					default:
					}
					return
				}
				meta := manifest.Chunks[index]
				copy(staged[int(meta.Offset):int(meta.Offset)+meta.Size], data)
			}
		}()
	}
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		defer close(jobs)
		for index := range manifest.Chunks {
			select {
			case <-ctx.Done():
				return
			case jobs <- index:
			}
		}
	}()
	wg.Wait()
	<-feedDone
	select {
	case err := <-errCh:
		return collab.Snapshot{}, err
	default:
	}
	if err := ctx.Err(); err != nil {
		return collab.Snapshot{}, err
	}

	if hashCollaborationSnapshot(staged) != manifest.RootSHA256 {
		return collab.Snapshot{}, fmt.Errorf("collaboration snapshot root hash mismatch")
	}
	var snapshot collab.Snapshot
	if err := json.Unmarshal(staged, &snapshot); err != nil {
		return collab.Snapshot{}, fmt.Errorf("decode collaboration snapshot %s: %w", manifest.SnapshotID, err)
	}
	if snapshot.Room.ID != manifest.Room || snapshot.LatestSequence != manifest.BaseSequence || snapshot.Room.LatestSequence != manifest.BaseSequence {
		return collab.Snapshot{}, fmt.Errorf("collaboration snapshot watermark mismatch")
	}
	return snapshot, nil
}

func validateCollaborationSnapshotManifest(value collab.SnapshotManifest) error {
	if value.Version != collab.SnapshotFormatVersion || value.SnapshotID == "" || value.Room == "" || value.Encoding != "json" {
		return fmt.Errorf("invalid collaboration snapshot manifest identity")
	}
	if value.TotalBytes < 0 || value.TotalBytes > collab.MaxSnapshotBytes || len(value.Chunks) == 0 || len(value.Chunks) > collab.MaxSnapshotChunks {
		return fmt.Errorf("invalid collaboration snapshot manifest size")
	}
	if len(value.RootSHA256) != 64 || value.ChunkSizeBytes <= 0 || value.ChunkSizeBytes > collab.MaxSnapshotChunkBytes {
		return fmt.Errorf("invalid collaboration snapshot manifest checksum or chunk size")
	}
	offset := int64(0)
	for index, chunk := range value.Chunks {
		if chunk.Index != index || chunk.Offset != offset || chunk.Size < 0 || chunk.Size > collab.MaxSnapshotChunkBytes || len(chunk.SHA256) != 64 {
			return fmt.Errorf("invalid collaboration snapshot chunk metadata at %d", index)
		}
		offset += int64(chunk.Size)
	}
	if offset != value.TotalBytes {
		return fmt.Errorf("collaboration snapshot manifest byte total mismatch")
	}
	return nil
}

func fetchCollaborationSnapshotChunk(ctx context.Context, manifest collab.SnapshotManifest, index int, fetch func(context.Context, string, int) (collab.SnapshotChunk, error)) ([]byte, error) {
	meta := manifest.Chunks[index]
	var lastErr error
	for attempt := 0; attempt < collaborationChunkAttempts; attempt++ {
		chunk, err := fetch(ctx, manifest.SnapshotID, index)
		if err == nil {
			switch {
			case chunk.SnapshotID != manifest.SnapshotID || chunk.Index != index:
				err = fmt.Errorf("collaboration snapshot chunk %d identity mismatch", index)
			case len(chunk.Data) != meta.Size:
				err = fmt.Errorf("collaboration snapshot chunk %d size mismatch", index)
			case chunk.SHA256 != meta.SHA256 || hashCollaborationSnapshot(chunk.Data) != meta.SHA256:
				err = fmt.Errorf("collaboration snapshot chunk %d hash mismatch", index)
			default:
				return append([]byte(nil), chunk.Data...), nil
			}
		}
		lastErr = err
		if snapshotGenerationUnavailable(err) || (!collaborationErrorRetryable(err) && !snapshotIntegrityFailure(err)) {
			return nil, err
		}
		if attempt+1 < collaborationChunkAttempts {
			timer := time.NewTimer(time.Duration(attempt+1) * 25 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
	}
	return nil, lastErr
}

func snapshotChunksUnsupported(err error) bool {
	var protocol *collab.Error
	if !errors.As(err, &protocol) {
		return false
	}
	return protocol.Code == collab.CodeNotFound || (protocol.Code == collab.CodeInvalid && strings.Contains(strings.ToLower(protocol.Message), "unsupported"))
}

func snapshotGenerationUnavailable(err error) bool {
	var protocol *collab.Error
	return errors.As(err, &protocol) && protocol.Code == collab.CodeNotFound
}

func snapshotIntegrityFailure(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "snapshot chunk")
}

func hashCollaborationSnapshot(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
