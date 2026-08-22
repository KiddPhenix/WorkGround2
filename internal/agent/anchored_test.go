package agent

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"workground2/internal/agent/testutil"
	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

// anchoredFakeTool is a minimal Tool for registry-based anchored-bootstrap tests.
type anchoredFakeTool struct {
	name string
}

func (f anchoredFakeTool) Name() string        { return f.name }
func (f anchoredFakeTool) Description() string { return "fake tool " + f.name }
func (f anchoredFakeTool) Schema() json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{}}`)
}
func (f anchoredFakeTool) Execute(context.Context, json.RawMessage) (string, error) {
	if f.name == "read_file" {
		return "file: ok", nil
	}
	return "ok", nil
}
func (f anchoredFakeTool) ReadOnly() bool {
	return f.name != "bash" && f.name != "write_file"
}

// anchoredRegistry holds the bootstrap trio plus representative full-catalog
// tools the bootstrap must hide.
func anchoredRegistry() *tool.Registry {
	reg := tool.NewRegistry()
	for _, n := range []string{"bash", "read_file", "edit_file", "write_file", "grep", "todo_write", "ls"} {
		reg.Add(anchoredFakeTool{name: n})
	}
	return reg
}

const (
	anchoredFullPrompt      = "FULL: base persona\n\npersistent memory block\n\nskills index"
	anchoredBootstrapPrompt = "FULL: base persona"
)

func anchoredTestAgent(mp *testutil.MockProvider, sess *Session) *Agent {
	return New(mp, anchoredRegistry(), sess, Options{AnchoredBootstrapSystemPrompt: anchoredBootstrapPrompt}, event.Discard)
}

func schemaNames(schemas []provider.ToolSchema) []string {
	out := make([]string, 0, len(schemas))
	for _, s := range schemas {
		out = append(out, s.Name)
	}
	return out
}

// TestAnchoredFirstRequestSendsBootstrapSurface drives one text-only turn and
// asserts request #1 saw the bootstrap trio, the shortened system prompt, and
// that the durable session log kept the full prompt untouched.
func TestAnchoredFirstRequestSendsBootstrapSurface(t *testing.T) {
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	sess := NewSession(anchoredFullPrompt)
	a := anchoredTestAgent(mp, sess)
	if err := a.Run(context.Background(), "inspect this repo"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := mp.Requests()
	if len(reqs) == 0 {
		t.Fatal("no model requests recorded")
	}
	req := reqs[0]
	names := schemaNames(req.Tools)
	if len(names) != 3 {
		t.Fatalf("bootstrap catalog must expose exactly bash/read_file/edit_file, got %v", names)
	}
	for _, want := range []string{"bash", "read_file", "edit_file"} {
		if !containsStr(names, want) {
			t.Fatalf("bootstrap catalog missing %q: %v", want, names)
		}
	}
	if req.Messages[0].Role != provider.RoleSystem || req.Messages[0].Content != anchoredBootstrapPrompt {
		t.Fatalf("request #1 system prompt must be the bootstrap prefix, got %q", req.Messages[0].Content)
	}
	if sess.Messages[0].Content != anchoredFullPrompt {
		t.Fatalf("session log must keep the full prompt untouched, got %q", sess.Messages[0].Content)
	}
	if !a.anchoredPromoted() {
		t.Fatal("session must be promoted after the first assistant reply")
	}
}

// TestAnchoredPromotionRestoresFullSurface scripts a tool round so the loop
// issues two requests: request #1 on the bootstrap surface, request #2 (after
// the tool result) on the full catalog with the full system prompt.
func TestAnchoredPromotionRestoresFullSurface(t *testing.T) {
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "r1", Name: "read_file", Arguments: `{"path":"a.go"}`}}},
		testutil.Turn{Text: "done"},
	)
	sess := NewSession(anchoredFullPrompt)
	a := anchoredTestAgent(mp, sess)
	if err := a.Run(context.Background(), "review a.go"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	reqs := mp.Requests()
	if len(reqs) != 2 {
		t.Fatalf("want 2 requests, got %d", len(reqs))
	}
	first, second := reqs[0], reqs[1]
	if len(schemaNames(first.Tools)) != 3 {
		t.Fatalf("request #1 must be bootstrap: %v", schemaNames(first.Tools))
	}
	if first.Messages[0].Content != anchoredBootstrapPrompt {
		t.Fatalf("request #1 must send bootstrap prompt, got %q", first.Messages[0].Content)
	}
	if got := schemaNames(second.Tools); len(got) != 7 {
		t.Fatalf("request #2 must carry the full catalog, got %v", got)
	}
	if second.Messages[0].Content != anchoredFullPrompt {
		t.Fatalf("request #2 must send the full prompt, got %q", second.Messages[0].Content)
	}
}

// TestAnchoredNeverDemotesAcrossFold asserts promotion survives a compaction
// fold (assistant messages summarized away) and even a fully stripped log:
// the in-process latch is monotonic, per the "keep promoted, never fall back"
// decision.
func TestAnchoredNeverDemotesAcrossFold(t *testing.T) {
	sess := NewSession(anchoredFullPrompt)
	a := anchoredTestAgent(testutil.NewMock("m"), sess)
	if a.anchoredPromoted() {
		t.Fatal("fresh session must start unpromoted")
	}
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "first reply"})
	if !a.anchoredPromoted() {
		t.Fatal("first assistant reply must promote")
	}
	// A compaction folds the assistant turn into a summary; a resumed process
	// must still reconstruct promotion from the summary marker.
	sess.Replace([]provider.Message{
		{Role: provider.RoleSystem, Content: anchoredFullPrompt},
		{Role: provider.RoleUser, Content: "task"},
		{Role: provider.RoleUser, Content: "<compaction-summary>\nprior work folded", Origin: provider.MessageOriginHost},
		{Role: provider.RoleUser, Content: "continue"},
	})
	if !a.anchoredPromoted() {
		t.Fatal("compaction summary must keep the session promoted")
	}
	// Even with no promotion evidence left, the process latch holds.
	sess.Replace([]provider.Message{{Role: provider.RoleUser, Content: "x"}})
	if !a.anchoredPromoted() {
		t.Fatal("in-process promotion latch must never demote")
	}
}

// TestAnchoredResumeReconstructsPromotion simulates a process restart: a fresh
// Agent over the same durable messages (full prompt + assistant reply) must
// immediately run on the full surface.
func TestAnchoredResumeReconstructsPromotion(t *testing.T) {
	sess := NewSession(anchoredFullPrompt)
	sess.Add(provider.Message{Role: provider.RoleUser, Content: "task"})
	sess.Add(provider.Message{Role: provider.RoleAssistant, Content: "earlier reply"})
	mp := testutil.NewMock("m", testutil.Turn{Text: "continue"})
	a := anchoredTestAgent(mp, sess)
	if !a.anchoredPromoted() {
		t.Fatal("resumed session with an assistant reply must be promoted")
	}
	if err := a.Run(context.Background(), "more work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := mp.Requests()[0]
	if got := schemaNames(req.Tools); len(got) != 7 {
		t.Fatalf("resumed promoted session must use the full catalog, got %v", got)
	}
	if req.Messages[0].Content != anchoredFullPrompt {
		t.Fatalf("resumed session must send the full prompt, got %q", req.Messages[0].Content)
	}
}

// TestAnchoredMissingBootstrapToolDegradesToFullCatalog verifies a
// composition drift (registry without bash) degrades to the full catalog with
// a one-time warning instead of failing requests, and that the degraded path
// keeps the diagnostics and notices consistent with what is actually sent
// (full prompt, no "minimal surface" claim).
func TestAnchoredMissingBootstrapToolDegradesToFullCatalog(t *testing.T) {
	reg := tool.NewRegistry()
	for _, n := range []string{"read_file", "edit_file", "write_file"} {
		reg.Add(anchoredFakeTool{name: n})
	}
	sink := &recordingSink{}
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	sess := NewSession(anchoredFullPrompt)
	a := New(mp, reg, sess, Options{AnchoredBootstrapSystemPrompt: anchoredBootstrapPrompt}, sink)
	if err := a.Run(context.Background(), "work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := mp.Requests()[0]
	if got := schemaNames(req.Tools); len(got) != 3 {
		t.Fatalf("missing bash must degrade to the full catalog, got %v", got)
	}
	if req.Messages[0].Content == anchoredBootstrapPrompt {
		t.Fatal("degraded request must not swap the system prompt")
	}
	// Prefix shape must reflect the full prompt actually sent, not the
	// bootstrap prefix the mechanism would have used.
	shape := a.capturePrefixShape(req.Tools)
	if shape.SystemHash != shortHash(anchoredFullPrompt) {
		t.Fatalf("degraded prefix shape must hash the full prompt, got %s", shape.SystemHash)
	}
	// The bootstrap announcement must not fire on the degraded path.
	for _, n := range sink.notices {
		if strings.Contains(n, "minimal surface") {
			t.Fatalf("degraded path must not announce the minimal surface, got %q", n)
		}
	}
}

// TestAnchoredBootstrapPromptNoopWhenSessionAlreadyBootstrap guards the
// idempotence boundary: a session whose system prompt already equals the
// bootstrap prefix is left untouched.
func TestAnchoredBootstrapPromptNoopWhenSessionAlreadyBootstrap(t *testing.T) {
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	sess := NewSession(anchoredBootstrapPrompt)
	a := anchoredTestAgent(mp, sess)
	if a.anchoredArmed() {
		t.Fatal("session already on the bootstrap prefix must not arm")
	}
	if err := a.Run(context.Background(), "work"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := mp.Requests()[0]
	if got := schemaNames(req.Tools); len(got) != 7 {
		t.Fatalf("unarmed session must use the full catalog, got %v", got)
	}
}

// TestAnchoredSkipsBootstrapForURLRequest verifies the user requirement that a
// conversation starting with a URL/web page does NOT use the bootstrap mode:
// the first request keeps the full catalog and the full system prompt, and no
// "minimal surface" notice is emitted.
func TestAnchoredSkipsBootstrapForURLRequest(t *testing.T) {
	sink := &recordingSink{}
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	sess := NewSession(anchoredFullPrompt)
	a := New(mp, anchoredRegistry(), sess, Options{AnchoredBootstrapSystemPrompt: anchoredBootstrapPrompt}, sink)
	if err := a.Run(context.Background(), "fetch https://example.com/docs and summarize the API"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := mp.Requests()[0]
	if got := schemaNames(req.Tools); len(got) != 7 {
		t.Fatalf("URL request must skip the bootstrap and use the full catalog, got %v", got)
	}
	if req.Messages[0].Content != anchoredFullPrompt {
		t.Fatalf("URL request must keep the full system prompt, got %q", req.Messages[0].Content)
	}
	var bootstrap, skipped bool
	for _, n := range sink.notices {
		if strings.Contains(n, "minimal surface") {
			bootstrap = true
		}
		if strings.Contains(n, "skipped") {
			skipped = true
		}
	}
	if bootstrap {
		t.Error("URL request must not announce the minimal bootstrap surface")
	}
	if !skipped {
		t.Error("URL request must announce the bootstrap skip")
	}
}

// TestAnchoredWWWURLSkipsBootstrap covers the bare www. form of a URL.
func TestAnchoredWWWURLSkipsBootstrap(t *testing.T) {
	mp := testutil.NewMock("m", testutil.Turn{Text: "done"})
	sess := NewSession(anchoredFullPrompt)
	a := anchoredTestAgent(mp, sess)
	if err := a.Run(context.Background(), "open www.deepseek.com in the browser"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	req := mp.Requests()[0]
	if got := schemaNames(req.Tools); len(got) != 7 {
		t.Fatalf("www URL request must use the full catalog, got %v", got)
	}
}

func containsStr(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

// TestAnchoredNoticeEmitsPhaseTransitions verifies the one-shot notices reach
// the sink (bootstrap announcement on request #1, promotion on request #2).
func TestAnchoredNoticeEmitsPhaseTransitions(t *testing.T) {
	sink := &recordingSink{}
	mp := testutil.NewMock("m",
		testutil.Turn{ToolCalls: []provider.ToolCall{{ID: "r1", Name: "read_file", Arguments: `{"path":"a.go"}`}}},
		testutil.Turn{Text: "done"},
	)
	sess := NewSession(anchoredFullPrompt)
	a := New(mp, anchoredRegistry(), sess, Options{AnchoredBootstrapSystemPrompt: anchoredBootstrapPrompt}, sink)
	if err := a.Run(context.Background(), "review"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	var bootstrap, promoted bool
	for _, n := range sink.notices {
		if strings.Contains(n, "minimal surface") {
			bootstrap = true
		}
		if strings.Contains(n, "promoted") {
			promoted = true
		}
	}
	if !bootstrap {
		t.Error("missing bootstrap announcement notice")
	}
	if !promoted {
		t.Error("missing promotion notice")
	}
}

type recordingSink struct {
	notices []string
}

func (s *recordingSink) Emit(ev event.Event) {
	if ev.Kind == event.Notice {
		s.notices = append(s.notices, ev.Text)
	}
}
