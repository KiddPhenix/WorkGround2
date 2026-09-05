package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"workground2/internal/provider"
	"workground2/internal/skill"
)

// touch sets a file's mtime to t. Used by the listing-order test so it
// doesn't have to sleep between Saves.
func touch(path string, t time.Time) error {
	return os.Chtimes(path, t, t)
}

// TestSaveLoadRoundTrip is the contract `WorkGround2 --resume` depends on: a
// session written to disk reloads byte-for-byte, including tool calls and
// reasoning content (which the model wants to keep across resumes for cache
// hits on thinking-mode providers).
func TestSaveLoadRoundTrip(t *testing.T) {
	s := NewSession("you are WorkGround2")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "find the bug"})
	s.Add(provider.Message{
		Role:             provider.RoleAssistant,
		Content:          "Let me check.",
		ReasoningContent: "I should look at main.go first.",
		ToolCalls: []provider.ToolCall{{
			ID: "call_1", Name: "read_file", Arguments: `{"path":"main.go"}`,
		}},
	})
	s.Add(provider.Message{
		Role: provider.RoleTool, Name: "read_file", ToolCallID: "call_1",
		Content: "package main\nfunc main() {}\n",
	})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "It's fine."})

	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	if got, want := len(loaded.Messages), len(s.Messages); got != want {
		t.Fatalf("message count after round-trip = %d, want %d", got, want)
	}
	for i, m := range s.Messages {
		if loaded.Messages[i].Role != m.Role {
			t.Errorf("message %d role mismatch", i)
		}
		if loaded.Messages[i].Content != m.Content {
			t.Errorf("message %d content mismatch", i)
		}
		if loaded.Messages[i].ReasoningContent != m.ReasoningContent {
			t.Errorf("message %d reasoning mismatch", i)
		}
		if len(loaded.Messages[i].ToolCalls) != len(m.ToolCalls) {
			t.Errorf("message %d tool_calls count mismatch", i)
		}
	}
}

// TestSaveSnapshotOwnedRewriteConvergesWithoutRecovery pins the mid-turn
// autosave fix (#5993): the last assistant/tool message grows in place across
// successive snapshots while the same Session still owns the on-disk
// digest/revision. Each snapshot must converge onto the same path as an owned
// rewrite instead of forking a recovery branch every midTurnSnapshotInterval.
func TestSaveSnapshotOwnedRewriteConvergesWithoutRecovery(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	s := NewSession("sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "run"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "partial"})
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("initial SaveSnapshot: %v", err)
	}

	// Simulate streaming: the last assistant message keeps growing in place.
	for _, chunk := range []string{"partial and more", "partial and more still", "partial and more still done"} {
		msgs := s.Snapshot()
		msgs[len(msgs)-1].Content = chunk
		s.Replace(msgs)
		if err := s.SaveSnapshot(path); err != nil {
			t.Fatalf("SaveSnapshot chunk %q: %v", chunk, err)
		}
	}

	// The turn-end save must also converge onto the same path.
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("turn-end SaveSnapshot: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), "-recovery-") {
			t.Fatalf("unexpected recovery branch: %s", e.Name())
		}
	}

	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.Messages[len(loaded.Messages)-1].Content; got != "partial and more still done" {
		t.Fatalf("converged content = %q", got)
	}
}

// TestSaveSnapshotExternalDivergenceStillConflicts guards the other half of the
// fix: a genuine cross-runtime advance (different content written by another
// Session) must still surface ErrSessionSnapshotConflict, never a silent owned
// rewrite.
func TestSaveSnapshotExternalDivergenceStillConflicts(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")

	s := NewSession("sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "run"})
	s.Add(provider.Message{Role: provider.RoleAssistant, Content: "done"})
	if err := s.SaveSnapshot(path); err != nil {
		t.Fatalf("initial SaveSnapshot: %v", err)
	}

	other := NewSession("sys")
	other.Add(provider.Message{Role: provider.RoleUser, Content: "different"})
	if err := other.Save(path); err != nil {
		t.Fatalf("other Save: %v", err)
	}

	msgs := s.Snapshot()
	msgs = append(msgs, provider.Message{Role: provider.RoleAssistant, Content: "continued"})
	s.Replace(msgs)
	if err := s.SaveSnapshot(path); !errors.Is(err, ErrSessionSnapshotConflict) {
		t.Fatalf("SaveSnapshot after external divergence = %v, want ErrSessionSnapshotConflict", err)
	}
}

func TestRecoveryBranchInheritsAndListsParentSource(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "work-task.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "persisted task"})
	if err := parent.Save(parentPath); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	parentMeta, err := EnsureBranchMeta(parentPath)
	if err != nil {
		t.Fatalf("ensure parent meta: %v", err)
	}
	const workSource = "work:work-1/run:run-1/stage:v2-dag/task:task-1/attempt:0/request:req-1"
	parentMeta.SessionSource = workSource
	if err := SaveBranchMetaPreserveUpdated(parentPath, parentMeta); err != nil {
		t.Fatalf("save parent source: %v", err)
	}

	stale := NewSession("sys")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "recovered task"})
	recovery, err := stale.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: parentPath})
	if err != nil {
		t.Fatalf("save recovery branch: %v", err)
	}
	if recovery.Meta.SessionSource != workSource {
		t.Fatalf("recovery source = %q, want inherited %q", recovery.Meta.SessionSource, workSource)
	}

	// Simulate a historical recovery branch that Desktop already migrated to
	// external. Listing must still recover the durable parent source.
	legacyMeta, ok, err := LoadBranchMeta(recovery.Path)
	if err != nil || !ok {
		t.Fatalf("load recovery meta: ok=%v err=%v", ok, err)
	}
	legacyMeta.SessionSource = "external"
	if err := SaveBranchMetaPreserveUpdated(recovery.Path, legacyMeta); err != nil {
		t.Fatalf("save legacy recovery source: %v", err)
	}

	externalPath := filepath.Join(dir, "external.jsonl")
	external := NewSession("sys")
	external.Add(provider.Message{Role: provider.RoleUser, Content: "real external"})
	if err := external.Save(externalPath); err != nil {
		t.Fatalf("save external session: %v", err)
	}
	externalMeta, err := EnsureBranchMeta(externalPath)
	if err != nil {
		t.Fatalf("ensure external meta: %v", err)
	}
	externalMeta.SessionSource = "external"
	if err := SaveBranchMetaPreserveUpdated(externalPath, externalMeta); err != nil {
		t.Fatalf("save external source: %v", err)
	}
	externalRecoveryPath := filepath.Join(dir, "external-recovery.jsonl")
	externalRecovery := NewSession("sys")
	externalRecovery.Add(provider.Message{Role: provider.RoleUser, Content: "real external recovery"})
	if err := externalRecovery.Save(externalRecoveryPath); err != nil {
		t.Fatalf("save external recovery: %v", err)
	}
	externalRecoveryMeta, err := EnsureBranchMeta(externalRecoveryPath)
	if err != nil {
		t.Fatalf("ensure external recovery meta: %v", err)
	}
	externalRecoveryMeta.Recovered = true
	externalRecoveryMeta.ParentID = string(BranchID(externalPath))
	externalRecoveryMeta.SessionSource = "external"
	if err := SaveBranchMetaPreserveUpdated(externalRecoveryPath, externalRecoveryMeta); err != nil {
		t.Fatalf("save external recovery source: %v", err)
	}

	infos, err := ListSessions(dir)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	sources := make(map[string]string, len(infos))
	for _, info := range infos {
		sources[filepath.Clean(info.Path)] = info.SessionSource
	}
	if got := sources[filepath.Clean(recovery.Path)]; got != workSource {
		t.Fatalf("listed legacy recovery source = %q, want %q", got, workSource)
	}
	if got := sources[filepath.Clean(externalPath)]; got != "external" {
		t.Fatalf("genuine external source = %q, want external", got)
	}
	if got := sources[filepath.Clean(externalRecoveryPath)]; got != "external" {
		t.Fatalf("genuine external recovery source = %q, want external", got)
	}
}

func TestSaveRedactsProtectedSkillBody(t *testing.T) {
	protected := skill.Render(skill.Skill{
		Name:        "remote-secret",
		Description: "remote",
		Body:        "REMOTE_SECRET_BODY " + strings.Repeat("do not persist this body ", 8),
		Protected:   true,
		AntiLeak:    true,
	}, "")
	s := NewSession("sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: protected})
	s.Add(provider.Message{Role: provider.RoleTool, Name: "run_skill", ToolCallID: "call_1", Content: protected})

	path := filepath.Join(t.TempDir(), "protected.jsonl")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read saved session: %v", err)
	}
	if strings.Contains(string(raw), "REMOTE_SECRET_BODY") || strings.Contains(string(raw), "do not persist this body") {
		t.Fatalf("saved session leaked protected body:\n%s", raw)
	}
	if !strings.Contains(string(raw), skill.ProtectedSkillRedaction) {
		t.Fatalf("saved session missing protected redaction marker:\n%s", raw)
	}
}

func TestSaveLoadLargeMessage(t *testing.T) {
	s := NewSession("sys")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "run it"})
	// A bash result can exceed any line-buffer cap; Save must round-trip it.
	big := strings.Repeat("x", 5*1024*1024)
	s.Add(provider.Message{Role: provider.RoleTool, Name: "bash", ToolCallID: "c1", Content: big})

	path := filepath.Join(t.TempDir(), "big.jsonl")
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	loaded, err := LoadSession(path)
	if err != nil {
		t.Fatalf("LoadSession of a session with a >4MiB message: %v", err)
	}
	if len(loaded.Messages) != 3 {
		t.Fatalf("message count = %d, want 3", len(loaded.Messages))
	}
	if loaded.Messages[2].Content != big {
		t.Errorf("large content not round-tripped (got %d bytes, want %d)", len(loaded.Messages[2].Content), len(big))
	}
}

// TestListSessionsOrdersByMTime makes sure the picker shows the most
// recently used conversation first — that's what users reach for when they
// hit `WorkGround2 --continue`.
func TestListSessionsOrdersByMTime(t *testing.T) {
	dir := t.TempDir()
	// Write two sessions with explicit mtimes so the order is deterministic.
	for _, name := range []string{"a.jsonl", "b.jsonl"} {
		s := NewSession("")
		s.Add(provider.Message{Role: provider.RoleUser, Content: "preview for " + name})
		if err := s.Save(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	oldT := time.Now().Add(-1 * time.Hour)
	newT := time.Now()
	if err := touch(filepath.Join(dir, "a.jsonl"), oldT); err != nil {
		t.Fatal(err)
	}
	if err := touch(filepath.Join(dir, "b.jsonl"), newT); err != nil {
		t.Fatal(err)
	}

	got, err := ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if !strings.HasSuffix(got[0].Path, "b.jsonl") {
		t.Errorf("first entry = %s, want the newer 'b.jsonl'", got[0].Path)
	}
	if got[0].Turns != 1 || got[0].Preview != "preview for b.jsonl" {
		t.Errorf("preview/turns wrong on newest: turns=%d preview=%q", got[0].Turns, got[0].Preview)
	}
}

func TestListSessionsSkipsCleanupPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "pending.jsonl")
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "preview"})
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := MarkCleanupPending(path, "delete"); err != nil {
		t.Fatal(err)
	}
	if !IsCleanupPending(path) {
		t.Fatal("session should be marked cleanup-pending")
	}

	got, err := ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("cleanup-pending session should be hidden, got %+v", got)
	}

	if err := ClearCleanupPending(path); err != nil {
		t.Fatal(err)
	}
	got, err = ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Path != path {
		t.Fatalf("session should be visible after clearing marker, got %+v", got)
	}
}

func TestListSessionsOrdersByLastActivityMeta(t *testing.T) {
	dir := t.TempDir()
	aPath := filepath.Join(dir, "a.jsonl")
	bPath := filepath.Join(dir, "b.jsonl")
	for _, path := range []string{aPath, bPath} {
		s := NewSession("")
		s.Add(provider.Message{Role: provider.RoleUser, Content: "preview for " + filepath.Base(path)})
		if err := s.Save(path); err != nil {
			t.Fatal(err)
		}
	}

	now := time.Now().UTC()
	olderActivity := now.Add(-2 * time.Hour)
	newerActivity := now.Add(-1 * time.Hour)
	writeBranchMeta(t, aPath, now.Add(-24*time.Hour), newerActivity, "")
	writeBranchMeta(t, bPath, now.Add(-24*time.Hour), olderActivity, "")
	if err := touch(aPath, now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := touch(bPath, now); err != nil {
		t.Fatal(err)
	}

	got, err := ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].Path != aPath {
		t.Fatalf("first entry = %s, want activity-newer a.jsonl despite older file mtime", got[0].Path)
	}
	if !got[0].LastActivityAt.Equal(newerActivity) || !got[0].ModTime.Equal(newerActivity) {
		t.Fatalf("activity fields = %s / %s, want %s", got[0].LastActivityAt, got[0].ModTime, newerActivity)
	}
}

func TestListSessionOrderIncludesEmptySessionsWithoutPreviewScan(t *testing.T) {
	dir := t.TempDir()
	emptyPath := filepath.Join(dir, "empty.jsonl")
	collaborationPath := filepath.Join(dir, "collaboration.jsonl")
	realPath := filepath.Join(dir, "real.jsonl")
	if err := os.WriteFile(emptyPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(collaborationPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "real prompt"})
	if err := s.Save(realPath); err != nil {
		t.Fatal(err)
	}

	now := time.Now().UTC()
	writeBranchMeta(t, emptyPath, now, now.Add(time.Hour), "")
	writeBranchMeta(t, collaborationPath, now, now.Add(30*time.Minute), SessionKindCollaboration)
	writeBranchMeta(t, realPath, now, now, "")

	ordered, err := ListSessionOrder(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(ordered) != 3 {
		t.Fatalf("lightweight order len = %d, want 3", len(ordered))
	}
	if ordered[0].Path != emptyPath {
		t.Fatalf("lightweight order first = %s, want newer empty session %s", ordered[0].Path, emptyPath)
	}

	listed, err := ListSessions(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed) != 2 || listed[0].Path != collaborationPath || listed[1].Path != realPath {
		t.Fatalf("ListSessions = %+v, want the durable collaboration session and non-empty real session", listed)
	}
}

func writeBranchMeta(t *testing.T, path string, createdAt, updatedAt time.Time, kind SessionKind) {
	t.Helper()
	meta := BranchMeta{
		ID:          BranchID(path),
		CreatedAt:   createdAt,
		UpdatedAt:   updatedAt,
		SessionKind: kind,
	}
	b, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(BranchMetaPath(path), append(b, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestContinueSessionPathReusesPriorFile(t *testing.T) {
	prev := filepath.Join("sessions", "20260602-120000.000000000-deepseek.jsonl")
	if got := ContinueSessionPath(prev, "sessions", "other-model"); got != prev {
		t.Fatalf("carried conversation should keep its file %q, got %q", prev, got)
	}
}

func TestContinueSessionPathMintsFreshWhenNoPrior(t *testing.T) {
	dir := t.TempDir()
	got := ContinueSessionPath("", dir, "deepseek")
	if filepath.Dir(got) != dir || !strings.HasSuffix(got, ".jsonl") {
		t.Fatalf("fresh path = %q, want a .jsonl under %q", got, dir)
	}
}

func TestContinueSessionPathNoPersistence(t *testing.T) {
	if got := ContinueSessionPath("", "", "deepseek"); got != "" {
		t.Fatalf("no session dir should disable persistence, got %q", got)
	}
}

// TestListSessionsMissingDir returns nil + no error so callers can fall
// through to a fresh session without special-casing.
func TestListSessionsMissingDir(t *testing.T) {
	got, err := ListSessions(filepath.Join(t.TempDir(), "never-created"))
	if err != nil || got != nil {
		t.Errorf("missing dir = %v / %v, want nil/nil", got, err)
	}
}

// TestRecoveryBranchInheritsWorkIdentity verifies that SaveRecoveryBranch
// inherits SessionKind/WorkID/WorkRequestID from the parent BranchMeta
// when the caller does not provide them.
func TestRecoveryBranchInheritsWorkIdentity(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "work-task.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "work item"})
	if err := parent.Save(parentPath); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	parentMeta, err := EnsureBranchMeta(parentPath)
	if err != nil {
		t.Fatalf("ensure parent meta: %v", err)
	}
	parentMeta.SessionKind = SessionKindWork
	parentMeta.WorkID = "work-1"
	parentMeta.WorkRequestID = "req-abc"
	if err := SaveBranchMetaPreserveUpdated(parentPath, parentMeta); err != nil {
		t.Fatalf("save parent work identity: %v", err)
	}

	stale := NewSession("sys")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "recovered"})
	recovery, err := stale.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: parentPath})
	if err != nil {
		t.Fatalf("save recovery branch: %v", err)
	}
	if recovery.Meta.SessionKind != SessionKindWork {
		t.Fatalf("recovery SessionKind = %q, want %q", recovery.Meta.SessionKind, SessionKindWork)
	}
	if recovery.Meta.WorkID != "work-1" {
		t.Fatalf("recovery WorkID = %q, want work-1", recovery.Meta.WorkID)
	}
	if recovery.Meta.WorkRequestID != "req-abc" {
		t.Fatalf("recovery WorkRequestID = %q, want req-abc", recovery.Meta.WorkRequestID)
	}

	// Round-trip through disk: reload and verify persistence.
	loaded, ok, err := LoadBranchMeta(recovery.Path)
	if err != nil || !ok {
		t.Fatalf("load recovery meta: ok=%v err=%v", ok, err)
	}
	if loaded.SessionKind != SessionKindWork {
		t.Fatalf("persisted SessionKind = %q, want %q", loaded.SessionKind, SessionKindWork)
	}
	if loaded.WorkID != "work-1" {
		t.Fatalf("persisted WorkID = %q", loaded.WorkID)
	}
	if loaded.WorkRequestID != "req-abc" {
		t.Fatalf("persisted WorkRequestID = %q", loaded.WorkRequestID)
	}
}

// TestRecoveryBranchInheritsWorkIdentityNested verifies that nested
// recovery branches also inherit Work identity from the root ancestor.
func TestRecoveryBranchInheritsWorkIdentityNested(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "work-task.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "root"})
	if err := parent.Save(parentPath); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	parentMeta, err := EnsureBranchMeta(parentPath)
	if err != nil {
		t.Fatalf("ensure parent meta: %v", err)
	}
	parentMeta.SessionKind = SessionKindWork
	parentMeta.WorkID = "work-nested"
	parentMeta.WorkRequestID = "req-nested"
	if err := SaveBranchMetaPreserveUpdated(parentPath, parentMeta); err != nil {
		t.Fatalf("save parent work identity: %v", err)
	}

	// First recovery.
	r1 := NewSession("sys")
	r1.Add(provider.Message{Role: provider.RoleUser, Content: "recovery 1"})
	info1, err := r1.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: parentPath})
	if err != nil {
		t.Fatalf("first recovery: %v", err)
	}

	// Second recovery from the first recovery.
	r2 := NewSession("sys")
	r2.Add(provider.Message{Role: provider.RoleUser, Content: "recovery 2"})
	info2, err := r2.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: info1.Path})
	if err != nil {
		t.Fatalf("second recovery: %v", err)
	}
	if info2.Meta.SessionKind != SessionKindWork {
		t.Fatalf("nested recovery SessionKind = %q, want %q", info2.Meta.SessionKind, SessionKindWork)
	}
	if info2.Meta.WorkID != "work-nested" {
		t.Fatalf("nested recovery WorkID = %q, want work-nested", info2.Meta.WorkID)
	}
	if info2.Meta.WorkRequestID != "req-nested" {
		t.Fatalf("nested recovery WorkRequestID = %q, want req-nested", info2.Meta.WorkRequestID)
	}
}

// TestRecoveryBranchDoesNotInventWorkIdentity verifies that a normal
// session recovery does not pick up a spurious Work identity.
func TestRecoveryBranchDoesNotInventWorkIdentity(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "chat.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	if err := parent.Save(parentPath); err != nil {
		t.Fatalf("save parent: %v", err)
	}

	stale := NewSession("sys")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "recovered chat"})
	recovery, err := stale.SaveRecoveryBranch(RecoveryBranchOptions{OriginalPath: parentPath})
	if err != nil {
		t.Fatalf("save recovery branch: %v", err)
	}
	if recovery.Meta.SessionKind != "" && recovery.Meta.SessionKind != SessionKindNormal {
		t.Fatalf("normal session recovery got SessionKind = %q, want empty or normal", recovery.Meta.SessionKind)
	}
	if recovery.Meta.WorkID != "" {
		t.Fatalf("normal session recovery got WorkID = %q, want empty", recovery.Meta.WorkID)
	}
	if recovery.Meta.WorkRequestID != "" {
		t.Fatalf("normal session recovery got WorkRequestID = %q, want empty", recovery.Meta.WorkRequestID)
	}
}

// TestRecoveryBranchCallerProvidedWorkIdentityWins verifies that when the
// caller explicitly provides Work identity in opts.BranchMeta, it is
// preserved (not overwritten by parent inheritance).
func TestRecoveryBranchCallerProvidedWorkIdentityWins(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "work-old.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "old work"})
	if err := parent.Save(parentPath); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	parentMeta, err := EnsureBranchMeta(parentPath)
	if err != nil {
		t.Fatalf("ensure parent meta: %v", err)
	}
	parentMeta.SessionKind = SessionKindWork
	parentMeta.WorkID = "old-work"
	parentMeta.WorkRequestID = "old-req"
	if err := SaveBranchMetaPreserveUpdated(parentPath, parentMeta); err != nil {
		t.Fatalf("save parent old work: %v", err)
	}

	stale := NewSession("sys")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "recovered"})
	recovery, err := stale.SaveRecoveryBranch(RecoveryBranchOptions{
		OriginalPath: parentPath,
		BranchMeta: BranchMeta{
			SessionKind:   SessionKindWork,
			WorkID:        "caller-work",
			WorkRequestID: "caller-req",
		},
	})
	if err != nil {
		t.Fatalf("save recovery branch: %v", err)
	}
	if recovery.Meta.WorkID != "caller-work" {
		t.Fatalf("caller WorkID should win: got %q, want caller-work", recovery.Meta.WorkID)
	}
	if recovery.Meta.WorkRequestID != "caller-req" {
		t.Fatalf("caller WorkRequestID should win: got %q, want caller-req", recovery.Meta.WorkRequestID)
	}
}

func TestRecoveryBranchExplicitNormalDoesNotInheritWorkIdentity(t *testing.T) {
	dir := t.TempDir()
	parentPath := filepath.Join(dir, "work-parent.jsonl")
	parent := NewSession("sys")
	parent.Add(provider.Message{Role: provider.RoleUser, Content: "work"})
	if err := parent.Save(parentPath); err != nil {
		t.Fatalf("save parent: %v", err)
	}
	meta, err := EnsureBranchMeta(parentPath)
	if err != nil {
		t.Fatalf("ensure parent meta: %v", err)
	}
	meta.SessionKind = SessionKindWork
	meta.WorkID = "work-parent"
	meta.WorkRequestID = "request-parent"
	if err := SaveBranchMetaPreserveUpdated(parentPath, meta); err != nil {
		t.Fatalf("save parent meta: %v", err)
	}

	stale := NewSession("sys")
	stale.Add(provider.Message{Role: provider.RoleUser, Content: "normal recovery"})
	recovery, err := stale.SaveRecoveryBranch(RecoveryBranchOptions{
		OriginalPath: parentPath,
		BranchMeta:   BranchMeta{SessionKind: SessionKindNormal},
	})
	if err != nil {
		t.Fatalf("save recovery: %v", err)
	}
	if recovery.Meta.SessionKind != SessionKindNormal || recovery.Meta.WorkID != "" || recovery.Meta.WorkRequestID != "" {
		t.Fatalf("explicit normal recovery inherited Work identity: %+v", recovery.Meta)
	}
}

func TestResolveLegacyWorkIdentities_ParentChain(t *testing.T) {
	dir := t.TempDir()

	// Build: work-parent → recovery-1 → recovery-2 (deep chain)
	// Only work-parent has SessionKind=Work; the recoveries were saved
	// before the inheritance fix so their sidecars lack the field.
	parentPath := filepath.Join(dir, "work-parent.jsonl")
	saveSessionWithMeta(t, parentPath, BranchMeta{
		SessionKind: SessionKindWork, WorkID: "w-1", WorkRequestID: "r-1",
		ParentID: "",
	})

	r1Path := filepath.Join(dir, "recovery-1.jsonl")
	saveSessionWithMeta(t, r1Path, BranchMeta{
		ParentID: BranchID(parentPath), Recovered: true,
		// SessionKind intentionally empty — simulates pre-fix sidecar.
	})

	r2Path := filepath.Join(dir, "recovery-2.jsonl")
	saveSessionWithMeta(t, r2Path, BranchMeta{
		ParentID: BranchID(r1Path), Recovered: true,
	})

	infos := mustListSessionOrder(t, dir)
	// resolveLegacyWorkIdentities is called inside ListSessionOrder.
	for _, info := range infos {
		switch info.Path {
		case parentPath:
			if info.SessionKind != SessionKindWork || info.WorkID != "w-1" {
				t.Fatalf("parent SessionKind=%q WorkID=%q, want work/w-1", info.SessionKind, info.WorkID)
			}
		case r1Path:
			if info.SessionKind != SessionKindWork || info.WorkID != "w-1" {
				t.Fatalf("r1 SessionKind=%q WorkID=%q, want work/w-1 (chain resolve)", info.SessionKind, info.WorkID)
			}
		case r2Path:
			if info.SessionKind != SessionKindWork || info.WorkID != "w-1" {
				t.Fatalf("r2 SessionKind=%q WorkID=%q, want work/w-1 (nested resolve)", info.SessionKind, info.WorkID)
			}
		}
	}
}

func TestResolveLegacyWorkIdentities_NormalUnaffected(t *testing.T) {
	dir := t.TempDir()

	// Normal chat session with a recovery child.
	chatPath := filepath.Join(dir, "chat.jsonl")
	saveSessionWithMeta(t, chatPath, BranchMeta{
		SessionKind: SessionKindNormal, ParentID: "",
	})

	recPath := filepath.Join(dir, "chat-recovery.jsonl")
	saveSessionWithMeta(t, recPath, BranchMeta{
		ParentID: BranchID(chatPath), Recovered: true,
	})

	infos := mustListSessionOrder(t, dir)
	for _, info := range infos {
		if info.SessionKind == SessionKindWork {
			t.Fatalf("%s got SessionKind=work from normal parent", info.Path)
		}
		if info.WorkID != "" {
			t.Fatalf("%s got WorkID=%q from normal parent", info.Path, info.WorkID)
		}
	}
}

func TestResolveLegacyWorkIdentities_ExplicitNormalStops(t *testing.T) {
	dir := t.TempDir()

	// Work parent → recovery with explicit SessionKind=normal.
	workPath := filepath.Join(dir, "work.jsonl")
	saveSessionWithMeta(t, workPath, BranchMeta{
		SessionKind: SessionKindWork, WorkID: "w-stop",
	})

	recPath := filepath.Join(dir, "work-recovery-normal.jsonl")
	saveSessionWithMeta(t, recPath, BranchMeta{
		ParentID: BranchID(workPath), Recovered: true,
		SessionKind: SessionKindNormal,
	})

	infos := mustListSessionOrder(t, dir)
	for _, info := range infos {
		if info.Path == recPath && info.SessionKind == SessionKindWork {
			t.Fatalf("explicit normal recovery should not inherit Work identity")
		}
	}
}

func TestResolveLegacyWorkIdentities_CycleSafe(t *testing.T) {
	dir := t.TempDir()

	// Self-referencing ParentID should not crash or infinite-loop.
	aPath := filepath.Join(dir, "a.jsonl")
	saveSessionWithMeta(t, aPath, BranchMeta{
		ParentID: BranchID(aPath), Recovered: true,
	})

	infos := mustListSessionOrder(t, dir)
	if len(infos) != 1 {
		t.Fatalf("expected 1 session, got %d", len(infos))
	}
	// Just verifying no panic — the function should return safely.
}

func TestResolveLegacyWorkIdentities_MissingParent(t *testing.T) {
	dir := t.TempDir()

	// Session with ParentID pointing to a non-existent session.
	orphanPath := filepath.Join(dir, "orphan.jsonl")
	saveSessionWithMeta(t, orphanPath, BranchMeta{
		ParentID: "nonexistent-id", Recovered: true,
	})

	infos := mustListSessionOrder(t, dir)
	if len(infos) != 1 {
		t.Fatalf("expected 1 session, got %d", len(infos))
	}
	// Orphan should stay as-is, no crash.
	if infos[0].SessionKind == SessionKindWork {
		t.Fatalf("orphan with missing parent should not get Work identity")
	}
}

func TestResolveLegacyWorkIdentities_InvalidParentID(t *testing.T) {
	dir := t.TempDir()

	// ParentID with path separators should be rejected safely.
	badPath := filepath.Join(dir, "bad.jsonl")
	saveSessionWithMeta(t, badPath, BranchMeta{
		ParentID: "../../etc/passwd", Recovered: true,
	})

	infos := mustListSessionOrder(t, dir)
	if len(infos) != 1 {
		t.Fatalf("expected 1 session, got %d", len(infos))
	}
	// Should not crash and should not resolve.
}

func TestResolveLegacyWorkIdentities_DepthLimited(t *testing.T) {
	dir := t.TempDir()
	rootPath := filepath.Join(dir, "work-root.jsonl")
	saveSessionWithMeta(t, rootPath, BranchMeta{SessionKind: SessionKindWork, WorkID: "too-deep"})
	parentID := BranchID(rootPath)
	for i := 10; i >= 1; i-- {
		path := filepath.Join(dir, fmt.Sprintf("recovery-%d.jsonl", i))
		saveSessionWithMeta(t, path, BranchMeta{ParentID: parentID, Recovered: true})
		parentID = BranchID(path)
	}
	currentPath := filepath.Join(dir, "current.jsonl")
	saveSessionWithMeta(t, currentPath, BranchMeta{ParentID: parentID, Recovered: true})

	infos := mustListSessionOrder(t, dir)
	for _, info := range infos {
		if info.Path == currentPath && info.SessionKind == SessionKindWork {
			t.Fatal("over-depth recovery chain inherited Work identity")
		}
	}
}

// saveSessionWithMeta writes a minimal .jsonl + .jsonl.meta pair.
func saveSessionWithMeta(t *testing.T, path string, meta BranchMeta) {
	t.Helper()
	s := NewSession("")
	s.Add(provider.Message{Role: provider.RoleUser, Content: "hello"})
	if err := s.Save(path); err != nil {
		t.Fatalf("save %s: %v", path, err)
	}
	// Load the metadata EnsureBranchMeta just wrote, then merge in our fields.
	existing, err := EnsureBranchMeta(path)
	if err != nil {
		t.Fatalf("ensure meta %s: %v", path, err)
	}
	// Merge: keep existing non-empty values unless overridden by meta param.
	if meta.SessionKind != "" {
		existing.SessionKind = meta.SessionKind
	}
	if meta.WorkID != "" {
		existing.WorkID = meta.WorkID
	}
	if meta.WorkRequestID != "" {
		existing.WorkRequestID = meta.WorkRequestID
	}
	if meta.ParentID != "" {
		existing.ParentID = meta.ParentID
	}
	if meta.Recovered {
		existing.Recovered = true
	}
	existing.SessionSource = meta.SessionSource
	if err := SaveBranchMetaPreserveUpdated(path, existing); err != nil {
		t.Fatalf("save meta %s: %v", path, err)
	}
}

func mustListSessionOrder(t *testing.T, dir string) []SessionOrderInfo {
	t.Helper()
	infos, err := ListSessionOrder(dir)
	if err != nil {
		t.Fatalf("ListSessionOrder: %v", err)
	}
	return infos
}
