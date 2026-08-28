package sessiontool

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"

	"workground2/internal/agent"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

func execTool(t *testing.T, tl tool.Tool, args map[string]any) map[string]any {
	t.Helper()
	raw, _ := json.Marshal(args)
	out, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s Execute: %v", tl.Name(), err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%s returned invalid JSON %q: %v", tl.Name(), out, err)
	}
	return res
}

func makeSession(t *testing.T, dir, name, owner string, purpose agent.SessionPurpose, status agent.SessionStatus) {
	t.Helper()
	path := filepath.Join(dir, name)
	sess := agent.NewSession("sys")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "hi"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		SessionKind: agent.SessionKindAssistant, AssistantID: owner, Purpose: purpose,
		Status: status, Turns: 1, SchemaVersion: agent.BranchMetaCountsVersion,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSessionListAndStatusQuery(t *testing.T) {
	dir := t.TempDir()
	makeSession(t, dir, "s1.jsonl", "assistant-1", agent.PurposeManaged, agent.SessionStatusCompleted)
	makeSession(t, dir, "s2.jsonl", "assistant-1", agent.PurposeSupervisor, agent.SessionStatusRunning)
	makeSession(t, dir, "s3.jsonl", "assistant-2", agent.PurposeManaged, agent.SessionStatusFailed)

	list := NewSessionListTool(dir)
	// session_list returns a top-level JSON array, not an object.
	raw, _ := json.Marshal(map[string]any{"owner_assistant_id": "assistant-1"})
	out, _ := list.Execute(context.Background(), raw)
	var items []sessionSummary
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("session_list: %v (out=%s)", err, out)
	}
	if len(items) != 2 {
		t.Fatalf("owned sessions = %d, want 2 (%+v)", len(items), items)
	}
	for _, it := range items {
		if it.OwnerID != "assistant-1" {
			t.Fatalf("unexpected owner %q", it.OwnerID)
		}
	}

	status := NewSessionStatusTool(dir)
	raw2, _ := json.Marshal(map[string]any{"session_ids": []string{"s3", "missing"}})
	out2, _ := status.Execute(context.Background(), raw2)
	var sts []sessionSummary
	if err := json.Unmarshal([]byte(out2), &sts); err != nil {
		t.Fatal(err)
	}
	if len(sts) != 2 || sts[0].Status != agent.SessionStatusFailed || sts[1].ID != "missing" {
		t.Fatalf("status = %+v", sts)
	}
}

func TestSessionQueriesSpanDirsAndReadBoundedTail(t *testing.T) {
	dirA := t.TempDir()
	dirB := t.TempDir()
	makeSession(t, dirA, "a.jsonl", "assistant-1", agent.PurposeManaged, agent.SessionStatusCompleted)
	path := filepath.Join(dirB, "b.jsonl")
	sess := agent.NewSession("system must stay hidden")
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "question"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "answer"})
	sess.Add(provider.Message{Role: provider.RoleTool, Name: "bash", Content: "tool summary"})
	if err := sess.Save(path); err != nil {
		t.Fatal(err)
	}
	if err := agent.SaveBranchMeta(path, agent.BranchMeta{
		SessionKind: agent.SessionKindAssistant, AssistantID: "assistant-1", Purpose: agent.PurposeManaged,
		Status: agent.SessionStatusCompleted, Turns: 2, SchemaVersion: agent.BranchMetaCountsVersion,
	}); err != nil {
		t.Fatal(err)
	}

	raw, _ := json.Marshal(map[string]any{"owner_assistant_id": "assistant-1"})
	out, err := NewSessionListToolDirs([]string{dirA, dirB, dirA}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var listed []sessionSummary
	if err := json.Unmarshal([]byte(out), &listed); err != nil || len(listed) != 2 {
		t.Fatalf("multi-dir list = %+v err=%v", listed, err)
	}

	raw, _ = json.Marshal(map[string]any{"session_id": "b", "limit": 2})
	out, err = NewSessionReadToolDirs([]string{dirA, dirB}).Execute(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	var read sessionReadResult
	if err := json.Unmarshal([]byte(out), &read); err != nil {
		t.Fatal(err)
	}
	if read.Session.ID != "b" || len(read.Messages) != 2 {
		t.Fatalf("session_read = %+v", read)
	}
	if read.Messages[0].Role != string(provider.RoleAssistant) || read.Messages[1].Name != "bash" {
		t.Fatalf("bounded tail = %+v", read.Messages)
	}
	if read.Messages[0].Content == "system must stay hidden" || read.Messages[1].Content == "system must stay hidden" {
		t.Fatal("session_read exposed the system prompt")
	}
}

type fakeControl struct {
	steered   string
	answered  bool
	cancelled bool
	resumed   bool
	retried   bool
	forked    string
	created   string
}

func (f *fakeControl) Steer(id, text, requestID string) error {
	f.steered = id + ":" + text
	return nil
}
func (f *fakeControl) AnswerQuestion(id, qid string, a []event.AskAnswer, requestID string) error {
	f.answered = true
	return nil
}
func (f *fakeControl) Cancel(id, requestID string) error { f.cancelled = true; return nil }
func (f *fakeControl) Resume(id, requestID string) error { f.resumed = true; return nil }
func (f *fakeControl) Retry(id, requestID string) error  { f.retried = true; return nil }
func (f *fakeControl) Fork(id, requestID string) (string, error) {
	f.forked = id
	return id + "-fork", nil
}
func (f *fakeControl) Create(req SessionCreateRequest) (string, error) {
	f.created = req.Title
	return "new-session", nil
}
func (f *fakeControl) PendingInteractions(id string) ([]SessionInteraction, error) {
	return []SessionInteraction{{Kind: "ask", ID: "q1"}}, nil
}

func TestSessionControlToolsSubmitIntent(t *testing.T) {
	fc := &fakeControl{}

	steer := NewSessionSteerTool(fc, t.TempDir())
	r := execTool(t, steer, map[string]any{"session_id": "s1", "text": "do x"})
	if r["status"] != "accepted" || fc.steered != "s1:do x" {
		t.Fatalf("steer = %v fc=%+v", r, fc)
	}

	answer := NewInteractionAnswerTool(fc, t.TempDir())
	r = execTool(t, answer, map[string]any{"session_id": "s1", "question_id": "q1", "selected": []string{"a"}})
	if r["status"] != "accepted" || !fc.answered {
		t.Fatalf("answer = %v fc=%+v", r, fc)
	}

	cancel := NewSessionCancelTool(fc, t.TempDir())
	if r := execTool(t, cancel, map[string]any{"session_id": "s1"}); r["status"] != "accepted" || !fc.cancelled {
		t.Fatalf("cancel = %v fc=%+v", r, fc)
	}

	resume := NewSessionResumeTool(fc, t.TempDir())
	if r := execTool(t, resume, map[string]any{"session_id": "s1"}); r["status"] != "accepted" || !fc.resumed {
		t.Fatalf("resume = %v fc=%+v", r, fc)
	}

	retry := NewSessionRetryTool(fc, t.TempDir())
	if r := execTool(t, retry, map[string]any{"session_id": "s1"}); r["status"] != "accepted" || !fc.retried {
		t.Fatalf("retry = %v fc=%+v", r, fc)
	}

	fork := NewSessionForkTool(fc, t.TempDir())
	r = execTool(t, fork, map[string]any{"session_id": "s1"})
	if r["status"] != "accepted" || r["session"] != "s1-fork" || fc.forked != "s1" {
		t.Fatalf("fork = %v fc=%+v", r, fc)
	}

	create := NewSessionCreateTool(fc, t.TempDir())
	r = execTool(t, create, map[string]any{"title": "t", "prompt": "p", "request_id": "r1"})
	if r["status"] != "accepted" || r["session"] != "new-session" || fc.created != "t" {
		t.Fatalf("create = %v fc=%+v", r, fc)
	}

	// Empty session_id is rejected explicitly.
	r = execTool(t, steer, map[string]any{"session_id": "", "text": "x"})
	if r["status"] != "invalid" {
		t.Fatalf("empty session_id status = %v", r["status"])
	}
}

func TestSessionControlToolReplayIsIdempotent(t *testing.T) {
	fc := &fakeControl{}
	steer := NewSessionSteerTool(fc, t.TempDir())

	// First application executes and is accepted.
	r := execTool(t, steer, map[string]any{"session_id": "s1", "text": "do x", "request_id": "steer-replay-1"})
	if r["status"] != "accepted" || fc.steered != "s1:do x" {
		t.Fatalf("first steer = %v fc=%+v", r, fc)
	}
	fc.steered = ""

	// A replay with the same request_id is not executed again.
	r = execTool(t, steer, map[string]any{"session_id": "s1", "text": "do x", "request_id": "steer-replay-1"})
	if r["status"] != "already_applied" {
		t.Fatalf("replay steer status = %v, want already_applied (%v)", r["status"], r)
	}
	if fc.steered != "" {
		t.Fatalf("replay steer re-executed: fc.steered = %q", fc.steered)
	}

	// A different request_id still executes normally.
	r = execTool(t, steer, map[string]any{"session_id": "s2", "text": "do y", "request_id": "steer-replay-2"})
	if r["status"] != "accepted" || fc.steered != "s2:do y" {
		t.Fatalf("second steer = %v fc=%+v", r, fc)
	}

	// A replayed fork returns already_applied with the original session ID.
	fork := NewSessionForkTool(fc, t.TempDir())
	r = execTool(t, fork, map[string]any{"session_id": "s1", "request_id": "fork-replay-1"})
	if r["status"] != "accepted" || r["session"] != "s1-fork" {
		t.Fatalf("first fork = %v", r)
	}
	fc.forked = ""
	r = execTool(t, fork, map[string]any{"session_id": "s1", "request_id": "fork-replay-1"})
	if r["status"] != "already_applied" || r["session"] != "s1-fork" {
		t.Fatalf("replay fork = %v", r)
	}
	if fc.forked != "" {
		t.Fatalf("replay fork re-executed: fc.forked = %q", fc.forked)
	}
}

func TestSessionControlToolReceiptSurvivesToolReconstruction(t *testing.T) {
	dir := t.TempDir()

	// First tool instance applies and persists its receipt to dir.
	fc1 := &fakeControl{}
	steer1 := NewSessionSteerTool(fc1, dir)
	r := execTool(t, steer1, map[string]any{"session_id": "s1", "text": "do x", "request_id": "persist-1"})
	if r["status"] != "accepted" || fc1.steered != "s1:do x" {
		t.Fatalf("first steer = %v fc=%+v", r, fc1)
	}

	// A brand-new tool instance with a brand-new control, sharing only the same
	// receiptDir, must observe the earlier receipt: the receipt is durable (a
	// Session-subsystem file), not an in-process map.
	fc2 := &fakeControl{}
	steer2 := NewSessionSteerTool(fc2, dir)
	r = execTool(t, steer2, map[string]any{"session_id": "s1", "text": "do x", "request_id": "persist-1"})
	if r["status"] != "already_applied" {
		t.Fatalf("replay across instances status = %v, want already_applied (%v)", r["status"], r)
	}
	if fc2.steered != "" {
		t.Fatalf("replay across instances re-executed: fc2.steered = %q", fc2.steered)
	}
}

// TestSessionControlToolRequestConflictRejected proves acceptance #3: the same
// request_id reused with different parameters is an explicit conflict (invalid
// + message), never a silent reuse of the old outcome — across tool instances
// and Stores (the receipt is a durable Session-subsystem file).
func TestSessionControlToolRequestConflictRejected(t *testing.T) {
	dir := t.TempDir()
	fc := &fakeControl{}
	resume := NewSessionResumeTool(fc, dir)

	// First application with request "conflict-1" targets session s1.
	r := execTool(t, resume, map[string]any{"session_id": "s1", "request_id": "conflict-1"})
	if r["status"] != "accepted" || !fc.resumed {
		t.Fatalf("first resume = %v fc=%+v", r, fc)
	}
	fc.resumed = false

	// Same request_id but a different session is a conflict — the old receipt
	// is bound to a different fingerprint.
	r = execTool(t, resume, map[string]any{"session_id": "s2", "request_id": "conflict-1"})
	if r["status"] != "invalid" {
		t.Fatalf("conflicting resume status = %v, want invalid (%v)", r["status"], r)
	}
	if fc.resumed {
		t.Fatal("conflicting request re-executed the resume against a different session")
	}

	// Same request_id AND same parameters is already_applied with the recorded
	// outcome (cross-instance replay).
	fc2 := &fakeControl{}
	resume2 := NewSessionResumeTool(fc2, dir)
	r = execTool(t, resume2, map[string]any{"session_id": "s1", "request_id": "conflict-1"})
	if r["status"] != "already_applied" {
		t.Fatalf("replay status = %v, want already_applied (%v)", r["status"], r)
	}
	if fc2.resumed {
		t.Fatal("replay re-executed the resume")
	}
}

// TestSessionControlResultUnifiedOutcome proves the write tools return the
// unified outcome/status/revision/next_hint shape (not only a bare status),
// with the fields populated from the durable Session subsystem when the target
// session is locatable under the receipt dir.
func TestSessionControlResultUnifiedOutcome(t *testing.T) {
	dir := t.TempDir()
	makeSession(t, dir, "s1.jsonl", "assistant-1", agent.PurposeManaged, agent.SessionStatusFailed)

	fc := &fakeControl{}
	resume := NewSessionResumeTool(fc, dir)
	r := execTool(t, resume, map[string]any{"session_id": "s1", "request_id": "unified-1"})
	if r["outcome"] != "accepted" {
		t.Fatalf("outcome = %v, want accepted (%v)", r["outcome"], r)
	}
	if r["status"] != "accepted" {
		t.Fatalf("status = %v, want accepted (backward compat) (%v)", r["status"], r)
	}
	if got := r["session_status"]; got != "failed" {
		t.Fatalf("session_status = %v, want failed (from durable meta) (%v)", got, r)
	}
	if _, has := r["revision"]; !has {
		t.Fatalf("result missing revision field: %v", r)
	}
	if _, has := r["next_hint"]; !has {
		t.Fatalf("result missing next_hint field: %v", r)
	}
}
