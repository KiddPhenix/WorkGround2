package control

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"workground2/internal/tool"
	"workground2/internal/work"
)

const cornerstoneSourceMaxRead = 10 << 20

var (
	// ErrCornerstoneRefOutsideWorkspace is returned when a file source escapes
	// the configured workspace after symlinks are resolved.
	ErrCornerstoneRefOutsideWorkspace = errors.New("cornerstone: file ref is outside the workspace root")
	errCornerstoneSourceTooLarge      = errors.New("cornerstone: source exceeds the read limit")
	errCornerstoneSourceNotRegular    = errors.New("cornerstone: source is not a regular file")
)

// SessionTurnLookup resolves the actual user message at a zero-based turn.
// Implementations must confine session IDs to their authoritative session root.
type SessionTurnLookup interface {
	LookupSessionTurn(ctx context.Context, sessionID string, turn int) (content string, found bool, err error)
}

// LiveCornerstoneResolverOptions contains the production source adapters. All
// dependencies are narrow and read-only; a missing dependency fails explicitly.
type LiveCornerstoneResolverOptions struct {
	WorkspaceRoot string
	SessionTurns  SessionTurnLookup
	WorkStore     work.WorkStore
	BlobStore     work.BlobStore
	URLTool       tool.Tool
}

// LiveCornerstoneResolver resolves every persisted live_ref kind through the
// same production stores and guarded tools used by the Controller.
type LiveCornerstoneResolver struct {
	workspaceRoot   string
	sessionTurns    SessionTurnLookup
	workStore       work.WorkStore
	blobStore       work.BlobStore
	urlTool         tool.Tool
	artifactSources *work.StoreArtifactSourceResolver
}

// NewLiveCornerstoneResolver creates the production resolver. Configuration is
// completed during boot before the Controller is returned to callers.
func NewLiveCornerstoneResolver(opts LiveCornerstoneResolverOptions) *LiveCornerstoneResolver {
	root := strings.TrimSpace(opts.WorkspaceRoot)
	if root != "" {
		if abs, err := filepath.Abs(root); err == nil {
			root = filepath.Clean(abs)
		}
	}
	return &LiveCornerstoneResolver{
		workspaceRoot:   root,
		sessionTurns:    opts.SessionTurns,
		workStore:       opts.WorkStore,
		blobStore:       opts.BlobStore,
		urlTool:         opts.URLTool,
		artifactSources: work.NewStoreArtifactSourceResolver(opts.WorkStore, opts.BlobStore, root),
	}
}

// ResolveArtifactSource implements the binary-safe Work preview source port.
func (r *LiveCornerstoneResolver) ResolveArtifactSource(
	ctx context.Context,
	request work.ArtifactSourceRequest,
) (*work.ArtifactSource, error) {
	if r == nil || r.artifactSources == nil {
		return nil, errors.New("artifact source resolver is unavailable")
	}
	return r.artifactSources.ResolveArtifactSource(ctx, request)
}

// ValidateArtifactSource performs the final source check without recursively
// loading the Work projection while FileWorkStore holds its commit lease.
func (r *LiveCornerstoneResolver) ValidateArtifactSource(
	ctx context.Context,
	workID string,
	ref work.ArtifactRef,
	expectedDigest string,
) error {
	if r == nil || r.artifactSources == nil {
		return errors.New("artifact source resolver is unavailable")
	}
	return r.artifactSources.ValidateArtifactSource(ctx, workID, ref, expectedDigest)
}

// Resolve implements work.CornerstoneResolver for non-Work-scoped sources.
func (r *LiveCornerstoneResolver) Resolve(ctx context.Context, ref work.CornerstoneRef) (work.ResolveResult, error) {
	return r.resolve(ctx, "", ref)
}

// ResolveForWork implements work.ScopedCornerstoneResolver. Artifact IDs are
// resolved only from the owning Work projection supplied by CornerstoneManager.
func (r *LiveCornerstoneResolver) ResolveForWork(ctx context.Context, workID string, ref work.CornerstoneRef) (work.ResolveResult, error) {
	return r.resolve(ctx, strings.TrimSpace(workID), ref)
}

func (r *LiveCornerstoneResolver) resolve(ctx context.Context, workID string, ref work.CornerstoneRef) (work.ResolveResult, error) {
	if err := ctx.Err(); err != nil {
		return work.ResolveResult{}, err
	}
	switch ref.Kind {
	case "workspace_file":
		return r.resolveWorkspaceFile(ctx, ref.Path)
	case "session_turn":
		return r.resolveSessionTurn(ctx, ref)
	case "artifact":
		return r.resolveArtifact(ctx, workID, ref.ArtifactID)
	case "url":
		return r.resolveURL(ctx, ref.URL)
	case "inline":
		return invalidSource("inline refs do not have an external source"), nil
	default:
		return invalidSource("unsupported cornerstone source kind"), nil
	}
}

func (r *LiveCornerstoneResolver) resolveWorkspaceFile(ctx context.Context, path string) (work.ResolveResult, error) {
	content, err := r.readWorkspaceFile(ctx, path, false)
	if err == nil {
		return resolvedSource(content), nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return work.ResolveResult{}, err
	}
	return fileResolveFailure(err), nil
}

func (r *LiveCornerstoneResolver) resolveSessionTurn(ctx context.Context, ref work.CornerstoneRef) (work.ResolveResult, error) {
	if r.sessionTurns == nil {
		return missingSource("session source resolver is unavailable"), nil
	}
	content, found, err := r.sessionTurns.LookupSessionTurn(ctx, strings.TrimSpace(ref.SessionID), ref.Turn)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return work.ResolveResult{}, err
		}
		return work.ResolveResult{}, &work.ResolverError{
			Kind:      work.ResolveErrorNetwork,
			Retryable: true,
			Err:       errors.New("session source lookup failed"),
		}
	}
	if !found {
		return missingSource("session turn is missing"), nil
	}
	return resolvedSource(content), nil
}

func (r *LiveCornerstoneResolver) resolveArtifact(ctx context.Context, workID, artifactID string) (work.ResolveResult, error) {
	if workID == "" || r.workStore == nil {
		return invalidSource("artifact source resolver is unavailable"), nil
	}
	projection, err := r.workStore.LoadProjection(workID)
	if err != nil {
		if errors.Is(err, work.ErrWorkNotFound) {
			return missingSource("artifact owner is missing"), nil
		}
		return work.ResolveResult{}, &work.ResolverError{
			Kind:      work.ResolveErrorNetwork,
			Retryable: true,
			Err:       errors.New("artifact projection lookup failed"),
		}
	}
	artifact, found := findWorkArtifact(projection, strings.TrimSpace(artifactID))
	if !found {
		return missingSource("artifact is missing"), nil
	}
	switch artifact.Status {
	case "missing":
		return missingSource("artifact is missing"), nil
	case "failed":
		return invalidSource("artifact generation failed"), nil
	}
	if artifact.BlobDigest != "" {
		if r.blobStore == nil {
			return invalidSource("artifact blob resolver is unavailable"), nil
		}
		content, err := r.blobStore.Get(workID, artifact.BlobDigest)
		if err != nil {
			if errors.Is(err, work.ErrWorkNotFound) || os.IsNotExist(err) {
				return missingSource("artifact blob is missing"), nil
			}
			return invalidSource("artifact blob is corrupt or unreadable"), nil
		}
		return resolvedSource(string(content)), nil
	}
	artifactPath := strings.TrimSpace(artifact.RelativePath)
	if artifactPath == "" {
		artifactPath = strings.TrimSpace(artifact.Path)
	}
	if artifactPath == "" {
		return missingSource("artifact has no readable content"), nil
	}
	content, err := r.readWorkspaceFile(ctx, artifactPath, true)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return work.ResolveResult{}, err
		}
		return fileResolveFailure(err), nil
	}
	return resolvedSource(content), nil
}

func (r *LiveCornerstoneResolver) resolveURL(ctx context.Context, rawURL string) (work.ResolveResult, error) {
	if r.urlTool == nil || !r.urlTool.ReadOnly() {
		return deniedSource("URL source resolution is disabled"), nil
	}
	args, err := json.Marshal(map[string]string{"url": strings.TrimSpace(rawURL)})
	if err != nil {
		return invalidSource("URL source is invalid"), nil
	}
	content, err := r.urlTool.Execute(ctx, args)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return work.ResolveResult{}, err
		}
		if strings.Contains(err.Error(), "refusing to fetch internal address") {
			return deniedSource("URL source was denied by network policy"), nil
		}
		return work.ResolveResult{}, &work.ResolverError{
			Kind:      work.ResolveErrorNetwork,
			Retryable: true,
			Err:       errors.New("URL source fetch failed"),
		}
	}
	return resolvedSource(content), nil
}

func (r *LiveCornerstoneResolver) readWorkspaceFile(ctx context.Context, value string, allowAbsolute bool) (string, error) {
	if r.workspaceRoot == "" {
		return "", os.ErrNotExist
	}
	value = strings.TrimSpace(value)
	if value == "" || filepath.IsAbs(value) && !allowAbsolute {
		return "", ErrCornerstoneRefOutsideWorkspace
	}
	root, err := filepath.EvalSymlinks(r.workspaceRoot)
	if err != nil {
		return "", err
	}
	candidate := filepath.FromSlash(value)
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	candidate, err = filepath.Abs(candidate)
	if err != nil {
		return "", ErrCornerstoneRefOutsideWorkspace
	}
	resolved, err := filepath.EvalSymlinks(candidate)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, resolved)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrCornerstoneRefOutsideWorkspace
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f, err := os.Open(resolved)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", errCornerstoneSourceNotRegular
	}
	content, err := io.ReadAll(io.LimitReader(f, cornerstoneSourceMaxRead+1))
	if err != nil {
		return "", err
	}
	if len(content) > cornerstoneSourceMaxRead {
		return "", errCornerstoneSourceTooLarge
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return string(content), nil
}

func findWorkArtifact(value *work.Work, artifactID string) (work.ArtifactRef, bool) {
	if value == nil || artifactID == "" {
		return work.ArtifactRef{}, false
	}
	for _, conclusion := range value.Conclusions {
		for _, artifact := range conclusion.Artifacts {
			if artifact.ID == artifactID {
				return artifact, true
			}
		}
	}
	for _, run := range value.Runs {
		if run.Conclusion == nil {
			continue
		}
		for _, artifact := range run.Conclusion.Artifacts {
			if artifact.ID == artifactID {
				return artifact, true
			}
		}
	}
	return work.ArtifactRef{}, false
}

func fileResolveFailure(err error) work.ResolveResult {
	switch {
	case errors.Is(err, ErrCornerstoneRefOutsideWorkspace), os.IsPermission(err):
		return deniedSource("file source is outside the workspace or inaccessible")
	case os.IsNotExist(err):
		return missingSource("file source is missing")
	case errors.Is(err, errCornerstoneSourceTooLarge), errors.Is(err, errCornerstoneSourceNotRegular):
		return invalidSource("file source is not a supported regular file")
	default:
		return invalidSource("file source could not be read")
	}
}

func resolvedSource(content string) work.ResolveResult {
	if len(content) > cornerstoneSourceMaxRead {
		return invalidSource("source exceeds the supported size limit")
	}
	return work.ResolveResult{Content: content, Found: true, Accessible: true}
}

func missingSource(message string) work.ResolveResult {
	return work.ResolveResult{ErrorKind: work.ResolveErrorMissing, Error: message}
}

func deniedSource(message string) work.ResolveResult {
	return work.ResolveResult{Found: true, ErrorKind: work.ResolveErrorDenied, Error: message}
}

func invalidSource(message string) work.ResolveResult {
	return work.ResolveResult{Found: true, Accessible: true, ErrorKind: work.ResolveErrorInvalid, Error: message}
}

var (
	_ work.CornerstoneResolver           = (*LiveCornerstoneResolver)(nil)
	_ work.ScopedCornerstoneResolver     = (*LiveCornerstoneResolver)(nil)
	_ work.ArtifactSourceResolver        = (*LiveCornerstoneResolver)(nil)
	_ work.ArtifactSourceCommitValidator = (*LiveCornerstoneResolver)(nil)
)
