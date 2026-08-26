package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"workground2/internal/event"
	"workground2/internal/provider"
	"workground2/internal/tool"
)

type permissionSubjectTool struct {
	subject string
	err     error
	ran     bool
}

func (t *permissionSubjectTool) Name() string            { return "semantic_writer" }
func (t *permissionSubjectTool) Description() string     { return "test" }
func (t *permissionSubjectTool) Schema() json.RawMessage { return json.RawMessage(`{"type":"object"}`) }
func (t *permissionSubjectTool) ReadOnly() bool          { return false }
func (t *permissionSubjectTool) PlanModeSafe() bool      { return false }
func (t *permissionSubjectTool) Execute(context.Context, json.RawMessage) (string, error) {
	t.ran = true
	return "ok", nil
}
func (t *permissionSubjectTool) PermissionSubject(context.Context, json.RawMessage) (string, error) {
	return t.subject, t.err
}

type permissionSubjectGate struct {
	subject    string
	plainCalls int
}

func (g *permissionSubjectGate) Check(context.Context, string, json.RawMessage, bool) (bool, string, error) {
	g.plainCalls++
	return false, "plain gate path", nil
}
func (g *permissionSubjectGate) CheckSubject(_ context.Context, _, subject string, _ json.RawMessage, _ bool) (bool, string, error) {
	g.subject = subject
	return true, "", nil
}

func TestExecuteUsesAuthoritativePermissionSubject(t *testing.T) {
	registry := tool.NewRegistry()
	writer := &permissionSubjectTool{subject: "publish"}
	registry.Add(writer)
	gate := &permissionSubjectGate{}
	a := New(nil, registry, NewSession("system"), Options{Gate: gate}, event.Discard)
	output, err := a.ExecuteSyntheticToolCall(context.Background(), "test", provider.ToolCall{
		ID: "call-1", Name: writer.Name(), Arguments: `{}`,
	})
	if err != nil || output != "ok" || !writer.ran {
		t.Fatalf("output=%q ran=%v err=%v", output, writer.ran, err)
	}
	if gate.subject != "publish" || gate.plainCalls != 0 {
		t.Fatalf("subject=%q plainCalls=%d", gate.subject, gate.plainCalls)
	}
}

func TestPermissionSubjectFailureStopsBeforeExecute(t *testing.T) {
	registry := tool.NewRegistry()
	writer := &permissionSubjectTool{err: errors.New("stale page")}
	registry.Add(writer)
	a := New(nil, registry, NewSession("system"), Options{Gate: &permissionSubjectGate{}}, event.Discard)
	_, err := a.ExecuteSyntheticToolCall(context.Background(), "test", provider.ToolCall{
		ID: "call-2", Name: writer.Name(), Arguments: `{}`,
	})
	if err == nil || writer.ran {
		t.Fatalf("ran=%v err=%v, want fail closed", writer.ran, err)
	}
}
