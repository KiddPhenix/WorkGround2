package work

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const artifactSourceMaxRead = 32 << 20

// ArtifactSourceRequest is the complete authoritative identity of one artifact
// revision. Resolvers must reject partial identities and must not treat any
// field as a wildcard.
type ArtifactSourceRequest struct {
	WorkID             string
	DefinitionRevision int64
	SlotID             string
	SlotRevision       int64
	ArtifactRefID      string
}

// ArtifactSource is an immutable source snapshot returned by a resolver.
// Data is safe for binary artifacts; callers must not mutate it.
type ArtifactSource struct {
	Ref           ArtifactRef
	Data          []byte
	ContentDigest string
	MimeType      string
	Name          string
	SourceKind    string
}

// ArtifactSourceResolver is the narrow, binary-safe production source port
// used by preview and conversion.
type ArtifactSourceResolver interface {
	ResolveArtifactSource(context.Context, ArtifactSourceRequest) (*ArtifactSource, error)
}

// ArtifactSourceCommitValidator re-reads the exact projection ref while the
// FileWorkStore final-commit lease is held. Converters cannot complete through
// a resolver that does not implement this final barrier.
type ArtifactSourceCommitValidator interface {
	ValidateArtifactSource(context.Context, string, ArtifactRef, string) error
}

// StoreArtifactSourceResolver resolves artifacts through the authoritative Work
// projection, BlobStore, and a guarded workspace root.
type StoreArtifactSourceResolver struct {
	store         WorkStore
	blobs         BlobStore
	workspaceRoot string
}

// NewStoreArtifactSourceResolver creates the shared production artifact source
// boundary. Missing dependencies remain fail-closed.
func NewStoreArtifactSourceResolver(store WorkStore, blobs BlobStore, workspaceRoot string) *StoreArtifactSourceResolver {
	root := strings.TrimSpace(workspaceRoot)
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			root = filepath.Clean(abs)
		} else {
			root = ""
		}
	}
	return &StoreArtifactSourceResolver{store: store, blobs: blobs, workspaceRoot: root}
}

// ResolveArtifactSource resolves BlobDigest first, then RelativePath/Path under
// the configured workspace. A declared blob digest never falls back to a path.
func (r *StoreArtifactSourceResolver) ResolveArtifactSource(ctx context.Context, request ArtifactSourceRequest) (*ArtifactSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request.WorkID = strings.TrimSpace(request.WorkID)
	request.SlotID = strings.TrimSpace(request.SlotID)
	request.ArtifactRefID = strings.TrimSpace(request.ArtifactRefID)
	if request.WorkID == "" || request.DefinitionRevision <= 0 || request.SlotID == "" ||
		request.SlotRevision <= 0 || request.ArtifactRefID == "" {
		return nil, errors.New("artifact source: complete artifact identity is required")
	}
	if r == nil || r.store == nil {
		return nil, errors.New("artifact source: authoritative Work store is unavailable")
	}
	projection, err := r.store.LoadProjection(request.WorkID)
	if err != nil {
		return nil, fmt.Errorf("artifact source: load projection: %w", err)
	}
	ref, found := findArtifactRefExact(
		projection,
		request.DefinitionRevision,
		request.SlotID,
		request.SlotRevision,
		request.ArtifactRefID,
	)
	if !found {
		return nil, errors.New("artifact source: authoritative artifact revision changed or is missing")
	}
	switch ref.Status {
	case ArtifactRefStatusMissing, ArtifactRefStatusFailed:
		return nil, fmt.Errorf("artifact source: artifact status is %q", ref.Status)
	}

	data, sourceKind, err := r.resolveRefBytes(ctx, request.WorkID, *ref)
	if err != nil {
		return nil, err
	}
	if len(data) > artifactSourceMaxRead {
		return nil, errors.New("artifact source: source exceeds read limit")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	snapshot := append([]byte(nil), data...)
	name := strings.TrimSpace(ref.Name)
	if name == "" {
		name = filepath.Base(strings.TrimSpace(ref.RelativePath))
	}
	if name == "." || name == "" {
		name = filepath.Base(strings.TrimSpace(ref.Path))
	}
	mimeType := detectArtifactMime(name, snapshot)
	return &ArtifactSource{
		Ref:           *ref,
		Data:          snapshot,
		ContentDigest: ContentDigest(snapshot),
		MimeType:      mimeType,
		Name:          name,
		SourceKind:    sourceKind,
	}, nil
}

// ValidateArtifactSource performs the final source check without loading the
// Work projection again, so FileWorkStore can call it inside its work lease.
func (r *StoreArtifactSourceResolver) ValidateArtifactSource(
	ctx context.Context,
	workID string,
	ref ArtifactRef,
	expectedDigest string,
) error {
	data, _, err := r.resolveRefBytes(ctx, workID, ref)
	if err != nil {
		return err
	}
	if ContentDigest(data) != expectedDigest {
		return errors.New("artifact source: content changed before final commit")
	}
	return nil
}

func (r *StoreArtifactSourceResolver) resolveRefBytes(ctx context.Context, workID string, ref ArtifactRef) ([]byte, string, error) {
	if err := ctx.Err(); err != nil {
		return nil, "", err
	}
	if digest := strings.TrimSpace(ref.BlobDigest); digest != "" {
		if r.blobs == nil {
			return nil, "", errors.New("artifact source: blob resolver is unavailable")
		}
		data, err := r.blobs.Get(workID, digest)
		if err != nil {
			return nil, "", fmt.Errorf("artifact source: read authoritative blob: %w", err)
		}
		if ContentDigest(data) != digest {
			return nil, "", errors.New("artifact source: authoritative blob digest mismatch")
		}
		return data, "blob", nil
	}
	value := strings.TrimSpace(ref.RelativePath)
	sourceKind := "relative_path"
	if value == "" {
		value = strings.TrimSpace(ref.Path)
		sourceKind = "workspace_path"
	}
	if value == "" {
		return nil, "", errors.New("artifact source: artifact has no readable source")
	}
	data, err := r.readWorkspaceBytes(ctx, value)
	if err != nil {
		return nil, "", fmt.Errorf("artifact source: read workspace artifact: %w", err)
	}
	return data, sourceKind, nil
}

func (r *StoreArtifactSourceResolver) readWorkspaceBytes(ctx context.Context, value string) ([]byte, error) {
	if r.workspaceRoot == "" {
		return nil, errors.New("workspace root is unavailable")
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, errors.New("workspace path is empty")
	}
	root, err := filepath.EvalSymlinks(r.workspaceRoot)
	if err != nil {
		return nil, err
	}
	candidate := filepath.FromSlash(value)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return nil, errors.New("workspace path escapes configured root")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("workspace source is not a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, artifactSourceMaxRead+1))
	if err != nil {
		return nil, err
	}
	if len(data) > artifactSourceMaxRead {
		return nil, errors.New("workspace source exceeds read limit")
	}
	return data, ctx.Err()
}

func detectArtifactMime(name string, data []byte) string {
	if mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(name))); mimeType != "" {
		if index := strings.IndexByte(mimeType, ';'); index >= 0 {
			mimeType = mimeType[:index]
		}
		return mimeType
	}
	if len(data) == 0 {
		return ""
	}
	return http.DetectContentType(data)
}

var _ ArtifactSourceResolver = (*StoreArtifactSourceResolver)(nil)
var _ ArtifactSourceCommitValidator = (*StoreArtifactSourceResolver)(nil)
