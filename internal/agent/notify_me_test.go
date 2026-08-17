package agent

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestNotifyMeToolCreatesDurableNotification(t *testing.T) {
	var first map[string]any
	calls := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if err := json.NewDecoder(r.Body).Decode(&first); err != nil {
			t.Fatal(err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"D-TEST","status":"applied"}`))
	}))
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server.Listener = listener
	server.Start()
	defer server.Close()

	stateDir := t.TempDir()
	port := listener.Addr().(*net.TCPAddr).Port
	if err := os.WriteFile(filepath.Join(stateDir, notifyMePortFile), []byte(strconv.Itoa(port)), 0o644); err != nil {
		t.Fatal(err)
	}
	tool := newNotifyMeTool(stateDir, server.Client())
	ctx := WithParentSession(context.Background(), "session-a")
	ctx = withCallContext(ctx, "call-a", nil, nil, false)
	out, err := tool.Execute(ctx, json.RawMessage(`{"title":"构建完成","task_summary":"修复版已通过测试。","why_now":"可以开始验收。"}`))
	if err != nil || !strings.Contains(out, "D-TEST") {
		t.Fatalf("Execute = %q, %v", out, err)
	}
	if calls != 1 || first["kind"] != "notify" || first["sessionId"] != "session-a" || first["agentId"] != "workground2" {
		t.Fatalf("request calls=%d payload=%+v", calls, first)
	}
	if key, _ := first["idempotencyKey"].(string); !strings.HasPrefix(key, "notify-me:") {
		t.Fatalf("idempotency key = %q", key)
	}
}

func TestNotifyMeToolFailsClearlyWithoutDesktop(t *testing.T) {
	tool := newNotifyMeTool(t.TempDir(), http.DefaultClient)
	ctx := WithParentSession(context.Background(), "session-a")
	_, err := tool.Execute(ctx, json.RawMessage(`{"title":"完成","task_summary":"任务已完成。"}`))
	if err == nil || !strings.Contains(err.Error(), "Desktop is not running") {
		t.Fatalf("error = %v", err)
	}
}

func TestNotifyMeKeyRetriesSameCallIdempotently(t *testing.T) {
	a := notifyMeKey("session", "call", "title", "summary", "why")
	b := notifyMeKey("session", "call", "title", "summary", "why")
	c := notifyMeKey("session", "next-call", "title", "summary", "why")
	if a != b || a == c {
		t.Fatalf("keys = %q %q %q", a, b, c)
	}
}
