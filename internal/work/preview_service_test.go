package work

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func makeWorkStore(t *testing.T, dir string) *FileWorkStore {
	t.Helper()
	requireFileStoreIntegration(t)
	store, err := NewFileWorkStore(dir, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func makeWorkDir(t *testing.T, store *FileWorkStore, workID string) string {
	t.Helper()
	now := time.Now()
	if err := store.CreateWorkDir(CreateWorkDirInput{
		RequestID: "req-" + workID,
		Work: &Work{
			SchemaVersion: SchemaVersion,
			ID:            workID,
			Name:          workID,
			State:         WorkDraft,
			ArchiveState:  ArchiveActive,
			BlueprintRef:  BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
			CreatedAt:     now,
			UpdatedAt:     now,
		},
	}); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(store.WorkDir(), workID)
}

type previewFixture struct {
	store     *FileWorkStore
	service   *Service
	workID    string
	root      string
	workspace string
	refs      map[string]string
}

func newPreviewFixture(t *testing.T, kinds ...string) previewFixture {
	t.Helper()
	root := t.TempDir()
	workspace := t.TempDir()
	store := makeWorkStore(t, root)
	workID := "preview-work"
	now := time.Now()
	work := &Work{
		SchemaVersion:     SchemaVersionV2,
		ID:                workID,
		Name:              workID,
		State:             WorkDraft,
		ArchiveState:      ArchiveActive,
		BlueprintRef:      BlueprintRef{ID: "blueprint:blank", SchemaVersion: 1, Version: 1},
		CreatedAt:         now,
		UpdatedAt:         now,
		V2ArtifactSlots:   make([]ArtifactSlot, 0, len(kinds)),
		V2CurrentRevision: 1,
		V2LatestRevision:  1,
		V2RevisionStates:  map[int64]DefinitionStatus{1: DefActive},
	}
	paths := make(map[string]string, len(kinds))
	for i, kind := range kinds {
		slotID := "slot-" + string(rune('a'+i))
		artifactID := "artifact-" + string(rune('a'+i))
		ext := kind
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		path := filepath.Join(workspace, "artifacts", workID, artifactID+ext)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		body := []byte("source-" + artifactID)
		switch ext {
		case ".pdf":
			body = []byte("%PDF-1.4\n1 0 obj\n<<>>\nendobj\nxref\n")
		case ".png":
			body = tinyPNG
		}
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
		digest := ContentDigest(body)
		work.V2ArtifactSlots = append(work.V2ArtifactSlots, ArtifactSlot{
			ID:            slotID,
			WorkID:        workID,
			DefinitionRev: 1,
			Title:         artifactID,
			Kind:          strings.TrimPrefix(ext, "."),
			ExpectedCount: 1,
			Required:      true,
			State:         SlotReady,
			Revision:      1,
			ArtifactRefs: []ArtifactRef{{
				ID:         artifactID,
				Name:       filepath.Base(path),
				Type:       strings.TrimPrefix(ext, "."),
				Status:     ArtifactRefStatusAvailable,
				Path:       path,
				BlobDigest: digest,
			}},
		})
		paths[artifactID] = path
	}
	if err := store.CreateWorkDir(CreateWorkDirInput{
		RequestID: "req-" + workID,
		Work:      work,
	}); err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.Put(workID, body); err != nil {
			t.Fatal(err)
		}
	}
	service := NewService(store, nil, ViewSinkDiscard)
	service.SetArtifactSourceResolver(NewStoreArtifactSourceResolver(store, store, workspace))
	return previewFixture{store: store, service: service, workID: workID, root: root, workspace: workspace, refs: paths}
}

func previewRequest(f previewFixture, index int, requestID string) PreviewArtifactRequest {
	return PreviewArtifactRequest{
		WorkID:             f.workID,
		DefinitionRevision: 1,
		SlotID:             "slot-" + string(rune('a'+index)),
		SlotRevision:       1,
		ArtifactRefID:      "artifact-" + string(rune('a'+index)),
		RequestID:          requestID,
	}
}

func conversionRequest(f previewFixture, index int, requestID string) RequestArtifactConversionInput {
	p := previewRequest(f, index, requestID)
	return RequestArtifactConversionInput{
		WorkID:             p.WorkID,
		DefinitionRevision: p.DefinitionRevision,
		SlotID:             p.SlotID,
		SlotRevision:       p.SlotRevision,
		ArtifactRefID:      p.ArtifactRefID,
		RequestID:          requestID,
	}
}

func updateFixtureRef(
	t *testing.T,
	f previewFixture,
	index int,
	requestID string,
	mutate func(*ArtifactRef),
) int64 {
	t.Helper()
	projection, state, err := f.store.LoadState(f.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	slot := projection.V2ArtifactSlots[index]
	ref := slot.ArtifactRefs[0]
	mutate(&ref)
	nextRevision := slot.Revision + 1
	if _, err := f.service.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: f.workID, SlotID: slot.ID, DefinitionRev: slot.DefinitionRev,
		ExpectedRevision: state.Revision, Revision: nextRevision, RequestID: requestID,
		State: SlotReady, Refs: []ArtifactRef{ref},
	}); err != nil {
		t.Fatal(err)
	}
	return nextRevision
}

func waitConversion(t *testing.T, svc *Service, input RequestArtifactConversionInput) *RequestArtifactConversionResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		result, err := svc.RequestArtifactConversion(context.Background(), input)
		if err != nil {
			t.Fatal(err)
		}
		if result.Preview != nil && result.Preview.ConversionState == ConversionCompleted {
			return result
		}
		if result.TransportError != nil && !result.Recoverable {
			return result
		}
		if time.Now().After(deadline) {
			t.Fatalf("conversion %s did not finish: %+v", input.RequestID, result)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type countingConverter struct {
	name      string
	version   string
	target    string
	grade     PreviewGrade
	external  bool
	calls     *atomic.Int64
	block     <-chan struct{}
	fail      bool
	overwrite bool
	state     string
}

func (c *countingConverter) Identity() ConverterIdentity {
	version := c.version
	if version == "" {
		version = "1"
	}
	target := c.target
	if target == "" {
		target = "test-preview"
	}
	return ConverterIdentity{Name: c.name, Version: version, Target: target}
}
func (c *countingConverter) CanConvert(grade PreviewGrade, _ string) bool {
	return grade == c.grade
}
func (c *countingConverter) External() bool { return c.external }
func (c *countingConverter) Convert(_ string, path string, _ string) (*ArtifactPreview, error) {
	c.calls.Add(1)
	if c.block != nil {
		<-c.block
	}
	if c.overwrite {
		_ = os.WriteFile(path, []byte("converter-overwrite"), 0o600)
	}
	if c.fail {
		return nil, errors.New("simulated converter failure")
	}
	return &ArtifactPreview{TextContent: "converted", ConversionState: c.state}, nil
}

type tokenApproval struct {
	want  string
	calls atomic.Int64
	mu    sync.Mutex
	last  ExternalApprovalIntent
}

func (a *tokenApproval) VerifyExternalApproval(intent ExternalApprovalIntent, token string) error {
	a.calls.Add(1)
	a.mu.Lock()
	a.last = intent
	a.mu.Unlock()
	if token != a.want {
		return errors.New("bad approval token")
	}
	return nil
}

func TestGradePDFInline(t *testing.T) {
	if GradeArtifact("r.pdf", "") != PreviewInline {
		t.Fatal("PDF must be inline")
	}
}

func TestGradeOfficeFileCard(t *testing.T) {
	if GradeArtifact("d.docx", "") != PreviewFileCard {
		t.Fatal("DOCX must be a file card")
	}
}

func TestPreviewArtifactProductionCacheHitAndFullIdentity(t *testing.T) {
	f := newPreviewFixture(t, "txt", "txt")
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{name: "count-local", grade: PreviewInline, calls: &calls})

	first, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "preview-a"))
	if err != nil || first.Preview == nil {
		t.Fatalf("first preview: result=%+v err=%v", first, err)
	}
	second, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "preview-a-repeat"))
	if err != nil || second.Preview == nil {
		t.Fatalf("second preview: result=%+v err=%v", second, err)
	}
	if calls.Load() != 1 {
		t.Fatalf("same full identity must hit cache, calls=%d", calls.Load())
	}
	if _, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 1, "preview-b")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("different slot identity must miss cache, calls=%d", calls.Load())
	}
}

func TestPreviewFullIdentityRejectsWildcardsBeforePersistence(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	cases := []struct {
		name   string
		mutate func(*PreviewArtifactRequest)
	}{
		{
			name: "empty-slot-id",
			mutate: func(input *PreviewArtifactRequest) {
				input.SlotID = ""
			},
		},
		{
			name: "zero-slot-revision",
			mutate: func(input *PreviewArtifactRequest) {
				input.SlotRevision = 0
			},
		},
		{
			name: "zero-definition-revision",
			mutate: func(input *PreviewArtifactRequest) {
				input.DefinitionRevision = 0
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			previewInput := previewRequest(f, 0, "preview-invalid-"+tc.name)
			tc.mutate(&previewInput)
			if result, err := f.service.PreviewArtifact(context.Background(), previewInput); err == nil || result.TransportError == nil {
				t.Fatalf("PreviewArtifact accepted wildcard identity: result=%+v err=%v", result, err)
			}

			conversionInput := conversionRequest(f, 0, "conversion-invalid-"+tc.name)
			conversionInput.DefinitionRevision = previewInput.DefinitionRevision
			conversionInput.SlotID = previewInput.SlotID
			conversionInput.SlotRevision = previewInput.SlotRevision
			if result, err := f.service.RequestArtifactConversion(context.Background(), conversionInput); err == nil || result.TransportError == nil {
				t.Fatalf("RequestArtifactConversion accepted wildcard identity: result=%+v err=%v", result, err)
			}
			if _, found, err := f.store.LoadConversionReceipt(f.workID, conversionInput.RequestID); err != nil || found {
				t.Fatalf("invalid service intent persisted: found=%v err=%v", found, err)
			}

			directInput := conversionInput
			directInput.RequestID += "-direct"
			if _, err := f.service.previewSvc.RequestConversion(context.Background(), directInput); err == nil {
				t.Fatal("PreviewService.RequestConversion accepted wildcard identity")
			}
			if _, found, err := f.store.LoadConversionReceipt(f.workID, directInput.RequestID); err != nil || found {
				t.Fatalf("invalid direct intent persisted: found=%v err=%v", found, err)
			}
		})
	}
}

func TestFindArtifactRefExactDoesNotWildcardIdentity(t *testing.T) {
	work := &Work{V2ArtifactSlots: []ArtifactSlot{{
		ID: "slot", DefinitionRev: 1, Revision: 1,
		ArtifactRefs: []ArtifactRef{{ID: "artifact"}},
	}}}
	for _, identity := range []struct {
		defRev  int64
		slotID  string
		slotRev int64
	}{
		{defRev: 0, slotID: "slot", slotRev: 1},
		{defRev: 1, slotID: "", slotRev: 1},
		{defRev: 1, slotID: "slot", slotRev: 0},
	} {
		if _, found := findArtifactRefExact(work, identity.defRev, identity.slotID, identity.slotRev, "artifact"); found {
			t.Fatalf("wildcard identity matched: %+v", identity)
		}
	}
	if ref, found := findArtifactRefExact(work, 1, "slot", 1, "artifact"); !found || ref.ID != "artifact" {
		t.Fatalf("exact identity did not match: ref=%+v found=%v", ref, found)
	}
}

func TestPreviewArtifactDigestChangeMissesCache(t *testing.T) {
	f := newPreviewFixture(t, "txt")
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{name: "digest-local", grade: PreviewInline, calls: &calls})
	if _, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "digest-1")); err != nil {
		t.Fatal(err)
	}
	path := f.refs["artifact-a"]
	if err := os.WriteFile(path, []byte("new authoritative content"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	digest, _ := fileContentDigest(path, info)
	if _, err := f.store.Put(f.workID, []byte("new authoritative content")); err != nil {
		t.Fatal(err)
	}
	projection, state, err := f.store.LoadState(f.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := projection.V2ArtifactSlots[0].ArtifactRefs[0]
	ref.BlobDigest = digest
	if _, err := f.service.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: f.workID, SlotID: "slot-a", DefinitionRev: 1,
		ExpectedRevision: state.Revision, Revision: 2, RequestID: "digest-authority",
		State: SlotReady, Refs: []ArtifactRef{ref},
	}); err != nil {
		t.Fatal(err)
	}
	request := previewRequest(f, 0, "digest-2")
	request.SlotRevision = 2
	if _, err := f.service.PreviewArtifact(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("content digest change must miss cache, calls=%d", calls.Load())
	}
}

func TestPreviewArtifactRejectsPathEscape(t *testing.T) {
	f := newPreviewFixture(t, "txt")
	outside := filepath.Join(filepath.Dir(f.root), "outside-preview.txt")
	if err := os.WriteFile(outside, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(outside) })
	projection, state, _ := f.store.LoadState(f.workID, "")
	ref := projection.V2ArtifactSlots[0].ArtifactRefs[0]
	ref.Path = outside
	ref.BlobDigest = ""
	if _, err := f.service.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: f.workID, SlotID: "slot-a", DefinitionRev: 1,
		ExpectedRevision: state.Revision, Revision: 2, RequestID: "escape-authority",
		State: SlotReady, Refs: []ArtifactRef{ref},
	}); err != nil {
		t.Fatal(err)
	}
	request := previewRequest(f, 0, "escape")
	request.SlotRevision = 2
	result, err := f.service.PreviewArtifact(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview == nil || result.Preview.Grade != PreviewFallback || !strings.Contains(result.Preview.Error, "escapes") {
		t.Fatalf("path escape was not safely degraded: %+v", result)
	}
}

func TestPreviewArtifactConverterFailureLeavesOriginalAvailable(t *testing.T) {
	f := newPreviewFixture(t, "txt")
	original, _ := os.ReadFile(f.refs["artifact-a"])
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "malicious-local", grade: PreviewInline, calls: &calls, fail: true, overwrite: true,
	})
	result, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "preview-fail"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview == nil || result.Preview.Error == "" {
		t.Fatalf("converter failure must be explicit: %+v", result)
	}
	after, _ := os.ReadFile(f.refs["artifact-a"])
	if string(after) != string(original) {
		t.Fatal("converter changed the original artifact")
	}
	projection, err := f.store.LoadProjection(f.workID)
	if err != nil || projection.V2ArtifactSlots[0].State != SlotReady {
		t.Fatalf("original slot availability changed: state=%v err=%v", projection.V2ArtifactSlots[0].State, err)
	}
}

func TestRequestArtifactConversionIsAsyncAndIdempotent(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	release := make(chan struct{})
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "office-local", grade: PreviewFileCard, calls: &calls, block: release,
	})
	input := conversionRequest(f, 0, "async-convert")
	started := time.Now()
	result, err := f.service.RequestArtifactConversion(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > 500*time.Millisecond {
		t.Fatal("conversion request waited for converter execution")
	}
	if !result.Committed || result.Preview == nil || result.Preview.ConversionState != ConversionPending {
		t.Fatalf("durable pending intent not returned: %+v", result)
	}
	duplicate, err := f.service.RequestArtifactConversion(context.Background(), input)
	if err != nil || !duplicate.Duplicate {
		t.Fatalf("same intent must be duplicate-safe: result=%+v err=%v", duplicate, err)
	}
	close(release)
	completed := waitConversion(t, f.service, input)
	if !completed.Committed || completed.Preview == nil || completed.Preview.TextContent != "converted" {
		t.Fatalf("conversion did not complete: %+v", completed)
	}
	if calls.Load() != 1 {
		t.Fatalf("duplicate request repeated conversion, calls=%d", calls.Load())
	}
}

func TestRequestArtifactConversionIntentConflict(t *testing.T) {
	f := newPreviewFixture(t, "docx", "docx")
	f.service.previewSvc.autoStart = false
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{name: "office-local", grade: PreviewFileCard, calls: &calls})
	first := conversionRequest(f, 0, "same-request")
	if _, err := f.service.RequestArtifactConversion(context.Background(), first); err != nil {
		t.Fatal(err)
	}
	conflict := conversionRequest(f, 1, "same-request")
	result, err := f.service.RequestArtifactConversion(context.Background(), conflict)
	if err == nil || result.TransportError == nil || !strings.Contains(result.TransportError.Message, "different intent") {
		t.Fatalf("same requestID with different intent was accepted: result=%+v err=%v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatal("conflicting intent reached converter")
	}
}

func TestConversionRetriesAreBounded(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "always-fails", grade: PreviewFileCard, calls: &calls, fail: true,
	})
	input := conversionRequest(f, 0, "bounded-retry")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	result := waitConversion(t, f.service, input)
	if result.Recoverable || result.TransportError == nil {
		t.Fatalf("retry exhaustion must be explicit and terminal: %+v", result)
	}
	if calls.Load() != int64(maxConversionRetries+1) {
		t.Fatalf("converter calls=%d, want initial + %d bounded retries", calls.Load(), maxConversionRetries)
	}
}

func TestLateConversionResultIsDiscarded(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	release := make(chan struct{})
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "late-local", grade: PreviewFileCard, calls: &calls, block: release,
	})
	input := conversionRequest(f, 0, "late-conversion")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("converter did not start, calls=%d", calls.Load())
	}

	path := f.refs["artifact-a"]
	if err := os.WriteFile(path, []byte("new source while conversion runs"), 0o600); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	digest, _ := fileContentDigest(path, info)
	if _, err := f.store.Put(f.workID, []byte("new source while conversion runs")); err != nil {
		t.Fatal(err)
	}
	projection, state, err := f.store.LoadState(f.workID, "")
	if err != nil {
		t.Fatal(err)
	}
	ref := projection.V2ArtifactSlots[0].ArtifactRefs[0]
	ref.BlobDigest = digest
	if _, err := f.service.UpdateArtifactSlot(context.Background(), UpdateArtifactSlotInput{
		WorkID: f.workID, SlotID: "slot-a", DefinitionRev: 1,
		ExpectedRevision: state.Revision, Revision: 2, RequestID: "late-authority",
		State: SlotReady, Refs: []ArtifactRef{ref},
	}); err != nil {
		t.Fatal(err)
	}
	close(release)

	deadline = time.Now().Add(5 * time.Second)
	for {
		result, err := f.service.RequestArtifactConversion(context.Background(), input)
		if err != nil {
			if strings.Contains(err.Error(), "revision changed") || strings.Contains(err.Error(), "different intent") {
				break
			}
			t.Fatal(err)
		}
		if result.TransportError != nil && !result.Recoverable {
			if !strings.Contains(result.TransportError.Message, "late result") &&
				!strings.Contains(result.TransportError.Message, "revision changed") {
				t.Fatalf("wrong stale error: %+v", result.TransportError)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("late conversion was not rejected: %+v", result)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("late result triggered another conversion, calls=%d", calls.Load())
	}
	receipt, found, err := f.store.LoadConversionReceipt(f.workID, input.RequestID)
	if err != nil || !found || receipt.State == ConversionCompleted {
		t.Fatalf("late result reached completed receipt: receipt=%+v found=%v err=%v", receipt, found, err)
	}
}

func TestExternalConversionRequiresExactApproval(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	var calls atomic.Int64
	external := &countingConverter{name: "office-external", grade: PreviewFileCard, calls: &calls, external: true}
	approval := &tokenApproval{want: "approved"}
	f.service.previewSvc.RegisterConverter(external)
	defaultDenied := conversionRequest(f, 0, "external-default-denied")
	defaultDenied.AllowExternal = true
	defaultDenied.ApprovalToken = "approved"
	if _, err := f.service.RequestArtifactConversion(context.Background(), defaultDenied); err == nil {
		t.Fatal("default approval verifier did not fail closed")
	}
	f.service.previewSvc.SetApprovalVerifier(approval)

	noToken := conversionRequest(f, 0, "external-empty")
	noToken.AllowExternal = true
	if _, err := f.service.RequestArtifactConversion(context.Background(), noToken); err == nil {
		t.Fatal("empty approval token was accepted")
	}
	wrong := conversionRequest(f, 0, "external-wrong")
	wrong.AllowExternal = true
	wrong.ApprovalToken = "wrong"
	if _, err := f.service.RequestArtifactConversion(context.Background(), wrong); err == nil {
		t.Fatal("wrong approval token was accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("unapproved external converter was called")
	}

	approved := conversionRequest(f, 0, "external-approved")
	approved.AllowExternal = true
	approved.ApprovalToken = "approved"
	if _, err := f.service.RequestArtifactConversion(context.Background(), approved); err != nil {
		t.Fatal(err)
	}
	waitConversion(t, f.service, approved)
	if calls.Load() != 1 {
		t.Fatalf("approved external conversion calls=%d", calls.Load())
	}
	approval.mu.Lock()
	approvedIntent := approval.last
	approval.mu.Unlock()
	if approvedIntent.WorkID != approved.WorkID ||
		approvedIntent.ArtifactRefID != approved.ArtifactRefID ||
		approvedIntent.RequestID != approved.RequestID ||
		approvedIntent.ContentDigest == "" ||
		approvedIntent.Converter != external.Identity() ||
		!approvedIntent.AllowExternal {
		t.Fatalf("approval did not bind complete intent: %+v", approvedIntent)
	}
}

func TestMissingConverterDegradesWithoutChangingArtifact(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	original, _ := os.ReadFile(f.refs["artifact-a"])
	input := conversionRequest(f, 0, "missing-converter")
	result, err := f.service.RequestArtifactConversion(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Committed || result.Preview == nil || result.Preview.Grade != PreviewFileCard ||
		result.Preview.CanConvert || result.Preview.ConversionState != ConversionCompleted {
		t.Fatalf("missing converter did not degrade safely: %+v", result)
	}
	after, _ := os.ReadFile(f.refs["artifact-a"])
	if string(after) != string(original) {
		t.Fatal("fallback changed original artifact")
	}
}

func TestConversionRestartPumpUsesRealFileWorkStore(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	var calls atomic.Int64
	converter := &countingConverter{name: "restart-local", grade: PreviewFileCard, calls: &calls}
	f.service.previewSvc.RegisterConverter(converter)
	f.service.previewSvc.autoStart = false
	input := conversionRequest(f, 0, "restart-pending")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}

	store2 := makeWorkStore(t, f.root)
	service2 := NewService(store2, nil, ViewSinkDiscard)
	service2.SetArtifactSourceResolver(NewStoreArtifactSourceResolver(store2, store2, f.workspace))
	service2.previewSvc.RegisterConverter(converter)
	pumped, err := service2.RecoverArtifactConversions(context.Background(), f.workID)
	if err != nil || pumped != 1 {
		t.Fatalf("restart pump=%d err=%v", pumped, err)
	}
	completed := waitConversion(t, service2, input)
	if completed.Preview == nil || completed.Preview.ConversionState != ConversionCompleted || calls.Load() != 1 {
		t.Fatalf("restart recovery failed: result=%+v calls=%d", completed, calls.Load())
	}
}

func TestTwoFileWorkStoresPreserveConcurrentKeysAndSingleClaim(t *testing.T) {
	f := newPreviewFixture(t, "docx", "docx")
	store2 := makeWorkStore(t, f.root)
	service2 := NewService(store2, nil, ViewSinkDiscard)
	service2.SetArtifactSourceResolver(NewStoreArtifactSourceResolver(store2, store2, f.workspace))
	var calls atomic.Int64
	converter := &countingConverter{name: "cross-instance", grade: PreviewFileCard, calls: &calls}
	f.service.previewSvc.RegisterConverter(converter)
	service2.previewSvc.RegisterConverter(converter)
	f.service.previewSvc.autoStart = false
	service2.previewSvc.autoStart = false

	inputA := conversionRequest(f, 0, "cross-a")
	inputB := conversionRequest(f, 1, "cross-b")
	var wg sync.WaitGroup
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		_, err := f.service.RequestArtifactConversion(context.Background(), inputA)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		_, err := service2.RequestArtifactConversion(context.Background(), inputB)
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	pumped, err := service2.RecoverArtifactConversions(context.Background(), f.workID)
	if err != nil || pumped != 2 {
		t.Fatalf("cross-instance pump=%d err=%v", pumped, err)
	}
	waitConversion(t, service2, inputA)
	waitConversion(t, service2, inputB)
	if calls.Load() != 2 {
		t.Fatalf("different keys were lost or duplicated, calls=%d", calls.Load())
	}

	// Two services race the same already-completed request; cache hit means no
	// repeated conversion.
	var race sync.WaitGroup
	race.Add(2)
	go func() { defer race.Done(); _, _ = f.service.RequestArtifactConversion(context.Background(), inputA) }()
	go func() { defer race.Done(); _, _ = service2.RequestArtifactConversion(context.Background(), inputA) }()
	race.Wait()
	if calls.Load() != 2 {
		t.Fatalf("same cross-instance request repeated converter, calls=%d", calls.Load())
	}
}

func TestDerivedWriteFailureIsExplicitAndOriginalSurvives(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	original, _ := os.ReadFile(f.refs["artifact-a"])
	originalWriter := writeDerivedFile
	writeDerivedFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == "preview-cache.json" {
			return errors.New("injected half-write")
		}
		return originalWriter(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = originalWriter })

	result, err := f.service.RequestArtifactConversion(context.Background(), conversionRequest(f, 0, "write-failure"))
	if err == nil || result.TransportError == nil || !result.Committed || !result.Recoverable {
		t.Fatalf("derived write failure not explicit: result=%+v err=%v", result, err)
	}
	after, _ := os.ReadFile(f.refs["artifact-a"])
	if string(after) != string(original) {
		t.Fatal("derived write failure changed original")
	}
}

func TestCompletedReceiptWriteFailureLeavesOnlyUnexposedOrphanCache(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	original, err := os.ReadFile(f.refs["artifact-a"])
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "receipt-write", grade: PreviewFileCard, calls: &calls,
	})
	originalWriter := writeDerivedFile
	writeDerivedFile = func(path string, data []byte, mode os.FileMode) error {
		if filepath.Base(path) == "conversion-receipts.json" &&
			strings.Contains(string(data), `"state":"completed"`) {
			return errors.New("injected completed-receipt write failure")
		}
		return originalWriter(path, data, mode)
	}
	t.Cleanup(func() { writeDerivedFile = originalWriter })

	input := conversionRequest(f, 0, "receipt-write-failure")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		receipt, found, err := f.store.LoadConversionReceipt(f.workID, input.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		if found && receipt.State == ConversionFailed {
			if !strings.Contains(receipt.Error, "completed-receipt write failure") {
				t.Fatalf("partial failure was not explicit: %+v", receipt)
			}
			break
		}
		if found && receipt.State == ConversionCompleted {
			t.Fatalf("failed receipt write exposed completion: %+v", receipt)
		}
		if time.Now().After(deadline) {
			t.Fatalf("receipt failure did not settle: %+v", receipt)
		}
		time.Sleep(10 * time.Millisecond)
	}
	cachePath, _ := f.store.previewCachePath(f.workID)
	cache, _, err := loadPreviewCacheFile(cachePath)
	if err != nil || len(cache) == 0 {
		t.Fatalf("expected disposable orphan cache: entries=%d err=%v", len(cache), err)
	}

	store3 := makeWorkStore(t, f.root)
	service3 := NewService(store3, nil, ViewSinkDiscard)
	service3.SetArtifactSourceResolver(NewStoreArtifactSourceResolver(store3, store3, f.workspace))
	service3.previewSvc.RegisterConverter(&countingConverter{
		name: "receipt-write", grade: PreviewFileCard, calls: &calls,
	})
	previewInput := previewRequest(f, 0, "orphan-preview-after-restart")
	hidden, err := service3.PreviewArtifact(context.Background(), previewInput)
	if err != nil {
		t.Fatal(err)
	}
	if hidden.Preview == nil || hidden.Preview.ConversionState == ConversionCompleted ||
		hidden.Preview.ConversionState != ConversionFailed ||
		!strings.Contains(hidden.Preview.Error, "completed-receipt write failure") {
		t.Fatalf("orphan cache was exposed after restart: %+v", hidden)
	}
	if calls.Load() != 1 {
		t.Fatalf("PreviewArtifact synchronously reran filecard converter: calls=%d", calls.Load())
	}

	writeDerivedFile = originalWriter
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	repaired := waitConversion(t, f.service, input)
	if repaired.Preview == nil || repaired.Preview.ConversionState != ConversionCompleted {
		t.Fatalf("orphan repair did not complete: %+v", repaired)
	}
	visible, err := service3.PreviewArtifact(context.Background(), previewInput)
	if err != nil {
		t.Fatal(err)
	}
	if visible.Preview == nil || visible.Preview.ConversionState != ConversionCompleted {
		t.Fatalf("completed receipt did not expose repaired cache: %+v", visible)
	}
	if calls.Load() != 1 {
		t.Fatalf("repair did not reuse raw orphan cache: calls=%d", calls.Load())
	}
	after, err := os.ReadFile(f.refs["artifact-a"])
	if err != nil || string(after) != string(original) {
		t.Fatalf("partial failure changed original artifact: err=%v", err)
	}
}

func TestCorruptCacheIsCleanedAndRebuilt(t *testing.T) {
	f := newPreviewFixture(t, "txt")
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{name: "cache-local", grade: PreviewInline, calls: &calls})
	if _, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "cache-first")); err != nil {
		t.Fatal(err)
	}
	cachePath, _ := f.store.previewCachePath(f.workID)
	if err := os.WriteFile(cachePath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "cache-rebuild")); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 2 {
		t.Fatalf("corrupt cache was not rebuilt, calls=%d", calls.Load())
	}
	raw, err := os.ReadFile(cachePath)
	if err != nil || !strings.HasPrefix(string(raw), "{") || string(raw) == "{broken" {
		t.Fatalf("cache cleanup failed: %q err=%v", raw, err)
	}
}

func TestVisiblePreviewCacheRejectsTokenlessCompletedEntry(t *testing.T) {
	f := newPreviewFixture(t, "txt")
	var calls atomic.Int64
	converter := &countingConverter{
		name: "tokenless-inline", grade: PreviewInline, calls: &calls,
		state: ConversionCompleted,
	}
	f.service.previewSvc.RegisterConverter(converter)
	digest := ContentDigest([]byte("source-artifact-a"))
	key := previewCacheDigest(
		f.workID, 1, "slot-a", 1, "artifact-a",
		digest, converter.Identity(), false,
	)
	orphan := &ArtifactPreview{
		ArtifactRefID: "artifact-a", WorkID: f.workID,
		ContentDigest: digest, Grade: PreviewInline, MimeType: "text/plain",
		TextContent: "tokenless-orphan", ConversionState: ConversionCompleted,
	}
	entry, err := marshalPreviewCacheEntry(orphan, nil)
	if err != nil {
		t.Fatal(err)
	}
	cachePath, err := f.store.previewCachePath(f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePreviewCacheFile(cachePath, map[string]json.RawMessage{key: entry}); err != nil {
		t.Fatal(err)
	}

	input := previewRequest(f, 0, "reject-tokenless-completed")
	result, err := f.service.PreviewArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview == nil || result.Preview.TextContent != "converted" ||
		result.Preview.ConversionState != ConversionIdle || calls.Load() != 1 {
		t.Fatalf("tokenless completed entry was exposed: result=%+v calls=%d", result, calls.Load())
	}
	repeated, err := f.service.PreviewArtifact(context.Background(), input)
	if err != nil {
		t.Fatal(err)
	}
	if repeated.Preview == nil || repeated.Preview.ConversionState != ConversionIdle ||
		repeated.Preview.TextContent != "converted" || calls.Load() != 1 {
		t.Fatalf("direct inline cache persisted forged completion: result=%+v calls=%d", repeated, calls.Load())
	}
}

func assertFileCardCompletedCacheHidden(t *testing.T, mode string) {
	t.Helper()
	f := newPreviewFixture(t, "docx")
	original, err := os.ReadFile(f.refs["artifact-a"])
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	converter := &countingConverter{
		name: "visibility-filecard", grade: PreviewFileCard, calls: &calls,
	}
	f.service.previewSvc.RegisterConverter(converter)
	digest := ContentDigest(original)
	key := previewCacheDigest(
		f.workID, 1, "slot-a", 1, "artifact-a",
		digest, converter.Identity(), false,
	)
	const (
		requestID    = "visibility-receipt"
		intentDigest = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	)
	resultDigest := key
	if mode == "result-digest" {
		resultDigest = ContentDigest([]byte("different-cache-key"))
	}
	if _, _, err := f.store.MutateConversionReceipt(f.workID, requestID, func(*conversionReceipt, bool) (*conversionReceipt, error) {
		return &conversionReceipt{
			RequestID: requestID, WorkID: f.workID,
			ArtifactRefID: "artifact-a", SlotID: "slot-a",
			SlotRevision: 1, DefinitionRevision: 1,
			ContentDigest: digest, MimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
			ConverterName: converter.Identity().Name, ConverterVersion: converter.Identity().Version,
			ConverterTarget: converter.Identity().Target,
			IntentDigest:    intentDigest, State: ConversionCompleted,
			ResultDigest: resultDigest, UpdatedAt: time.Now(),
		}, nil
	}); err != nil {
		t.Fatal(err)
	}
	cached := &ArtifactPreview{
		ArtifactRefID: "artifact-a", WorkID: f.workID,
		ContentDigest: digest, Grade: PreviewFileCard,
		ConversionState: ConversionCompleted, TextContent: "must-not-be-visible",
	}
	var entry []byte
	switch mode {
	case "wrong-intent":
		entry, err = marshalPreviewCacheEntry(cached, &previewVisibility{
			RequestID: requestID, IntentDigest: ContentDigest([]byte("wrong-intent")),
		})
	case "result-digest":
		entry, err = marshalPreviewCacheEntry(cached, &previewVisibility{
			RequestID: requestID, IntentDigest: intentDigest,
		})
	case "legacy":
		entry, err = json.Marshal(cached)
	default:
		t.Fatalf("unsupported visibility mode %q", mode)
	}
	if err != nil {
		t.Fatal(err)
	}
	cachePath, err := f.store.previewCachePath(f.workID)
	if err != nil {
		t.Fatal(err)
	}
	if err := writePreviewCacheFile(cachePath, map[string]json.RawMessage{key: entry}); err != nil {
		t.Fatal(err)
	}

	result, err := f.service.PreviewArtifact(context.Background(), previewRequest(f, 0, "visibility-preview-"+mode))
	if err != nil {
		t.Fatal(err)
	}
	if result.Preview == nil || result.Preview.ConversionState == ConversionCompleted ||
		result.Preview.Grade != PreviewFileCard || !result.Preview.CanOpen ||
		result.Preview.ConversionState != ConversionFailed ||
		!strings.Contains(result.Preview.Error, "cache is missing") {
		t.Fatalf("%s completed cache was visible: %+v", mode, result)
	}
	if calls.Load() != 0 {
		t.Fatalf("%s cache miss executed optional converter: calls=%d", mode, calls.Load())
	}
	after, err := os.ReadFile(f.refs["artifact-a"])
	if err != nil || string(after) != string(original) {
		t.Fatalf("%s changed original artifact: err=%v", mode, err)
	}
}

func TestPreviewRejectsCompletedCacheWithWrongIntentToken(t *testing.T) {
	assertFileCardCompletedCacheHidden(t, "wrong-intent")
}

func TestPreviewRejectsCompletedCacheWithMismatchedResultDigest(t *testing.T) {
	assertFileCardCompletedCacheHidden(t, "result-digest")
}

func TestPreviewRejectsLegacyRawCompletedCache(t *testing.T) {
	assertFileCardCompletedCacheHidden(t, "legacy")
}

func TestArtifactSourceResolverRealBlobRelativeAndAbsolute(t *testing.T) {
	f := newPreviewFixture(t, "txt", "txt", "txt")
	resolver := NewStoreArtifactSourceResolver(f.store, f.store, f.workspace)

	blobRevision := updateFixtureRef(t, f, 0, "source-blob", func(ref *ArtifactRef) {
		ref.Path = ""
		ref.RelativePath = ""
	})
	relativeRevision := updateFixtureRef(t, f, 1, "source-relative", func(ref *ArtifactRef) {
		ref.BlobDigest = ""
		ref.Path = ""
		relative, err := filepath.Rel(f.workspace, f.refs["artifact-b"])
		if err != nil {
			t.Fatal(err)
		}
		ref.RelativePath = filepath.ToSlash(relative)
	})
	absoluteRevision := updateFixtureRef(t, f, 2, "source-absolute", func(ref *ArtifactRef) {
		ref.BlobDigest = ""
		ref.RelativePath = ""
		ref.Path = f.refs["artifact-c"]
	})

	cases := []struct {
		index      int
		revision   int64
		sourceKind string
	}{
		{0, blobRevision, "blob"},
		{1, relativeRevision, "relative_path"},
		{2, absoluteRevision, "workspace_path"},
	}
	for _, tc := range cases {
		source, err := resolver.ResolveArtifactSource(context.Background(), ArtifactSourceRequest{
			WorkID: f.workID, DefinitionRevision: 1,
			SlotID: "slot-" + string(rune('a'+tc.index)), SlotRevision: tc.revision,
			ArtifactRefID: "artifact-" + string(rune('a'+tc.index)),
		})
		if err != nil {
			t.Fatalf("%s source: %v", tc.sourceKind, err)
		}
		if source.SourceKind != tc.sourceKind || string(source.Data) != "source-"+source.Ref.ID {
			t.Fatalf("%s source mismatch: %+v data=%q", tc.sourceKind, source, source.Data)
		}
	}

	absoluteRequest := previewRequest(f, 2, "absolute-preview-service")
	absoluteRequest.SlotRevision = absoluteRevision
	result, err := f.service.PreviewArtifact(context.Background(), absoluteRequest)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte("source-artifact-c")
	if result.Preview == nil ||
		result.Preview.TextContent != string(want) ||
		result.Preview.ContentDigest != ContentDigest(want) {
		t.Fatalf("absolute workspace preview service chain mismatch: %+v", result)
	}
}

func TestArtifactSourceSymlinkEscapeFailsClosedWithoutPersistence(t *testing.T) {
	for _, tc := range []struct {
		name     string
		relative bool
	}{
		{name: "relative", relative: true},
		{name: "absolute", relative: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newPreviewFixture(t, "txt")
			outsideDir := t.TempDir()
			outside := filepath.Join(outsideDir, "outside.txt")
			if err := os.WriteFile(outside, []byte("outside-secret"), 0o600); err != nil {
				t.Fatal(err)
			}
			link := filepath.Join(f.workspace, tc.name+"-escape.txt")
			if err := os.Symlink(outside, link); err != nil {
				t.Skipf("OS denied file symlink creation; escape test cannot be exercised reliably: %v", err)
			}
			revision := updateFixtureRef(t, f, 0, "symlink-"+tc.name, func(ref *ArtifactRef) {
				ref.BlobDigest = ""
				if tc.relative {
					ref.RelativePath = filepath.Base(link)
					ref.Path = ""
				} else {
					ref.RelativePath = ""
					ref.Path = link
				}
			})
			request := previewRequest(f, 0, "preview-symlink-"+tc.name)
			request.SlotRevision = revision
			result, err := f.service.PreviewArtifact(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if result.Preview == nil || result.Preview.Grade != PreviewFallback ||
				result.Preview.CanOpen ||
				!strings.Contains(result.Preview.Error, "escapes configured root") {
				t.Fatalf("symlink escape did not fail closed: %+v", result)
			}
			for _, pathFn := range []func(string) (string, error){
				f.store.previewCachePath,
				f.store.conversionReceiptsPath,
			} {
				path, err := pathFn(f.workID)
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("failed source wrote derived state %s: %v", path, err)
				}
			}
		})
	}
}

func TestConversionSameRequestChangedWorkspaceDigestConflicts(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	revision := updateFixtureRef(t, f, 0, "digest-workspace", func(ref *ArtifactRef) {
		ref.BlobDigest = ""
		relative, err := filepath.Rel(f.workspace, f.refs["artifact-a"])
		if err != nil {
			t.Fatal(err)
		}
		ref.RelativePath = filepath.ToSlash(relative)
		ref.Path = ""
	})
	f.service.previewSvc.autoStart = false
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "digest-conflict", version: "1", grade: PreviewFileCard, calls: &calls,
	})
	input := conversionRequest(f, 0, "same-digest-request")
	input.SlotRevision = revision
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.refs["artifact-a"], []byte("changed-same-projection"), 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := f.service.RequestArtifactConversion(context.Background(), input)
	if err == nil || result.TransportError == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("changed source reused old intent: result=%+v err=%v", result, err)
	}
	if calls.Load() != 0 {
		t.Fatalf("conflicting source reached converter: %d", calls.Load())
	}
}

func TestConversionSameRequestChangedConverterVersionConflicts(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	f.service.previewSvc.autoStart = false
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "versioned", version: "1", target: "pdf", grade: PreviewFileCard, calls: &calls,
	})
	input := conversionRequest(f, 0, "same-version-request")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "versioned", version: "2", target: "pdf", grade: PreviewFileCard, calls: &calls,
	})
	result, err := f.service.RequestArtifactConversion(context.Background(), input)
	if err == nil || result.TransportError == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("changed converter version reused old intent: result=%+v err=%v", result, err)
	}
}

func TestConversionSameRequestChangedConverterTargetConflicts(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	f.service.previewSvc.autoStart = false
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "targeted", version: "1", target: "pdf", grade: PreviewFileCard, calls: &calls,
	})
	input := conversionRequest(f, 0, "same-target-request")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "targeted", version: "1", target: "html", grade: PreviewFileCard, calls: &calls,
	})
	result, err := f.service.RequestArtifactConversion(context.Background(), input)
	if err == nil || result.TransportError == nil || !strings.Contains(err.Error(), "different intent") {
		t.Fatalf("changed converter target reused old intent: result=%+v err=%v", result, err)
	}
}

func TestConversionFinalBarrierRejectsWorkspaceChange(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	revision := updateFixtureRef(t, f, 0, "barrier-workspace", func(ref *ArtifactRef) {
		ref.BlobDigest = ""
		relative, err := filepath.Rel(f.workspace, f.refs["artifact-a"])
		if err != nil {
			t.Fatal(err)
		}
		ref.RelativePath = filepath.ToSlash(relative)
		ref.Path = ""
	})
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "barrier-local", version: "1", target: "pdf", grade: PreviewFileCard, calls: &calls,
	})
	var once sync.Once
	f.service.previewSvc.beforeCommit = func() {
		once.Do(func() {
			if err := os.WriteFile(f.refs["artifact-a"], []byte("changed-before-final-cas"), 0o600); err != nil {
				t.Errorf("barrier mutation: %v", err)
			}
		})
	}
	input := conversionRequest(f, 0, "barrier-request")
	input.SlotRevision = revision
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		receipt, found, err := f.store.LoadConversionReceipt(f.workID, input.RequestID)
		if err != nil {
			t.Fatal(err)
		}
		if found && receipt.State == ConversionFailed {
			if !strings.Contains(receipt.Error, "content changed") {
				t.Fatalf("wrong final barrier error: %+v", receipt)
			}
			break
		}
		if found && receipt.State == ConversionCompleted {
			t.Fatalf("changed source reached completed: %+v", receipt)
		}
		if time.Now().After(deadline) {
			t.Fatalf("final barrier did not settle: %+v", receipt)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestExpiredRunningConversionRestartPumpUsesRealFileWorkStore(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	var calls atomic.Int64
	converter := &countingConverter{name: "expired-restart", grade: PreviewFileCard, calls: &calls}
	f.service.previewSvc.RegisterConverter(converter)
	f.service.previewSvc.autoStart = false
	input := conversionRequest(f, 0, "expired-running")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.store.MutateConversionReceipt(f.workID, input.RequestID, func(current *conversionReceipt, found bool) (*conversionReceipt, error) {
		if !found {
			return nil, errors.New("missing receipt")
		}
		next := *current
		next.State = ConversionRunning
		next.LeaseOwner = "dead-instance"
		next.LeaseUntil = time.Now().Add(-time.Minute)
		return &next, nil
	}); err != nil {
		t.Fatal(err)
	}
	store2 := makeWorkStore(t, f.root)
	service2 := NewService(store2, nil, ViewSinkDiscard)
	service2.SetArtifactSourceResolver(NewStoreArtifactSourceResolver(store2, store2, f.workspace))
	service2.previewSvc.RegisterConverter(converter)
	pumped, err := service2.RecoverArtifactConversions(context.Background(), f.workID)
	if err != nil || pumped != 1 {
		t.Fatalf("expired restart pump=%d err=%v", pumped, err)
	}
	completed := waitConversion(t, service2, input)
	if completed.Preview == nil || completed.Preview.ConversionState != ConversionCompleted || calls.Load() != 1 {
		t.Fatalf("expired restart recovery failed: result=%+v calls=%d", completed, calls.Load())
	}
}

func TestTwoFileWorkStoresClaimSamePendingOnlyOnce(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	store2 := makeWorkStore(t, f.root)
	service2 := NewService(store2, nil, ViewSinkDiscard)
	service2.SetArtifactSourceResolver(NewStoreArtifactSourceResolver(store2, store2, f.workspace))
	var calls atomic.Int64
	converter := &countingConverter{name: "single-claim", grade: PreviewFileCard, calls: &calls}
	f.service.previewSvc.RegisterConverter(converter)
	service2.previewSvc.RegisterConverter(converter)
	f.service.previewSvc.autoStart = false
	service2.previewSvc.autoStart = false
	input := conversionRequest(f, 0, "single-claim-request")
	if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = f.service.RecoverArtifactConversions(context.Background(), f.workID) }()
	go func() { defer wg.Done(); _, _ = service2.RecoverArtifactConversions(context.Background(), f.workID) }()
	wg.Wait()
	completed := waitConversion(t, service2, input)
	if completed.Preview == nil || completed.Preview.ConversionState != ConversionCompleted || calls.Load() != 1 {
		t.Fatalf("cross-instance claim repeated conversion: result=%+v calls=%d", completed, calls.Load())
	}
}

func TestRecoverArtifactConversionsPumpIsBoundedAndContinues(t *testing.T) {
	f := newPreviewFixture(t, "docx")
	release := make(chan struct{})
	var calls atomic.Int64
	f.service.previewSvc.RegisterConverter(&countingConverter{
		name: "bounded-pump", grade: PreviewFileCard, calls: &calls, block: release,
	})
	f.service.previewSvc.autoStart = false
	for i := 0; i < maxConversionPump+1; i++ {
		input := conversionRequest(f, 0, fmt.Sprintf("bounded-pump-%03d", i))
		if _, err := f.service.RequestArtifactConversion(context.Background(), input); err != nil {
			t.Fatal(err)
		}
	}

	first, err := f.service.RecoverArtifactConversions(context.Background(), f.workID)
	if err != nil || first != maxConversionPump {
		t.Fatalf("first bounded pump=%d want=%d err=%v", first, maxConversionPump, err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() < int64(maxConversionPump) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != int64(maxConversionPump) {
		t.Fatalf("first pump claims=%d want=%d", calls.Load(), maxConversionPump)
	}
	receipts, err := f.store.ListConversionReceipts(f.workID)
	if err != nil {
		t.Fatal(err)
	}
	pending := 0
	for _, receipt := range receipts {
		if receipt.State == ConversionPending {
			pending++
		}
	}
	if pending < 1 {
		t.Fatalf("bounded pump consumed every pending receipt: %+v", receipts)
	}

	second, err := f.service.RecoverArtifactConversions(context.Background(), f.workID)
	if err != nil || second != 1 {
		t.Fatalf("second bounded pump=%d want=1 err=%v", second, err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for calls.Load() < int64(maxConversionPump+1) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != int64(maxConversionPump+1) {
		t.Fatalf("second pump did not continue remaining receipt: calls=%d", calls.Load())
	}
	close(release)

	deadline = time.Now().Add(10 * time.Second)
	for {
		receipts, err = f.store.ListConversionReceipts(f.workID)
		if err != nil {
			t.Fatal(err)
		}
		completed := 0
		for _, receipt := range receipts {
			if receipt.State == ConversionCompleted {
				completed++
			}
		}
		if completed == maxConversionPump+1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("bounded pump conversions did not settle: completed=%d", completed)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

var tinyPNG = []byte{
	0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a,
	0x00, 0x00, 0x00, 0x0d, 0x49, 0x48, 0x44, 0x52,
	0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
	0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4,
	0x89, 0x00, 0x00, 0x00, 0x0d, 0x49, 0x44, 0x41,
	0x54, 0x08, 0xd7, 0x63, 0xf8, 0xcf, 0xc0, 0x00,
	0x00, 0x04, 0x00, 0x01, 0x5c, 0xcd, 0xff, 0x7a,
	0x00, 0x00, 0x00, 0x00, 0x49, 0x45, 0x4e, 0x44,
	0xae, 0x42, 0x60, 0x82,
}
