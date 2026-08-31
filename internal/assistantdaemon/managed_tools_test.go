package assistantdaemon

import (
	"io"
	"testing"

	"workground2/internal/assistant"
	"workground2/internal/control"
	"workground2/internal/tool"
)

func TestDaemonManagedToolsCarryContextAndSupervisorStaysReadOnly(t *testing.T) {
	store, err := assistant.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(assistant.CreateInput{
		RequestID: "create-helper",
		Assistant: assistant.Assistant{ID: "helper", Name: "Helper", Mission: "inspect", Scope: assistant.ScopeGlobal, Lifecycle: assistant.LifecycleActive, Policy: assistant.DefaultPolicy()},
	}); err != nil {
		t.Fatal(err)
	}
	r := &Runtime{store: store}
	r.sessionControl = &daemonSessionControl{store: store, stderr: io.Discard, live: map[string]*control.Controller{}}
	managed := toolMap(daemonManagedSessionTools(r, "helper", "execution"))
	for _, name := range []string{"memory_search", "memory_remember", "project_status", "project_constraints_get", "session_list", "session_status", "session_read"} {
		if managed[name] == nil {
			t.Errorf("managed Session missing %s", name)
		}
	}
	supervisor := daemonSupervisorTools(r, "helper")
	for _, item := range supervisor {
		if !item.ReadOnly() {
			t.Errorf("supervisor unexpectedly received write tool %s", item.Name())
		}
	}
	if toolMap(supervisor)["memory_remember"] != nil {
		t.Fatal("supervisor received memory_remember")
	}
}

func toolMap(items []tool.Tool) map[string]tool.Tool {
	out := make(map[string]tool.Tool, len(items))
	for _, item := range items {
		out[item.Name()] = item
	}
	return out
}
