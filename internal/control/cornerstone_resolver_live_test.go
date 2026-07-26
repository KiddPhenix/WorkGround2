package control

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/work"
	"workground2/internal/work/worktest"
)

type cornerstoneTurnStub struct {
	sessionID string
	turn      int
	content   string
}

func (s *cornerstoneTurnStub) LookupSessionTurn(_ context.Context, sessionID string, turn int) (string, bool, error) {
	s.sessionID = sessionID
	s.turn = turn
	return s.content, true, nil
}

type cornerstoneURLToolStub struct {
	rawURL string
	result string
	err    error
}

func (s *cornerstoneURLToolStub) Name() string        { return "web_fetch" }
func (s *cornerstoneURLToolStub) Description() string { return "test guarded URL fetcher" }
func (s *cornerstoneURLToolStub) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object"}`)
}
func (s *cornerstoneURLToolStub) ReadOnly() bool { return true }
func (s *cornerstoneURLToolStub) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var input struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(args, &input); err != nil {
		return "", err
	}
	s.rawURL = input.URL
	return s.result, s.err
}

type cornerstoneBlobStub struct {
	workID string
	digest string
	data   []byte
}

func (s *cornerstoneBlobStub) Put(string, []byte) (string, error) {
	return "", errors.New("unexpected Put")
}
func (s *cornerstoneBlobStub) Get(workID, digest string) ([]byte, error) {
	s.workID = workID
	s.digest = digest
	return append([]byte(nil), s.data...), nil
}
func (s *cornerstoneBlobStub) Exists(string, string) (bool, error) {
	return false, errors.New("unexpected Exists")
}
func (s *cornerstoneBlobStub) Delete(string, string) error { return errors.New("unexpected Delete") }
func (s *cornerstoneBlobStub) ListDigests(string) ([]string, error) {
	return nil, errors.New("unexpected ListDigests")
}

func TestLiveCornerstoneResolverWorkspaceFileConfinesResolvedSymlinks(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "inside.txt"), []byte("inside"), 0o644); err != nil {
		t.Fatal(err)
	}
	resolver := NewLiveCornerstoneResolver(LiveCornerstoneResolverOptions{WorkspaceRoot: root})
	result, err := resolver.ResolveForWork(t.Context(), "work-a", work.CornerstoneRef{Kind: "workspace_file", Path: "inside.txt"})
	if err != nil || result.Content != "inside" || !result.Found || !result.Accessible {
		t.Fatalf("inside file result = %#v, err=%v", result, err)
	}

	outside := filepath.Join(t.TempDir(), "private.txt")
	if err := os.WriteFile(outside, []byte("private"), 0o644); err != nil {
		t.Fatal(err)
	}
	traversal, err := resolver.ResolveForWork(t.Context(), "work-a", work.CornerstoneRef{
		Kind: "workspace_file",
		Path: filepath.Join("..", filepath.Base(filepath.Dir(outside)), filepath.Base(outside)),
	})
	if err != nil || traversal.ErrorKind != work.ResolveErrorDenied {
		t.Fatalf("traversal result = %#v, err=%v", traversal, err)
	}
	if strings.Contains(traversal.Error, outside) {
		t.Fatalf("denied result leaked source path: %q", traversal.Error)
	}

	link := filepath.Join(root, "escape.txt")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlink unavailable on this host: %v", err)
	}
	symlinked, err := resolver.ResolveForWork(t.Context(), "work-a", work.CornerstoneRef{Kind: "workspace_file", Path: "escape.txt"})
	if err != nil || symlinked.ErrorKind != work.ResolveErrorDenied {
		t.Fatalf("symlink escape result = %#v, err=%v", symlinked, err)
	}
}

func TestLiveCornerstoneResolverUsesActualSessionTurn(t *testing.T) {
	turns := &cornerstoneTurnStub{content: "actual user turn"}
	resolver := NewLiveCornerstoneResolver(LiveCornerstoneResolverOptions{SessionTurns: turns})
	result, err := resolver.ResolveForWork(t.Context(), "work-a", work.CornerstoneRef{
		Kind: "session_turn", SessionID: "session.jsonl", Turn: 3,
	})
	if err != nil || result.Content != "actual user turn" {
		t.Fatalf("session turn result = %#v, err=%v", result, err)
	}
	if turns.sessionID != "session.jsonl" || turns.turn != 3 {
		t.Fatalf("lookup = (%q, %d), want exact ref", turns.sessionID, turns.turn)
	}
}

func TestLiveCornerstoneResolverScopesArtifactToOwningWork(t *testing.T) {
	const digest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	store := &worktest.Store{LoadProjectionFunc: func(workID string) (*work.Work, error) {
		if workID != "work-owner" {
			t.Fatalf("LoadProjection workID = %q", workID)
		}
		return &work.Work{ID: workID, Conclusions: []work.Conclusion{{
			Artifacts: []work.ArtifactRef{{ID: "artifact-1", Status: "available", BlobDigest: digest}},
		}}}, nil
	}}
	blobs := &cornerstoneBlobStub{data: []byte("artifact content")}
	resolver := NewLiveCornerstoneResolver(LiveCornerstoneResolverOptions{WorkStore: store, BlobStore: blobs})
	result, err := resolver.ResolveForWork(t.Context(), "work-owner", work.CornerstoneRef{Kind: "artifact", ArtifactID: "artifact-1"})
	if err != nil || result.Content != "artifact content" {
		t.Fatalf("artifact result = %#v, err=%v", result, err)
	}
	if blobs.workID != "work-owner" || blobs.digest != digest {
		t.Fatalf("blob lookup = (%q, %q)", blobs.workID, blobs.digest)
	}
}

func TestLiveCornerstoneResolverArtifactSourceUsesRealBinaryBlob(t *testing.T) {
	store, err := work.NewFileWorkStore(t.TempDir(), 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	const workID = "binary-artifact-work"
	now := time.Now()
	data := []byte{0x00, 0xff, 0x10, 0x80, 0x42}
	digest := work.ContentDigest(data)
	if err := store.CreateWorkDir(work.CreateWorkDirInput{
		RequestID: "create-binary-artifact",
		Work: &work.Work{
			SchemaVersion: work.SchemaVersionV2,
			ID:            workID,
			Name:          workID,
			State:         work.WorkDraft,
			ArchiveState:  work.ArchiveActive,
			BlueprintRef:  work.BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
			CreatedAt:     now,
			UpdatedAt:     now,
			V2ArtifactSlots: []work.ArtifactSlot{{
				ID: "slot", WorkID: workID, DefinitionRev: 1, Revision: 1,
				State: work.SlotReady, ArtifactRefs: []work.ArtifactRef{{
					ID: "artifact", Name: "artifact.bin", Status: work.ArtifactRefStatusAvailable,
					BlobDigest: digest,
				}},
			}},
			V2CurrentRevision: 1,
			V2LatestRevision:  1,
			V2RevisionStates:  map[int64]work.DefinitionStatus{1: work.DefActive},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(workID, data); err != nil {
		t.Fatal(err)
	}
	resolver := NewLiveCornerstoneResolver(LiveCornerstoneResolverOptions{
		WorkspaceRoot: t.TempDir(),
		WorkStore:     store,
		BlobStore:     store,
	})
	source, err := resolver.ResolveArtifactSource(t.Context(), work.ArtifactSourceRequest{
		WorkID: workID, DefinitionRevision: 1, SlotID: "slot",
		SlotRevision: 1, ArtifactRefID: "artifact",
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.SourceKind != "blob" || source.ContentDigest != digest ||
		string(source.Data) != string(data) {
		t.Fatalf("binary artifact changed: %+v data=%v", source, source.Data)
	}
}

func TestLiveCornerstoneResolverUsesInjectedGuardedURLTool(t *testing.T) {
	fetch := &cornerstoneURLToolStub{result: "status 200 OK\n\nsource content"}
	resolver := NewLiveCornerstoneResolver(LiveCornerstoneResolverOptions{URLTool: fetch})
	result, err := resolver.ResolveForWork(t.Context(), "work-a", work.CornerstoneRef{Kind: "url", URL: "https://example.test/source"})
	if err != nil || result.Content != fetch.result {
		t.Fatalf("URL result = %#v, err=%v", result, err)
	}
	if fetch.rawURL != "https://example.test/source" {
		t.Fatalf("guarded URL tool input = %q", fetch.rawURL)
	}

	fetch.err = errors.New("refusing to fetch internal address 169.254.169.254")
	denied, err := resolver.ResolveForWork(t.Context(), "work-a", work.CornerstoneRef{Kind: "url", URL: "http://169.254.169.254/metadata"})
	if err != nil || denied.ErrorKind != work.ResolveErrorDenied {
		t.Fatalf("SSRF denial = %#v, err=%v", denied, err)
	}
	if strings.Contains(denied.Error, "169.254") {
		t.Fatalf("URL denial leaked target: %q", denied.Error)
	}
}
