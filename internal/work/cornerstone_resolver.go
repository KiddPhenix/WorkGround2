package work

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// CornerstoneResolver is the narrow source port for session_turn,
// workspace_file, artifact, and URL references. Production adapters live
// outside this package; default tests use FakeCornerstoneResolver only.
type CornerstoneResolver interface {
	Resolve(ctx context.Context, ref CornerstoneRef) (ResolveResult, error)
}

// ScopedCornerstoneResolver is implemented by adapters whose source identity
// is scoped to a Work. Artifact IDs are only authoritative inside their owning
// Work, so CornerstoneManager prefers this port when it is available.
type ScopedCornerstoneResolver interface {
	ResolveForWork(ctx context.Context, workID string, ref CornerstoneRef) (ResolveResult, error)
}

// ResolveErrorKind is a stable classification used by retry and readiness
// policy. It must not contain source paths, URLs, or content.
type ResolveErrorKind string

const (
	ResolveErrorChanged ResolveErrorKind = "changed"
	ResolveErrorMissing ResolveErrorKind = "missing"
	ResolveErrorDenied  ResolveErrorKind = "denied"
	ResolveErrorNetwork ResolveErrorKind = "network"
	ResolveErrorInvalid ResolveErrorKind = "invalid"
)

// ResolveResult is the typed output of a resolver. Digest, when supplied, must
// match normalized Content; the manager verifies it before any state update.
type ResolveResult struct {
	Content    string           `json:"content"`
	Digest     string           `json:"digest"`
	Found      bool             `json:"found"`
	Accessible bool             `json:"accessible"`
	ErrorKind  ResolveErrorKind `json:"errorKind,omitempty"`
	Error      string           `json:"error,omitempty"`
}

// ResolverError represents an operational resolver failure. Network failures
// are retryable; context cancellation is returned directly and does not mutate
// Cornerstone state.
type ResolverError struct {
	Kind      ResolveErrorKind
	Retryable bool
	Err       error
}

func (e *ResolverError) Error() string {
	if e == nil || e.Err == nil {
		return "cornerstone resolver failed"
	}
	return e.Err.Error()
}

func (e *ResolverError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// FakeCornerstoneResolver is a concurrency-safe fake with deterministic
// content and bounded or persistent fault injection.
type FakeCornerstoneResolver struct {
	mu        sync.RWMutex
	contents  map[string]string
	faults    map[string]fakeResolverFault
	callCount map[string]int
}

type fakeResolverFault struct {
	kind      ResolveErrorKind
	msg       string
	remaining int
}

// NewFakeCornerstoneResolver creates an empty fake resolver.
func NewFakeCornerstoneResolver() *FakeCornerstoneResolver {
	return &FakeCornerstoneResolver{
		contents:  make(map[string]string),
		faults:    make(map[string]fakeResolverFault),
		callCount: make(map[string]int),
	}
}

func refIdentity(ref CornerstoneRef) string {
	ref = normalizedCornerstoneRef(ref)
	switch ref.Kind {
	case "session_turn":
		return fmt.Sprintf("session_turn:%s:%d", ref.SessionID, ref.Turn)
	case "workspace_file":
		return "workspace_file:" + ref.Path
	case "artifact":
		return "artifact:" + ref.ArtifactID
	case "url":
		return "url:" + ref.URL
	default:
		return "unsupported:" + ref.Kind
	}
}

// SetContent sets the source content for ref.
func (f *FakeCornerstoneResolver) SetContent(ref CornerstoneRef, content string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.contents[refIdentity(ref)] = content
}

// SetFault injects kind for count calls. A count of -1 persists until cleared.
func (f *FakeCornerstoneResolver) SetFault(ref CornerstoneRef, kind ResolveErrorKind, msg string, count int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.faults[refIdentity(ref)] = fakeResolverFault{kind: kind, msg: msg, remaining: count}
}

// ClearFault clears an injected fault.
func (f *FakeCornerstoneResolver) ClearFault(ref CornerstoneRef) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.faults, refIdentity(ref))
}

// CallCount returns Resolve calls for ref.
func (f *FakeCornerstoneResolver) CallCount(ref CornerstoneRef) int {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.callCount[refIdentity(ref)]
}

// Resolve implements CornerstoneResolver without external I/O.
func (f *FakeCornerstoneResolver) Resolve(ctx context.Context, ref CornerstoneRef) (ResolveResult, error) {
	if err := ctx.Err(); err != nil {
		return ResolveResult{}, err
	}
	id := refIdentity(ref)
	f.mu.Lock()
	f.callCount[id]++
	content, found := f.contents[id]
	fault, hasFault := f.faults[id]
	fires := hasFault && (fault.remaining == -1 || fault.remaining > 0)
	if fires && fault.remaining > 0 {
		fault.remaining--
		if fault.remaining == 0 {
			delete(f.faults, id)
		} else {
			f.faults[id] = fault
		}
	} else if hasFault && fault.remaining == 0 {
		delete(f.faults, id)
	}
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return ResolveResult{}, err
	}
	if fires {
		switch fault.kind {
		case ResolveErrorNetwork:
			return ResolveResult{}, &ResolverError{
				Kind:      ResolveErrorNetwork,
				Retryable: true,
				Err:       errors.New(fault.msg),
			}
		case ResolveErrorMissing:
			return ResolveResult{ErrorKind: fault.kind, Error: fault.msg}, nil
		case ResolveErrorDenied:
			return ResolveResult{Found: true, ErrorKind: fault.kind, Error: fault.msg}, nil
		case ResolveErrorInvalid:
			return ResolveResult{Content: content, Found: true, Accessible: true, ErrorKind: fault.kind, Error: fault.msg}, nil
		default:
			return ResolveResult{}, &ResolverError{Kind: ResolveErrorInvalid, Err: errors.New("unsupported fake resolver fault")}
		}
	}
	if !found {
		return ResolveResult{ErrorKind: ResolveErrorMissing, Error: "source is missing"}, nil
	}
	normalized := normalizeCornerstoneContent(content)
	return ResolveResult{
		Content:    normalized,
		Digest:     ContentDigest([]byte(normalized)),
		Found:      true,
		Accessible: true,
	}, nil
}
