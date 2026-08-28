package assistanttool

import (
	"context"
	"encoding/json"
	"testing"

	"workground2/internal/tool"
)

func exec(t *testing.T, tl tool.Tool, args map[string]any) (map[string]any, string) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out, err := tl.Execute(context.Background(), raw)
	if err != nil {
		t.Fatalf("%s Execute: %v", tl.Name(), err)
	}
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("%s returned invalid JSON %q: %v", tl.Name(), out, err)
	}
	status, _ := res["status"].(string)
	return res, status
}

func TestMemoryToolsLifecycle(t *testing.T) {
	store := newTestStore(t)
	id := "helper-a"

	snapshot, err := store.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	memRev := snapshot.Memory.Revision

	remember := NewMemoryRememberTool(store, id)
	res, status := exec(t, remember, map[string]any{
		"kind": "facts", "body": "the deploy runs at 02:00 UTC",
		"source": "run-1", "request_id": "mem-1", "expected_revision": memRev,
	})
	if status != "accepted" {
		t.Fatalf("remember status = %q, res = %v", status, res)
	}

	search := NewMemorySearchTool(store, id)
	_, status = exec(t, search, map[string]any{"query": "deploy"})
	if status != "accepted" {
		t.Fatalf("search status = %q", status)
	}
	// Search result carries the item including its stable ID.
	raw, _ := json.Marshal(map[string]any{"query": "deploy"})
	out, _ := search.Execute(context.Background(), raw)
	var sr struct {
		Items []struct {
			ID   string `json:"id"`
			Kind string `json:"kind"`
			Body string `json:"body"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &sr); err != nil {
		t.Fatal(err)
	}
	if len(sr.Items) != 1 || sr.Items[0].Body != "the deploy runs at 02:00 UTC" {
		t.Fatalf("search items = %+v", sr.Items)
	}

	forget := NewMemoryForgetTool(store, id)
	_, status = exec(t, forget, map[string]any{
		"memory_id": sr.Items[0].ID, "request_id": "forget-1",
		"expected_revision": memRev + 1,
	})
	if status != "accepted" {
		t.Fatalf("forget status = %q", status)
	}
	// The item is gone.
	out2, _ := search.Execute(context.Background(), raw)
	var sr2 struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(out2), &sr2); err != nil {
		t.Fatal(err)
	}
	if len(sr2.Items) != 0 {
		t.Fatalf("items after forget = %+v", sr2.Items)
	}
}

func TestPolicyToolsLifecycle(t *testing.T) {
	store := newTestStore(t)
	id := "helper-a"

	get := NewPolicyGetTool(store, id)
	res, status := exec(t, get, map[string]any{})
	if status != "accepted" {
		t.Fatalf("policy_get status = %q", status)
	}
	rev := int64(res["revision"].(float64))

	update := NewPolicyUpdateTool(store, id)
	// Tightening (approve -> deny) is allowed.
	res, status = exec(t, update, map[string]any{
		"publish": "deny", "expected_revision": rev, "request_id": "pol-1",
	})
	if status != "accepted" {
		t.Fatalf("policy_update status = %q res = %v", status, res)
	}
	policy, ok := res["policy"].(map[string]any)
	if !ok || policy["publish"] != "deny" {
		t.Fatalf("policy after update = %v", res)
	}
	rev = int64(res["revision"].(float64))

	// Widening (deny -> allow) is refused: the Assistant cannot expand its own
	// policy.
	_, status = exec(t, update, map[string]any{
		"network": "allow", "expected_revision": rev, "request_id": "pol-2",
	})
	if status != "blocked_by_policy" {
		t.Fatalf("widen policy_update status = %q, want blocked_by_policy", status)
	}

	// A further tightening bumps the revision.
	_, status = exec(t, update, map[string]any{
		"auto_answer": "ask", "expected_revision": rev, "request_id": "pol-3",
	})
	if status != "accepted" {
		t.Fatalf("tighten policy_update status = %q", status)
	}

	// Stale revision is rejected.
	_, status = exec(t, update, map[string]any{
		"network": "deny", "expected_revision": rev, "request_id": "pol-4",
	})
	if status != "stale" {
		t.Fatalf("stale policy_update status = %q", status)
	}
}
